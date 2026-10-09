package linkguard

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/links"
)

// fakeGuard is links.Guard in memory, with the kernel's rules for a gate: Open is create-only
// and refuses a spent token, Extend is all or nothing and fails with ENOENT on a gate that has
// gone, and a gate that has gone never comes back.
type fakeGuard struct {
	mu        sync.Mutex
	installed bool
	tampered  bool
	gates     map[links.Gate]bool
	srcs      map[netip.Addr]uint32
	highest   uint32
	calls     []string
	// Errors to return. extendErr applies before the kernel's own checks.
	installErr, extendErr, installedErr error
	// onOpen runs after each open that worked.
	onOpen func(g links.Gate)
}

func newFakeGuard() *fakeGuard {
	return &fakeGuard{gates: map[links.Gate]bool{}, srcs: map[netip.Addr]uint32{}}
}

func (f *fakeGuard) log(format string, args ...any) {
	f.calls = append(f.calls, fmt.Sprintf(format, args...))
}

func (f *fakeGuard) Install() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log("install")
	if f.installErr != nil {
		return f.installErr
	}
	f.installed, f.tampered = true, false
	f.gates, f.srcs = map[links.Gate]bool{}, map[netip.Addr]uint32{}
	return nil
}

func (f *fakeGuard) Installed() (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log("installed")
	return f.installed && !f.tampered, f.installedErr
}

func (f *fakeGuard) Remove() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.installed = false
	return nil
}

func (f *fakeGuard) Open(g links.Gate) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log("open %s %d", g.Addr, g.Token)
	if g.Token <= f.highest {
		return links.ErrGateSpent
	}
	f.highest = g.Token
	if !f.installed {
		return syscall.ENOENT
	}
	if _, ok := f.srcs[g.Addr]; ok || f.gates[g] {
		return syscall.EEXIST
	}
	f.gates[g], f.srcs[g.Addr] = true, g.Token
	if f.onOpen != nil {
		f.onOpen(g)
	}
	return nil
}

func (f *fakeGuard) Extend(gates []links.Gate) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var names []string
	for _, g := range gates {
		names = append(names, fmt.Sprintf("%s/%d", g.Addr, g.Token))
	}
	f.log("extend %s", strings.Join(names, " "))
	if f.extendErr != nil {
		return f.extendErr
	}
	if len(gates) == 0 {
		return nil
	}
	if !f.installed {
		return syscall.ENOENT
	}
	for _, g := range gates {
		if !f.gates[g] || f.srcs[g.Addr] != g.Token {
			return syscall.ENOENT
		}
	}
	return nil
}

func (f *fakeGuard) Drop(addr netip.Addr) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log("drop %s", addr)
	f.dropLocked(addr)
	return nil
}

func (f *fakeGuard) dropLocked(addr netip.Addr) {
	for g := range f.gates {
		if g.Addr == addr {
			delete(f.gates, g)
		}
	}
	delete(f.srcs, addr)
}

func (f *fakeGuard) Has(g links.Gate) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gates[g] && f.srcs[g.Addr] == g.Token, nil
}

// vanish takes addr's gates out of the kernel, as an expiry does.
func (f *fakeGuard) vanish(addr netip.Addr) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dropLocked(addr)
}

func (f *fakeGuard) set(do func(f *fakeGuard)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	do(f)
}

func (f *fakeGuard) open() map[links.Gate]bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return maps.Clone(f.gates)
}

// takeCalls returns the calls made since the last take.
func (f *fakeGuard) takeCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.calls
	f.calls = nil
	return c
}

type fakeRuns struct {
	mu   sync.Mutex
	live map[string]links.Run
	err  error
}

func (r *fakeRuns) Current(id string) (links.Run, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if run, ok := r.live[id]; ok {
		return run, nil
	}
	return "", links.ErrNotRunning
}

func (r *fakeRuns) Watch(string, links.Run) (<-chan struct{}, error) { return closedChan, nil }

func (r *fakeRuns) Live() (map[string]links.Run, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return maps.Clone(r.live), r.err
}

func (r *fakeRuns) set(id string, run links.Run) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if run == "" {
		delete(r.live, id)
		return
	}
	r.live[id] = run
}

// fakeResolver puts machine m<n> at 10.11.<n>.2.
type fakeResolver struct {
	runs links.Runs
	mu   sync.Mutex
	// at overrides who At says is at an address.
	at map[netip.Addr]links.Slot
}

