package linkadmit_test

import (
	"context"
	"errors"
	"maps"
	"net/netip"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/linkadmit"
	"github.com/solev/hibernaut/internal/links"
	"github.com/solev/hibernaut/internal/room"
)

// store is links.json in memory.
type store struct {
	mu  sync.Mutex
	doc links.Doc
}

func newStore() *store {
	return &store{doc: links.Doc{Machines: map[string]*links.Machine{}}}
}

func (s *store) Read() (links.Doc, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc := s.doc
	doc.Machines = make(map[string]*links.Machine, len(s.doc.Machines))
	for id, m := range s.doc.Machines {
		c := *m
		c.Ports, c.PortGen = slices.Clone(m.Ports), maps.Clone(m.PortGen)
		doc.Machines[id] = &c
	}
	return doc, nil
}

func (s *store) Update(_ context.Context, f func(*links.Doc) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return f(&s.doc)
}

func (s *store) Fence(_ context.Context, id string, why string) (func(), error) {
	s.edit(id, func(m *links.Machine) { m.Fence = &links.Fence{Since: time.Now(), Why: why} })
	return func() { s.edit(id, func(m *links.Machine) { m.Fence = nil }) }, nil
}

func (s *store) edit(id string, f func(*links.Machine)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.doc.Machines[id]
	if m == nil {
		m = &links.Machine{PortGen: map[int]uint64{}}
		s.doc.Machines[id] = m
	}
	f(m)
}

// publish publishes port, or publishes it again, which raises its generation.
func (s *store) publish(id string, port int) {
	s.edit(id, func(m *links.Machine) {
		if !slices.Contains(m.Ports, port) {
			m.Ports = append(m.Ports, port)
			slices.Sort(m.Ports)
		}
		m.PortGen[port]++
	})
}

// clock is the activity clock in memory.
type clock struct {
	mu sync.Mutex
	at map[string]time.Time
}

func (c *clock) Active(id string) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at[id]
}

func (c *clock) Touch(id string, t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t.After(c.at[id]) {
		c.at[id] = t
	}
}

// runs says a running machine's run is its StartedAt, which is what changes when E2B starts a
// new one. A pinned machine keeps its run whatever the engine says, like a cgroup that lingers.
type runs struct {
	e      *fake.Engine
	mu     sync.Mutex
	pinned map[string]links.Run
	ended  map[string]chan struct{}
}

func (r *runs) Current(id string) (links.Run, error) {
	r.mu.Lock()
	run, ok := r.pinned[id]
	r.mu.Unlock()
	if ok {
		return run, nil
	}
	em, err := r.e.Get(context.Background(), id)
	if err != nil || em.State != engine.Running {
		return "", links.ErrNotRunning
	}
	return links.Run(strconv.FormatInt(em.StartedAt.UnixNano(), 10)), nil
}

func (r *runs) Watch(id string, run links.Run) (<-chan struct{}, error) {
	return r.watch(id, run), nil
}

func (r *runs) watch(id string, run links.Run) chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := id + "/" + string(run)
	if r.ended[k] == nil {
		r.ended[k] = make(chan struct{})
	}
	return r.ended[k]
}

func (r *runs) Live() (map[string]links.Run, error) { return nil, errors.New("unused") }

func (r *runs) pin(id string, run links.Run) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pinned[id] = run
}

// keeper hands out one gate per run.
type keeper struct {
	mu    sync.Mutex
	gates map[string]links.Gate
}

func (k *keeper) Gate(_ context.Context, id string, run links.Run) (links.Gate, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	key := id + "/" + string(run)
	g, ok := k.gates[key]
	if !ok {
		n := uint32(len(k.gates) + 1)
		g = links.Gate{Addr: netip.AddrFrom4([4]byte{10, 11, 0, byte(n)}), Token: n}
		k.gates[key] = g
	}
	return g, nil
}

func (k *keeper) gate(id string, run links.Run) links.Gate {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.gates[id+"/"+string(run)]
}

func (k *keeper) Ready() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func (k *keeper) Retired(links.Gate) <-chan struct{} { return make(chan struct{}) }

// closer is a listener that answers n, or err, and records what it was asked. A blocking one
// does not answer AbortMachine until its context is done.
type closer struct {
	mu       sync.Mutex
	n        int
	err      error
	block    bool
	stats    links.Stats
	machines []string
	ports    map[string][]int
}

func (c *closer) AbortMachine(ctx context.Context, id string, _ string) (int, error) {
	c.mu.Lock()
	c.machines = append(c.machines, id)
	block, n, err := c.block, c.n, c.err
	c.mu.Unlock()
	if block {
		<-ctx.Done()
		return 0, ctx.Err()
	}
	return n, err
}

func (c *closer) AbortPorts(_ context.Context, id string, ports []int, _ string) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ports[id] = append(c.ports[id], ports...)
	return c.n, c.err
}

func (c *closer) Stat() links.Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stats
}

// guard is the Resolver and the Dropper of the close fallback.
type guard struct {
	mu      sync.Mutex
	addrs   map[string]netip.Addr
	dropped []netip.Addr
	err     error
}

