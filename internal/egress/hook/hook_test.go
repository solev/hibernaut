package hook

import (
	"slices"
	"strings"
	"testing"
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
	got := planRemove(nat(saveJump, saveE2B, saveLegacy, "-N HIBERNAUT", saveLink, save443))
	want := []string{
		"-D PREROUTING -i veth-+ -m comment --comment hibernaut -j HIBERNAUT",
		"-D PREROUTING -i veth-+ -p tcp -m tcp --dport 443 -m comment --comment hibernaut -j REDIRECT --to-ports 5443",
		"-F HIBERNAUT",
		"-X HIBERNAUT",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("planRemove = %q, want %q", got, want)
	}
	if got := planRemove(nat(saveE2B)); got != nil {
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