func slotAddr(id string) netip.Addr {
	var n byte
	fmt.Sscanf(id, "m%d", &n)
	return netip.AddrFrom4([4]byte{10, 11, n, 2})
}

func (r *fakeResolver) Addr(id string) (netip.Addr, links.Run, error) {
	run, err := r.runs.Current(id)
	return slotAddr(id), run, err
}

func (r *fakeResolver) At(addr netip.Addr) (links.Slot, error) {
	r.mu.Lock()
	slot, ok := r.at[addr]
	r.mu.Unlock()
	if ok {
		return slot, nil
	}
	id := fmt.Sprintf("m%d", addr.As4()[2])
	run, err := r.runs.Current(id)
	return links.Slot{ID: id, Run: run}, err
}

type fakeStore struct {
	mu  sync.Mutex
	doc links.Doc
	err error
}

func (s *fakeStore) Read() (links.Doc, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc := links.Doc{V: s.doc.V, Machines: map[string]*links.Machine{}}
	for id, m := range s.doc.Machines {
		c := *m
		doc.Machines[id] = &c
	}
	return doc, s.err
}

func (s *fakeStore) Update(context.Context, func(*links.Doc) error) error {
	return errors.New("the Keeper never writes links.json")
}

func (s *fakeStore) Fence(context.Context, string, string) (func(), error) {
	return nil, errors.New("the Keeper never fences")
}

// fence fences id, or lifts its fence when owner is empty.
func (s *fakeStore) fence(id, owner string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if owner == "" {
		delete(s.doc.Machines, id)
		return
	}
	s.doc.Machines[id] = &links.Machine{Fence: &links.Fence{Why: "pause", Owner: owner}}
}

func (s *fakeStore) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

type fakeTokens struct {
	mu         sync.Mutex
	next, last uint32
	calls      int
}

func (t *fakeTokens) Next() (uint32, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls++
	if t.next > t.last {
		return 0, links.ErrTokensSpent
	}
	t.next++
	return t.next - 1, nil
}

type rig struct {
	guard  *fakeGuard
	runs   *fakeRuns
	res    *fakeResolver
	store  *fakeStore
	tokens *fakeTokens
	k      *Keeper
	mu     sync.Mutex
	logs   []string
}