func (g *guard) Addr(id string) (netip.Addr, links.Run, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	a, ok := g.addrs[id]
	if !ok {
		return netip.Addr{}, "", links.ErrNotRunning
	}
	return a, "", nil
}

func (g *guard) At(netip.Addr) (links.Slot, error) { return links.Slot{}, errors.New("unused") }

func (g *guard) Drop(addr netip.Addr) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.err != nil {
		return g.err
	}
	g.dropped = append(g.dropped, addr)
	return nil
}

// hooked is the fake engine, counting resumes and running hook before each.
type hooked struct {
	*fake.Engine
	mu      sync.Mutex
	resumes int
	hook    func(ctx context.Context, id string) error
}

func (e *hooked) Resume(ctx context.Context, id string, timeout time.Duration) (engine.Machine, error) {
	e.mu.Lock()
	e.resumes++
	hook := e.hook
	e.mu.Unlock()
	if hook != nil {
		if err := hook(ctx, id); err != nil {
			return engine.Machine{}, err
		}
	}
	return e.Engine.Resume(ctx, id, timeout)
}

func (e *hooked) setHook(hook func(ctx context.Context, id string) error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.hook = hook
}

func (e *hooked) resumed() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.resumes
}

// holdResumes holds every resume until the returned channel is closed.
func (e *hooked) holdResumes() chan struct{} {
	release := make(chan struct{})
	e.setHook(func(ctx context.Context, _ string) error {
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	})
	return release
}

type env struct {
	e      *hooked
	store  *store
	clock  *clock
	runs   *runs
	keeper *keeper
	closer *closer
	guard  *guard
	// free is what Room reports.
	free atomic.Int64
	// needMu guards needs: the room Room reports each target's wake takes, 1 unless a test
	// sets it.
	needMu sync.Mutex
	needs  map[string]int
	a      *linkadmit.Admitter
}

func newEnv(t *testing.T, configure ...func(*linkadmit.Config)) *env {
	t.Helper()
	f := fake.New()
	v := &env{
		e:      &hooked{Engine: f},
		store:  newStore(),
		clock:  &clock{at: map[string]time.Time{}},
		runs:   &runs{e: f, pinned: map[string]links.Run{}, ended: map[string]chan struct{}{}},
		keeper: &keeper{gates: map[string]links.Gate{}},
		closer: &closer{ports: map[string][]int{}},
		guard:  &guard{addrs: map[string]netip.Addr{}},
		needs:  map[string]int{},
	}
	v.free.Store(8)
	cfg := linkadmit.Config{
		Engine: v.e, Runs: v.runs, Keeper: v.keeper, Dropper: v.guard, Closer: v.closer,
		Resolver: v.guard, Store: v.store, Clock: v.clock,
		Room: func(target string) (int, int, error) { return int(v.free.Load()), v.need(target), nil },
	}
	for _, c := range configure {
		c(&cfg)
	}
	a, err := linkadmit.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	linkadmit.SetReadRoom(a, func() (room.Host, error) {
		return room.Host{PageSize: 2 << 20, Pages: 8192, FreePages: 8000, DiskFree: 80 << 30}, nil
	})
	v.a = a
	return v
}

// machine adds a machine hibd made, which pauses after ten minutes idle.
func (v *env) machine(id string, state engine.State) {
	now := time.Now()
	v.e.Add(engine.Machine{
		ID: id, State: state, StartedAt: now.Add(-time.Hour), EndAt: now.Add(10 * time.Minute), MemoryMB: 1024,
		Metadata: map[string]string{"hib.name": id, "hib.idle": "10m0s"},
	})
}

// setNeed makes Room report that waking target takes the room of n default machines.
func (v *env) setNeed(target string, n int) {
	v.needMu.Lock()
	defer v.needMu.Unlock()
	v.needs[target] = n
}

func (v *env) need(target string) int {
	v.needMu.Lock()
	defer v.needMu.Unlock()
	if n, ok := v.needs[target]; ok {
		return n
	}
	return 1
}

// source adds a running machine that has just been active.
func (v *env) source(id string) {
	v.machine(id, engine.Running)
	v.clock.Touch(id, time.Now())
}

// target adds a machine publishing ports.
func (v *env) target(id string, state engine.State, ports ...int) {
	v.machine(id, state)
	for _, p := range ports {
		v.store.publish(id, p)
	}
}

var sourceGate = links.Gate{Addr: netip.MustParseAddr("10.11.0.200"), Token: 200}

// admit admits a link the way the listener does, from the source's current run.
func (v *env) admit(source, target string, port int) (links.Admission, error) {
	run, err := v.runs.Current(source)
	if err != nil {
		return links.Admission{}, err
	}
	return v.a.Admit(context.Background(), links.AdmitRequest{
		Source: source, SourceRun: run, SourceGate: sourceGate, Target: target, Port: port,
	})
}

type result struct {
	ad  links.Admission
	err error
}

func (v *env) admitLater(source, target string, port int) <-chan result {
	ch := make(chan result, 1)
	go func() {
		ad, err := v.admit(source, target, port)
		ch <- result{ad, err}
	}()
	return ch
}

func (v *env) endAt(t *testing.T, id string) time.Time {
	t.Helper()
	em, err := v.e.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return em.EndAt
}
