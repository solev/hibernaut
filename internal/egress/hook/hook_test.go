package hook

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	saveJump   = `-A PREROUTING -i veth-+ -m comment --comment hibernaut -j HIBERNAUT`
	saveLink   = `-A HIBERNAUT -d 198.18.0.0/15 -p tcp -j REDIRECT --to-ports 5444`
	save443    = `-A HIBERNAUT -p tcp -m tcp --dport 443 -j REDIRECT --to-ports 5443`
	saveLegacy = `-A PREROUTING -i veth-+ -p tcp -m tcp --dport 443 -m comment --comment hibernaut -j REDIRECT --to-ports 5443`
	saveE2B    = `-A PREROUTING -i veth-7 -p tcp -m tcp --dport 80 -j REDIRECT --to-ports 5016`
)

var linksOn = Rule{ProxyPort: 5443, LinkPort: 5444, LinkCIDR: "198.18.0.0/15"}

func nat(lines ...string) natTable {
	return parseNat("-P PREROUTING ACCEPT\n" + strings.Join(lines, "\n") + "\n")
}

func mustLayout(t *testing.T, r Rule) layout {
	t.Helper()
	want, err := r.layout()
	if err != nil {
		t.Fatal(err)
	}
	return want
}

func TestPlanInstallsChainBeforeJumpAndDeletesLegacyLast(t *testing.T) {
	got := plan(nat(saveLegacy, saveE2B), mustLayout(t, linksOn), false)
	want := []string{
		":HIBERNAUT - [0:0]",
		"-A HIBERNAUT -d 198.18.0.0/15 -p tcp -j REDIRECT --to-ports 5444",
		"-A HIBERNAUT -p tcp --dport 443 -j REDIRECT --to-ports 5443",
		"-I PREROUTING 1 -i veth-+ -m comment --comment hibernaut -j HIBERNAUT",
		"-D PREROUTING -i veth-+ -p tcp -m tcp --dport 443 -m comment --comment hibernaut -j REDIRECT --to-ports 5443",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("plan =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestPlanIsSilentWhenSaveRendersMatchTCP(t *testing.T) {
	table := nat(saveJump, saveE2B, "-N HIBERNAUT", saveLink, save443)
	if got := plan(table, mustLayout(t, linksOn), false); got != nil {
		t.Fatalf("plan for an installed layout = %q, want nothing", got)
	}
	toPort := nat(saveJump, "-N HIBERNAUT", saveLink, strings.Replace(save443, "--to-ports", "--to-port", 1))
	if got := plan(toPort, mustLayout(t, linksOn), false); got != nil {
		t.Fatalf("plan with --to-port = %q, want nothing", got)
	}
	legacy := nat(saveLegacy, saveE2B)
	if got := plan(legacy, mustLayout(t, Rule{ProxyPort: 5443}), false); got != nil {
		t.Fatalf("plan for the links-off rule = %q, want nothing", got)
	}
}

func TestPlanRepairsForeignRuleAboveJump(t *testing.T) {
	got := plan(nat("-A PREROUTING -p tcp -m tcp --dport 443 -j RETURN", saveJump, "-N HIBERNAUT", saveLink, save443), mustLayout(t, linksOn), false)
	want := []string{
		"-D PREROUTING -i veth-+ -m comment --comment hibernaut -j HIBERNAUT",
		"-I PREROUTING 1 -i veth-+ -m comment --comment hibernaut -j HIBERNAUT",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("plan = %q, want %q", got, want)
	}
}

func TestPlanRewritesHalfWrittenChainInOneStep(t *testing.T) {
	got := plan(nat(saveJump, saveLegacy, "-N HIBERNAUT", saveLink), mustLayout(t, linksOn), false)
	want := []string{
		":HIBERNAUT - [0:0]",
		"-A HIBERNAUT -d 198.18.0.0/15 -p tcp -j REDIRECT --to-ports 5444",
		"-A HIBERNAUT -p tcp --dport 443 -j REDIRECT --to-ports 5443",
		"-D PREROUTING -i veth-+ -p tcp -m tcp --dport 443 -m comment --comment hibernaut -j REDIRECT --to-ports 5443",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("plan = %q, want %q", got, want)
	}
}

func TestPlanDeletesEveryCopyOfTheJumpBeforeInsertingIt(t *testing.T) {
	got := plan(nat(saveJump, saveE2B, saveJump, "-N HIBERNAUT", saveLink, save443), mustLayout(t, linksOn), false)
	want := []string{
		"-D PREROUTING -i veth-+ -m comment --comment hibernaut -j HIBERNAUT",
		"-D PREROUTING -i veth-+ -m comment --comment hibernaut -j HIBERNAUT",
		"-I PREROUTING 1 -i veth-+ -m comment --comment hibernaut -j HIBERNAUT",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("plan = %q, want %q", got, want)
	}
}

func TestPlanDeletesUnrecognisedHibernautRules(t *testing.T) {
	odd := `-A PREROUTING -p udp -m comment --comment hibernaut -j RETURN`
	quoted := `-A PREROUTING -i veth-+ -m comment --comment "hibernaut" -j ACCEPT`
	notOurs := `-A PREROUTING -m comment --comment "\"hibernaut\"" -j ACCEPT`
	table := nat(saveJump, odd, quoted, notOurs, "-N HIBERNAUT", saveLink, save443)
	got := plan(table, mustLayout(t, linksOn), false)
	want := []string{
		"-D PREROUTING -p udp -m comment --comment hibernaut -j RETURN",
		`-D PREROUTING -i veth-+ -m comment --comment "hibernaut" -j ACCEPT`,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("plan = %q, want %q", got, want)
	}
	if got := plan(table, mustLayout(t, linksOn), true); got != nil {
		t.Fatalf("plan skipping unknown rules = %q, want nothing", got)
	}
}

func TestPlanLinksOffTakesTheChainOut(t *testing.T) {
	foreign := `-A OUTPUT -j HIBERNAUT`
	got := plan(nat(saveJump, "-N HIBERNAUT", saveLink, save443, foreign), mustLayout(t, Rule{ProxyPort: 5443}), false)
	want := []string{
		"-I PREROUTING 1 -i veth-+ -p tcp --dport 443 -m comment --comment hibernaut -j REDIRECT --to-ports 5443",
		"-D PREROUTING -i veth-+ -m comment --comment hibernaut -j HIBERNAUT",
		"-D OUTPUT -j HIBERNAUT",
		"-F HIBERNAUT",
		"-X HIBERNAUT",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("plan = %q, want %q", got, want)
	}
}

func TestPlanRemoveLeavesNothing(t *testing.T) {
	got := planRemove(nat(saveJump, saveE2B, saveLegacy, "-N HIBERNAUT", saveLink, save443), false)
	want := []string{
		"-D PREROUTING -i veth-+ -m comment --comment hibernaut -j HIBERNAUT",
		"-D PREROUTING -i veth-+ -p tcp -m tcp --dport 443 -m comment --comment hibernaut -j REDIRECT --to-ports 5443",
		"-F HIBERNAUT",
		"-X HIBERNAUT",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("planRemove = %q, want %q", got, want)
	}
	if got := planRemove(nat(saveE2B), false); got != nil {
		t.Fatalf("planRemove with nothing of ours = %q, want nothing", got)
	}
}

func TestLayoutRejectsHalfConfiguredLinks(t *testing.T) {
	for _, r := range []Rule{
		{ProxyPort: 0},
		{ProxyPort: 5443, LinkPort: 5444},
		{ProxyPort: 5443, LinkCIDR: "198.18.0.0/15"},
		{ProxyPort: 5443, LinkPort: 5444, LinkCIDR: "fd00::/8"},
		{ProxyPort: 5443, LinkPort: 70000, LinkCIDR: "198.18.0.0/15"},
	} {
		if _, err := r.layout(); err == nil {
			t.Errorf("layout(%+v) accepted", r)
		}
	}
	got := mustLayout(t, Rule{ProxyPort: 5443, LinkPort: 5444, LinkCIDR: "198.18.7.1/15"})
	if got.chain[0][1] != "198.18.0.0/15" {
		t.Errorf("link range = %s, want the masked 198.18.0.0/15 iptables prints", got.chain[0][1])
	}
}

func TestSplitFieldsUnquotesLikeIptablesRestore(t *testing.T) {
	got := splitFields(`-m comment --comment "a \"b\" \\c" -j ACCEPT`)
	want := []string{"-m", "comment", "--comment", `a "b" \c`, "-j", "ACCEPT"}
	if !slices.Equal(got, want) {
		t.Fatalf("splitFields = %q, want %q", got, want)
	}
}

// fakeNat is a nat table that takes an iptables-restore --noflush transaction whole or not at all,
// as the kernel does. Deleting a rule listed in stuck fails, as for a rule iptables cannot match.
type fakeNat struct {
	chains  map[string][]string
	order   []string
	stuck   map[string]bool
	failAll error
}

// newFakeNat builds a table from -N and -A lines, with the -A lines in stuck undeletable.
func newFakeNat(stuck []string, lines ...string) *fakeNat {
	f := &fakeNat{chains: map[string][]string{prerouting: nil}, stuck: map[string]bool{}}
	for _, line := range stuck {
		chain, spec, _ := strings.Cut(strings.TrimPrefix(line, "-A "), " ")
		f.stuck[chain+" "+canonical(splitFields(spec))] = true
	}
	for _, line := range lines {
		if err := f.apply(strings.Replace(line, "-N ", ":", 1)); err != nil {
			panic(err)
		}
	}
	return f
}

func (f *fakeNat) io() natIO {
	return natIO{
		list: func(context.Context) (natTable, error) { return parseNat(f.save()), nil },
		restore: func(_ context.Context, lines []string) error {
			if f.failAll != nil {
				return f.failAll
			}
			next := &fakeNat{chains: map[string][]string{}, order: slices.Clone(f.order), stuck: f.stuck}
			for c, rules := range f.chains {
				next.chains[c] = slices.Clone(rules)
			}
			for i, line := range lines {
				if err := next.apply(line); err != nil {
					return fmt.Errorf("iptables-restore: line %d failed: %v", i+2, err)
				}
			}
			*f = *next
			return nil
		},
	}
}

func (f *fakeNat) save() string {
	lines := []string{"-P PREROUTING ACCEPT"}
	for _, c := range f.order {
		lines = append(lines, "-N "+c)
	}
	for _, c := range append([]string{prerouting}, f.order...) {
		for _, spec := range f.chains[c] {
			lines = append(lines, "-A "+c+" "+spec)
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

func (f *fakeNat) apply(line string) error {
	if name, ok := strings.CutPrefix(line, ":"); ok {
		name, _, _ = strings.Cut(name, " ")
		if _, ok := f.chains[name]; !ok {
			f.order = append(f.order, name)
		}
		f.chains[name] = nil
		return nil
	}
	op, rest, _ := strings.Cut(line, " ")
	chain, spec, _ := strings.Cut(rest, " ")
	rules, ok := f.chains[chain]
	if !ok {
		return fmt.Errorf("no chain %s", chain)
	}
	switch op {
	case "-A":
		f.chains[chain] = append(rules, spec)
	case "-I":
		pos, spec, _ := strings.Cut(spec, " ")
		if pos != "1" {
			return fmt.Errorf("unexpected position %s", pos)
		}
		f.chains[chain] = append([]string{spec}, rules...)
	case "-D":
		key := canonical(splitFields(spec))
		i := slices.IndexFunc(rules, func(r string) bool { return canonical(splitFields(r)) == key })
		if i < 0 || f.stuck[chain+" "+key] {
			return errors.New("bad rule (does a matching rule exist in that chain?)")
		}
		f.chains[chain] = slices.Delete(rules, i, i+1)
	case "-F":
		f.chains[chain] = nil
	case "-X":
		if len(rules) > 0 {
			return errors.New("directory not empty")
		}
		for _, rs := range f.chains {
			for _, r := range rs {
				if (natRule{fields: splitFields(r)}).refersToChain() {
					return errors.New("too many links")
				}
			}
		}
		delete(f.chains, chain)
		f.order = slices.DeleteFunc(f.order, func(c string) bool { return c == chain })
	default:
		return fmt.Errorf("unknown command %q", line)
	}
	return nil
}

// onlyStray reports whether err wraps ErrStrayRule and nothing else.
func onlyStray(err error) bool {
	return errors.Is(err, ErrStrayRule) && errors.Unwrap(err) == ErrStrayRule
}

const saveStray = `-A PREROUTING -p udp -m comment --comment hibernaut -j RETURN`

func TestEnsureInstallsTheHookAroundAStuckStrayRule(t *testing.T) {
	fake := newFakeNat([]string{saveStray}, saveLegacy, saveStray)
	want := mustLayout(t, linksOn)
	installed := strings.Join([]string{
		"-P PREROUTING ACCEPT",
		"-N HIBERNAUT",
		saveJump,
		saveStray,
		saveLink,
		"-A HIBERNAUT -p tcp --dport 443 -j REDIRECT --to-ports 5443",
	}, "\n") + "\n"

	changed, err := ensure(t.Context(), fake.io(), want)
	if !changed || !onlyStray(err) {
		t.Fatalf("first ensure = %v, %v, want changed and only ErrStrayRule", changed, err)
	}
	if !strings.Contains(err.Error(), `"`+saveStray+`"`) {
		t.Fatalf("error %q does not name the stray rule", err)
	}
	if got := fake.save(); got != installed {
		t.Fatalf("table after ensure:\n%s\nwant\n%s", got, installed)
	}
	for range 3 {
		changed, err := ensure(t.Context(), fake.io(), want)
		if changed || !onlyStray(err) {
			t.Fatalf("later ensure = %v, %v, want unchanged and only ErrStrayRule", changed, err)
		}
		if got := fake.save(); got != installed {
			t.Fatalf("table after a later ensure:\n%s\nwant\n%s", got, installed)
		}
	}
}

func TestRemoveTakesOursAndReportsAStuckStrayRule(t *testing.T) {
	strayJump := `-A PREROUTING -p udp -m comment --comment hibernaut -j HIBERNAUT`
	for _, tc := range []struct {
		name, stray, left string
	}{
		{"beside the chain", saveStray, "-P PREROUTING ACCEPT\n" + saveStray + "\n"},
		{"into the chain", strayJump, "-P PREROUTING ACCEPT\n-N HIBERNAUT\n" + strayJump + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeNat([]string{tc.stray}, saveJump, tc.stray, saveLegacy, "-N HIBERNAUT", saveLink, save443)
			for range 2 {
				err := remove(t.Context(), fake.io())
				if !onlyStray(err) || !strings.Contains(err.Error(), `"`+tc.stray+`"`) {
					t.Fatalf("remove = %v, want only ErrStrayRule naming %q", err, tc.stray)
				}
				if got := fake.save(); got != tc.left {
					t.Fatalf("table after remove:\n%s\nwant\n%s", got, tc.left)
				}
			}
		})
	}
}

func TestEnsureAndRemoveKeepOtherFailuresErrors(t *testing.T) {
	locked := errors.New("xtables lock timed out")
	for _, tc := range []struct {
		name  string
		fake  *fakeNat
		apply func(natIO) error
	}{
		{"ensure, a known rule stuck", newFakeNat([]string{saveLegacy}, saveLegacy),
			func(nat natIO) error { _, err := ensure(t.Context(), nat, mustLayout(t, linksOn)); return err }},
		{"ensure, restore broken", &fakeNat{chains: map[string][]string{prerouting: nil}, failAll: locked},
			func(nat natIO) error { _, err := ensure(t.Context(), nat, mustLayout(t, linksOn)); return err }},
		{"ensure, restore broken beside a stray rule", &fakeNat{chains: map[string][]string{prerouting: {"-p udp -m comment --comment hibernaut -j RETURN"}}, failAll: locked},
			func(nat natIO) error { _, err := ensure(t.Context(), nat, mustLayout(t, linksOn)); return err }},
		{"remove, restore broken", &fakeNat{chains: map[string][]string{prerouting: {"-i veth-+ -m comment --comment hibernaut -j HIBERNAUT"}, chainName: nil}, order: []string{chainName}, failAll: locked},
			func(nat natIO) error { return remove(t.Context(), nat) }},
		{"remove, restore broken beside a stray rule", &fakeNat{chains: map[string][]string{prerouting: {"-i veth-+ -m comment --comment hibernaut -j HIBERNAUT", "-p udp -m comment --comment hibernaut -j RETURN"}, chainName: nil}, order: []string{chainName}, failAll: locked},
			func(nat natIO) error { return remove(t.Context(), nat) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := tc.fake.save()
			err := tc.apply(tc.fake.io())
			if err == nil || errors.Is(err, ErrStrayRule) {
				t.Fatalf("got %v, want an ordinary error", err)
			}
			if got := tc.fake.save(); got != before {
				t.Fatalf("table changed on a failure:\n%s\nwas\n%s", got, before)
			}
		})
	}
}

func TestKeepWarnsAboutAStrayRuleAndCarriesOn(t *testing.T) {
	strayA := fmt.Errorf("%w: A", ErrStrayRule)
	strayB := fmt.Errorf("%w: B", ErrStrayRule)
	other := errors.New("xtables lock timed out")
	type result struct {
		changed bool
		err     error
	}
	script := []result{{true, strayA}, {false, strayA}, {false, strayB}, {false, strayB}, {true, nil}, {false, strayB}, {false, other}, {false, strayB}, {false, nil}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	apply := func(ctx context.Context) (bool, error) {
		if calls == len(script) {
			cancel()
			return false, ctx.Err()
		}
		calls++
		return script[calls-1].changed, script[calls-1].err
	}
	var logs []string
	logf := func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }
	if err := keep(ctx, time.Millisecond, logf, apply); err != nil {
		t.Fatalf("keep = %v, want nil", err)
	}
	want := []string{strayA.Error(), strayB.Error(), "repaired the hibernaut iptables hook", strayB.Error(), "Ensure failed: " + other.Error()}
	if !slices.Equal(logs, want) {
		t.Fatalf("logs =\n%s\nwant\n%s", strings.Join(logs, "\n"), strings.Join(want, "\n"))
	}

	err := keep(t.Context(), time.Hour, logf, func(context.Context) (bool, error) { return false, other })
	if !errors.Is(err, other) {
		t.Fatalf("keep after a failed first call = %v, want %v", err, other)
	}
}

func TestKeepKeepsTheHookAroundAStuckStrayRule(t *testing.T) {
	fake := newFakeNat([]string{saveStray}, saveStray)
	want := mustLayout(t, linksOn)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	apply := func(ctx context.Context) (bool, error) {
		if calls++; calls == 5 {
			cancel()
		}
		return ensure(ctx, fake.io(), want)
	}
	var logs []string
	logf := func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }
	if err := keep(ctx, time.Millisecond, logf, apply); err != nil {
		t.Fatalf("keep = %v, want nil", err)
	}
	if calls != 5 {
		t.Fatalf("keep stopped after %d calls", calls)
	}
	// The first transaction installs the layout too, so its message differs from the later ones.
	if len(logs) != 2 || !strings.HasPrefix(logs[0], ErrStrayRule.Error()) || !strings.HasPrefix(logs[1], ErrStrayRule.Error()) {
		t.Fatalf("logs = %q, want two stray-rule warnings", logs)
	}
	if rest := plan(parseNat(fake.save()), want, true); rest != nil {
		t.Fatalf("hook not installed, still to do %q", rest)
	}
}

func TestEnsureLinksOffEmptiesTheChainAStuckStrayRuleJumpsTo(t *testing.T) {
	strayJump := `-A PREROUTING -i eth9 -m comment --comment hibernaut -j HIBERNAUT`
	fake := newFakeNat([]string{strayJump}, saveJump, strayJump, "-N HIBERNAUT", saveLink, save443)
	want := mustLayout(t, Rule{ProxyPort: 5443})
	installed := strings.Join([]string{
		"-P PREROUTING ACCEPT",
		"-N HIBERNAUT",
		"-A PREROUTING -i veth-+ -p tcp --dport 443 -m comment --comment hibernaut -j REDIRECT --to-ports 5443",
		strayJump,
	}, "\n") + "\n"

	changed, err := ensure(t.Context(), fake.io(), want)
	if !changed || !onlyStray(err) {
		t.Fatalf("first ensure = %v, %v, want changed and only ErrStrayRule", changed, err)
	}
	if got := fake.save(); got != installed {
		t.Fatalf("table after ensure:\n%s\nwant\n%s", got, installed)
	}
	changed, err = ensure(t.Context(), fake.io(), want)
	if changed || !onlyStray(err) {
		t.Fatalf("next ensure = %v, %v, want unchanged and only ErrStrayRule", changed, err)
	}
	if got := fake.save(); got != installed {
		t.Fatalf("table after the next ensure:\n%s\nwant\n%s", got, installed)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	apply := func(ctx context.Context) (bool, error) {
		if calls++; calls == 5 {
			cancel()
		}
		return ensure(ctx, fake.io(), want)
	}
	if err := keep(ctx, time.Millisecond, nil, apply); err != nil || calls != 5 {
		t.Fatalf("keep = %v after %d calls, want nil after 5", err, calls)
	}

	// A restart after Remove, which leaves the chain empty, installs the hook again.
	if err := remove(t.Context(), fake.io()); !onlyStray(err) {
		t.Fatalf("remove = %v, want only ErrStrayRule", err)
	}
	if changed, err := ensure(t.Context(), fake.io(), want); !changed || !onlyStray(err) {
		t.Fatalf("ensure after remove = %v, %v, want changed and only ErrStrayRule", changed, err)
	}
	if got := fake.save(); got != installed {
		t.Fatalf("table after remove and ensure:\n%s\nwant\n%s", got, installed)
	}
}