func newRig(t *testing.T, keep, selfCheck time.Duration) *rig {
	t.Helper()
	r := &rig{
		guard:  newFakeGuard(),
		runs:   &fakeRuns{live: map[string]links.Run{}},
		store:  &fakeStore{doc: links.Doc{V: 1, Machines: map[string]*links.Machine{}}},
		tokens: &fakeTokens{next: 1, last: links.GuardMaxToken},
	}
	r.res = &fakeResolver{runs: r.runs, at: map[netip.Addr]links.Slot{}}
	k, err := NewKeeper(KeeperConfig{
		Guard:     r.guard,
		Tokens:    r.tokens,
		Runs:      r.runs,
		Resolver:  r.res,
		Store:     r.store,
		Alive:     func(owner string) (bool, error) { return owner == "live", nil },
		Keep:      keep,
		SelfCheck: selfCheck,
		Logf: func(format string, args ...any) {
			r.mu.Lock()
			r.logs = append(r.logs, fmt.Sprintf(format, args...))
			r.mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r.k = k
	return r
}

// stepped is a rig whose cycles the test runs by hand, with the guard installed as Run
// leaves it.
func stepped(t *testing.T) *rig {
	t.Helper()
	r := newRig(t, 0, 0)
	if err := r.guard.Install(); err != nil {
		t.Fatal(err)
	}
	r.guard.takeCalls()
	return r
}

func (r *rig) held(id string) (links.Gate, bool) {
	r.k.mu.Lock()
	defer r.k.mu.Unlock()
	h := r.k.held[id]
	if h == nil {
		return links.Gate{}, false
	}
	return h.gate, true
}

func (r *rig) why(id string) error {
	r.k.mu.Lock()
	defer r.k.mu.Unlock()
	if r.k.last.err != nil {
		return r.k.last.err
	}
	return r.k.last.why[id]
}

func (r *rig) logged(sub string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.ContainsFunc(r.logs, func(l string) bool { return strings.Contains(l, sub) })
}

func isClosed(c <-chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

func mustHold(t *testing.T, r *rig, id string) links.Gate {
	t.Helper()
	g, ok := r.held(id)
	if !ok {
		t.Fatalf("%s has no gate (%v)", id, r.why(id))
	}
	if !r.guard.open()[g] {
		t.Fatalf("%s's gate %v is not in the kernel", id, g)
	}
	return g
}

func TestNewKeeperChecksItsConfig(t *testing.T) {
	r := newRig(t, 0, 0)
	good := r.k.cfg
	tests := map[string]func(c *KeeperConfig){
		"no guard":          func(c *KeeperConfig) { c.Guard = nil },
		"no tokens":         func(c *KeeperConfig) { c.Tokens = nil },
		"no runs":           func(c *KeeperConfig) { c.Runs = nil },
		"no resolver":       func(c *KeeperConfig) { c.Resolver = nil },
		"no store":          func(c *KeeperConfig) { c.Store = nil },
		"no alive":          func(c *KeeperConfig) { c.Alive = nil },
		"negative keep":     func(c *KeeperConfig) { c.Keep = -time.Second },
		"negative check":    func(c *KeeperConfig) { c.SelfCheck = -time.Second },
		"keep at the TTL":   func(c *KeeperConfig) { c.Keep = links.GuardTTL },
		"keep past the TTL": func(c *KeeperConfig) { c.Keep = 2 * links.GuardTTL },
	}
	for name, mutate := range tests {
		c := good
		mutate(&c)
		if _, err := NewKeeper(c); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if r.k.cfg.Keep != links.GuardKeep || r.k.cfg.SelfCheck != links.GuardSelfCheck {
		t.Errorf("defaults: Keep %v, SelfCheck %v", r.k.cfg.Keep, r.k.cfg.SelfCheck)
	}
}

func TestKeeperOpensGatesForLiveUnfencedRuns(t *testing.T) {
	r := stepped(t)
	r.runs.set("m1", "r1")
	r.runs.set("m2", "r2")
	r.runs.set("m3", "r3")
	r.runs.set("m4", "r4")
	r.store.fence("m3", "live")
	r.store.fence("m4", "gone")
	r.k.cycle()
	g1, g2 := mustHold(t, r, "m1"), mustHold(t, r, "m2")
	if g1.Token == g2.Token {
		t.Errorf("two runs share token %d", g1.Token)
	}
	for _, id := range []string{"m3", "m4"} {
		if _, ok := r.held(id); ok {
			t.Errorf("fenced %s has a gate", id)
		}
		if err := r.why(id); !errors.Is(err, links.ErrFenced) {
			t.Errorf("%s: %v", id, err)
		}
	}
	// Each address is dropped before its gate opens.
	calls := r.guard.takeCalls()
	for _, g := range []links.Gate{g1, g2} {
		drop := slices.Index(calls, "drop "+g.Addr.String())
		open := slices.Index(calls, fmt.Sprintf("open %s %d", g.Addr, g.Token))
		if drop < 0 || open < drop {
			t.Errorf("calls %q: no drop of %s before its open", calls, g.Addr)
		}
	}
	// The next cycle extends both in one batch and opens nothing.
	r.k.cycle()
	calls = r.guard.takeCalls()
	want := fmt.Sprintf("extend %s/%d %s/%d", g1.Addr, g1.Token, g2.Addr, g2.Token)
	if !slices.Equal(calls, []string{want}) {
		t.Errorf("second cycle: %q, want %q", calls, want)
	}
}

func TestKeeperRetiresOnFence(t *testing.T) {
	r := stepped(t)
	r.runs.set("m1", "r1")
	r.k.cycle()
	g := mustHold(t, r, "m1")
	r.store.fence("m1", "live")
	r.k.cycle()
	if _, ok := r.held("m1"); ok || !isClosed(r.k.Retired(g)) {
		t.Fatal("the fenced machine's gate was not retired")
	}
	if !r.logged("fenced") {
		t.Error("the retirement was not logged")
	}
	// Retired, not dropped: it expires on its own.
	if !r.guard.open()[g] {
		t.Error("the retired gate was dropped")
	}
	r.store.fence("m1", "")
	r.k.cycle()
	if g2 := mustHold(t, r, "m1"); g2.Token <= g.Token {
		t.Errorf("reopened with token %d after %d", g2.Token, g.Token)
	}
}

func TestKeeperRetiresOnRunEnd(t *testing.T) {
	r := stepped(t)
	r.runs.set("m1", "r1")
	r.k.cycle()
	old := mustHold(t, r, "m1")
	// The slot goes to a new run of the same machine before the old gate has expired.
	r.runs.set("m1", "r2")
	r.guard.takeCalls()
	r.k.cycle()
	if !isClosed(r.k.Retired(old)) {
		t.Fatal("the ended run's gate was not retired")
	}
	if r.logged("retired") {
		t.Error("a run ending was logged")
	}
	g := mustHold(t, r, "m1")
	if g.Token <= old.Token || r.guard.open()[old] {
		t.Errorf("new gate %v, old gate %v still open %t", g, old, r.guard.open()[old])
	}
	r.runs.set("m1", "")
	r.k.cycle()
	if _, ok := r.held("m1"); ok || !isClosed(r.k.Retired(g)) {
		t.Error("the gate of a machine with no run survived")
	}
}

func TestKeeperRechecksAfterOpening(t *testing.T) {
	t.Run("run ended", func(t *testing.T) {
		r := stepped(t)
		r.runs.set("m1", "r1")
		r.guard.onOpen = func(links.Gate) { r.runs.set("m1", "r2") }
		r.k.cycle()
		if _, ok := r.held("m1"); ok || len(r.guard.open()) != 0 {
			t.Fatalf("a gate opened for a run that ended while it opened: %v", r.guard.open())
		}
		if err := r.why("m1"); !errors.Is(err, links.ErrRunEnded) {
			t.Errorf("why: %v", err)
		}
	})
	t.Run("fenced", func(t *testing.T) {
		r := stepped(t)
		r.runs.set("m1", "r1")
		r.guard.onOpen = func(links.Gate) { r.store.fence("m1", "live") }
		r.k.cycle()
		if _, ok := r.held("m1"); ok || len(r.guard.open()) != 0 {
			t.Fatalf("a gate opened for a machine fenced while it opened: %v", r.guard.open())
		}
		if err := r.why("m1"); !errors.Is(err, links.ErrFenced) {
			t.Errorf("why: %v", err)
		}
	})
}

func TestKeeperRetiresVanishedGate(t *testing.T) {
	r := stepped(t)
	r.runs.set("m1", "r1")
	r.runs.set("m2", "r2")
	r.k.cycle()
	g1, g2 := mustHold(t, r, "m1"), mustHold(t, r, "m2")
	r.guard.vanish(g1.Addr)
	r.guard.takeCalls()
	r.k.cycle()
	if !isClosed(r.k.Retired(g1)) || isClosed(r.k.Retired(g2)) {
		t.Fatal("ENOENT did not retire exactly the vanished gate")
	}
	if got := mustHold(t, r, "m2"); got != g2 {
		t.Errorf("m2's gate changed from %v to %v", g2, got)
	}
	// The vanished gate is not re-added. Its run gets a new token through the open path.
	g := mustHold(t, r, "m1")
	if g.Token <= g2.Token {
		t.Errorf("m1 reopened with token %d", g.Token)
	}
	calls := r.guard.takeCalls()
	if !slices.Contains(calls, "installed") || slices.Contains(calls, "install") {
		t.Errorf("calls %q: want Installed checked and no reinstall", calls)
	}
}

func TestKeeperReinstallsWhenTableMissing(t *testing.T) {
	r := stepped(t)
	r.runs.set("m1", "r1")
	r.k.cycle()
	old := mustHold(t, r, "m1")
	r.guard.set(func(f *fakeGuard) {
		f.installed, f.gates, f.srcs = false, map[links.Gate]bool{}, map[netip.Addr]uint32{}
	})
	r.k.cycle()
	if !isClosed(r.k.Retired(old)) {
		t.Fatal("the gate survived the table going")
	}
	if !slices.Contains(r.guard.takeCalls(), "install") || !r.logged("reinstalled") {
		t.Fatal("no reinstall")
	}
	r.k.cycle()
	if g := mustHold(t, r, "m1"); g.Token <= old.Token {
		t.Errorf("reopened with token %d", g.Token)
	}
}

func TestKeeperDownUntilReinstall(t *testing.T) {
	r := stepped(t)
	r.runs.set("m1", "r1")
	r.k.cycle()
	old := mustHold(t, r, "m1")
	r.guard.set(func(f *fakeGuard) { f.extendErr, f.installErr = syscall.EPERM, syscall.EPERM })
	r.k.cycle()
	if !isClosed(r.k.Retired(old)) || !r.k.isDown() {
		t.Fatal("a failed refresh left the guard up")
	}
	if err := r.why("m1"); !errors.Is(err, links.ErrGuardDown) {
		t.Errorf("why: %v", err)
	}
	r.guard.takeCalls()
	r.k.cycle()
	if calls := r.guard.takeCalls(); len(calls) != 0 {
		t.Errorf("a cycle while down called %q", calls)
	}
	r.k.selfCheck()
	if !r.k.isDown() {
		t.Fatal("up after a failed reinstall")
	}
	r.guard.set(func(f *fakeGuard) { f.extendErr, f.installErr = nil, nil })
	r.k.selfCheck()
	if r.k.isDown() {
		t.Fatal("still down after a reinstall")
	}
	r.k.cycle()
	mustHold(t, r, "m1")
}

func TestKeeperSelfCheck(t *testing.T) {
	for name, tamper := range map[string]func(f *fakeGuard){
		"hand edit":     func(f *fakeGuard) { f.tampered = true },
		"check failing": func(f *fakeGuard) { f.installedErr = syscall.EIO },
	} {
		t.Run(name, func(t *testing.T) {
			r := stepped(t)
			r.runs.set("m1", "r1")
			r.k.cycle()
			old := mustHold(t, r, "m1")
			r.k.selfCheck()
			if isClosed(r.k.Retired(old)) {
				t.Fatal("a passing self-check retired a gate")
			}
			r.guard.set(tamper)
			r.k.selfCheck()
			if !isClosed(r.k.Retired(old)) || !r.logged("self-check") {
				t.Fatal("the self-check did not take the guard down")
			}
		})
	}
}

func TestKeeperTokensSpentFailsClosed(t *testing.T) {
	r := stepped(t)
	r.tokens.last = 1
	r.runs.set("m1", "r1")
	r.runs.set("m2", "r2")
	r.k.cycle()
	g := mustHold(t, r, "m1")
	if _, ok := r.held("m2"); ok {
		t.Fatal("a gate opened past the last token")
	}
	if err := r.why("m2"); !errors.Is(err, links.ErrTokensSpent) {
		t.Errorf("why: %v", err)
	}
	calls := r.tokens.calls
	r.runs.set("m3", "r3")
	r.guard.takeCalls()
	r.k.cycle()
	if got := mustHold(t, r, "m1"); got != g {
		t.Error("the open gate was not kept")
	}
	if !slices.Contains(r.guard.takeCalls(), fmt.Sprintf("extend %s/%d", g.Addr, g.Token)) {
		t.Error("the open gate was not extended")
	}
	if r.tokens.calls != calls {
		t.Error("asked for a token after running out")
	}
	if err := r.why("m3"); !errors.Is(err, links.ErrTokensSpent) {
		t.Errorf("why: %v", err)
	}
}

func TestKeeperStoreFailureShutsEveryGate(t *testing.T) {
	r := stepped(t)
	r.runs.set("m1", "r1")
	r.runs.set("m2", "r2")
	r.k.cycle()
	g1, g2 := mustHold(t, r, "m1"), mustHold(t, r, "m2")
	r.store.fail(links.ErrStateUnreadable)
	r.guard.takeCalls()
	r.k.cycle()
	if !isClosed(r.k.Retired(g1)) || !isClosed(r.k.Retired(g2)) {
		t.Fatal("an unreadable links.json left a gate up")
	}
	for _, c := range r.guard.takeCalls() {
		if strings.HasPrefix(c, "open") || c != "extend " && strings.HasPrefix(c, "extend") {
			t.Errorf("called %q with links.json unreadable", c)
		}
	}
	if err := r.why("m1"); !errors.Is(err, links.ErrFenced) {
		t.Errorf("why: %v", err)
	}
}

func TestKeeperLiveFailureShutsEveryGate(t *testing.T) {
	r := stepped(t)
	r.runs.set("m1", "r1")
	r.k.cycle()
	g := mustHold(t, r, "m1")
	r.runs.mu.Lock()
	r.runs.err = errors.New("cgroups unreadable")
	r.runs.mu.Unlock()
	r.k.cycle()
	if !isClosed(r.k.Retired(g)) {
		t.Fatal("unreadable runs left a gate up")
	}
	if err := r.why("m1"); err == nil || !strings.Contains(err.Error(), "cgroups unreadable") {
		t.Errorf("why: %v", err)
	}
}

func TestKeeperChecksTheSlotBack(t *testing.T) {
	r := stepped(t)
	r.runs.set("m1", "r1")
	r.res.at[slotAddr("m1")] = links.Slot{ID: "m9", Run: "r9"}
	r.k.cycle()
	if _, ok := r.held("m1"); ok {
		t.Fatal("a gate opened at an address that resolves back to another machine")
	}
	if calls := r.guard.takeCalls(); len(calls) != 1 {
		t.Errorf("calls %q: want only the empty extend", calls)
	}
	if r.why("m1") == nil {
		t.Error("no reason recorded")
	}
}

// running is a rig whose Keeper runs for real.
func running(t *testing.T, keep, selfCheck time.Duration) (*rig, context.CancelFunc, <-chan error) {
	t.Helper()
	r := newRig(t, keep, selfCheck)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	exited := make(chan struct{})
	go func() {
		done <- r.k.Run(ctx)
		close(exited)
	}()
	t.Cleanup(func() {
		cancel()
		<-exited
	})
	select {
	case <-r.k.Ready():
	case err := <-done:
		t.Fatalf("Run returned before Ready: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("not ready")
	}
	return r, cancel, done
}

func TestKeeperRunFailsOnFirstInstall(t *testing.T) {
	r := newRig(t, 0, 0)
	r.guard.installErr = syscall.EPERM
	if err := r.k.Run(context.Background()); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("Run: %v", err)
	}
	if isClosed(r.k.Ready()) {
		t.Error("Ready closed without a guard")
	}
	if _, err := r.k.Gate(context.Background(), "m1", "r1"); !errors.Is(err, links.ErrGuardDown) {
		t.Errorf("Gate: %v", err)
	}
	if err := r.k.Run(context.Background()); err == nil {
		t.Error("Run ran twice")
	}
}

func TestKeeperGateWaitsForACycle(t *testing.T) {
	r, _, _ := running(t, 900*time.Millisecond, time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// A run a wake has just resumed gets its gate from a nudged cycle, not the next tick.
	r.runs.set("m1", "r1")
	start := time.Now()
	g, err := r.k.Gate(ctx, "m1", "r1")
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Errorf("Gate took %v, so it waited for the tick", took)
	}
	if !r.guard.open()[g] || isClosed(r.k.Retired(g)) {
		t.Errorf("Gate returned %v, which is not open", g)
	}
	if again, err := r.k.Gate(ctx, "m1", "r1"); again != g || err != nil {
		t.Errorf("second Gate: %v, %v", again, err)
	}
	if _, err := r.k.Gate(ctx, "m1", "r0"); !errors.Is(err, links.ErrRunEnded) {
		t.Errorf("Gate for an old run: %v", err)
	}
	if _, err := r.k.Gate(ctx, "m2", "r2"); !errors.Is(err, links.ErrRunEnded) {
		t.Errorf("Gate for a machine with no run: %v", err)
	}
	r.runs.set("m3", "r3")
	r.store.fence("m3", "live")
	if _, err := r.k.Gate(ctx, "m3", "r3"); !errors.Is(err, links.ErrFenced) {
		t.Errorf("Gate for a fenced machine: %v", err)
	}
	r.runs.set("m4", "r4")
	short, stop := context.WithCancel(ctx)
	stop()
	if _, err := r.k.Gate(short, "m4", "r4"); !errors.Is(err, context.Canceled) {
		t.Errorf("Gate with its context done: %v", err)
	}
}

func TestKeeperStopRetiresEveryGate(t *testing.T) {
	r, cancel, done := running(t, 50*time.Millisecond, time.Hour)
	r.runs.set("m1", "r1")
	g, err := r.k.Gate(context.Background(), "m1", "r1")
	if err != nil {
		t.Fatal(err)
	}
	// It keeps the gate across cycles.
	time.Sleep(200 * time.Millisecond)
	if isClosed(r.k.Retired(g)) {
		t.Fatal("retired while the run is live")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !isClosed(r.k.Retired(g)) {
		t.Error("the gate outlived the Keeper")
	}
	if _, err := r.k.Gate(context.Background(), "m1", "r1"); !errors.Is(err, links.ErrGuardDown) {
		t.Errorf("Gate after Run: %v", err)
	}
}

func TestRetiredIsClosedForUnknownGate(t *testing.T) {
	r := stepped(t)
	if !isClosed(r.k.Retired(links.Gate{Addr: addrS, Token: 1})) {
		t.Error("Retired is open for a gate the Keeper never held")
	}
}
