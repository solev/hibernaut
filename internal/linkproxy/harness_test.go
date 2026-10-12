package linkproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/audit"
	"github.com/solev/hibernaut/internal/links"
)

// The default world: src calls tgt's published 8080 and 5432 over 198.18.0.1.
var (
	srcGate = links.Gate{Addr: netip.MustParseAddr("10.11.0.1"), Token: 1}
	tgtGate = links.Gate{Addr: netip.MustParseAddr("10.11.0.2"), Token: 2}
	tgtAddr = netip.MustParseAddr("198.18.0.1")
	srcAddr = netip.MustParseAddr("198.18.0.2")
)

type harness struct {
	t         *testing.T
	p         *Proxy
	world     *world
	admitter  *fakeAdmitter
	ingress   *fakeIngress
	dialer    *fakeDialer
	doc       *fakeDoc
	clock     *fakeClock
	log       *recorder
	lines     *lines
	quiescing atomic.Bool
	backends  map[netip.AddrPort]*backend

	addr   string
	cancel context.CancelFunc
	served chan error
}

// newHarness builds a listener over the default world without serving it.
func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		t:        t,
		world:    newWorld(),
		ingress:  &fakeIngress{peers: map[int]identity{}},
		clock:    &fakeClock{now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
		log:      &recorder{},
		lines:    &lines{},
		backends: map[netip.AddrPort]*backend{},
	}
	h.world.start("src", "r1", srcGate)
	h.world.start("tgt", "t1", tgtGate)
	h.doc = &fakeDoc{doc: links.Doc{V: links.Version, Machines: map[string]*links.Machine{
		"src": {Addr: srcAddr},
		"tgt": {Addr: tgtAddr, Ports: []int{8080, 5432}, PortGen: map[int]uint64{8080: 1, 5432: 1}},
	}}}
	h.admitter = newFakeAdmitter(h.world, h.doc)
	h.dialer = &fakeDialer{routes: map[netip.AddrPort]string{}}
	h.route(tgtGate, 8080, echo)
	h.route(tgtGate, 5432, echo)

	p, err := New(Config{
		Addr:      "127.0.0.1:0",
		Admitter:  h.admitter,
		Runs:      h.world,
		Keeper:    h.world,
		Ingress:   h.ingress,
		Dialer:    h.dialer,
		Doc:       h.doc.Doc,
		Quiescing: h.quiescing.Load,
		Now:       h.clock.Now,
		Logf:      h.lines.logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	p.write = h.log.write
	p.acceptPause = 5 * time.Millisecond
	p.admitTimeout = time.Second
	p.dialTimeout = 2 * time.Second
	p.drainTimeout = time.Second
	h.p = p
	return h
}

// start builds a listener over the default world and serves it until the test ends.
func start(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.serve()
	return h
}

func (h *harness) serve() {
	h.t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		h.t.Fatal(err)
	}
	h.addr = l.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	h.served = make(chan error, 1)
	go func() { h.served <- h.p.Serve(ctx, l) }()
	h.t.Cleanup(func() { h.stop() })
}

// stop shuts the listener down and returns what Serve returned.
func (h *harness) stop() error {
	h.t.Helper()
	h.cancel()
	select {
	case err := <-h.served:
		h.served <- err
		return err
	case <-time.After(5 * time.Second):
		h.t.Fatal("Serve did not return after its context was cancelled")
		return nil
	}
}

// route sends dials of gate's port to a new backend running handler.
func (h *harness) route(gate links.Gate, port int, handler func(*backend, net.Conn)) *backend {
	h.t.Helper()
	b := newBackend(h.t, handler)
	key := netip.AddrPortFrom(gate.Addr, uint16(port))
	h.backends[key] = b
	h.dialer.mu.Lock()
	h.dialer.routes[key] = b.addr
	h.dialer.mu.Unlock()
	return b
}

// peer is src calling tgt's 8080 from its own slot with its own run's token.
func (h *harness) peer() Peer {
	return Peer{Source: "src", SourceAddr: srcGate.Addr, Dst: netip.AddrPortFrom(tgtAddr, 8080), Token: srcGate.Token}
}

// patience bounds every read and write on a test's connection, so a link that never closes
// fails its test instead of hanging the package.
const patience = 20 * time.Second

// connect opens a connection to the listener that Identify will say is peer.
func (h *harness) connect(peer Peer) net.Conn {
	h.t.Helper()
	return h.connectAs(peer, nil)
}

func (h *harness) connectAs(peer Peer, err error) net.Conn {
	h.t.Helper()
	c, dialErr := net.Dial("tcp", h.addr)
	if dialErr != nil {
		h.t.Fatal(dialErr)
	}
	h.t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(patience))
	h.ingress.add(c.LocalAddr().(*net.TCPAddr).Port, identity{peer: peer, err: err})
	return c
}

// link opens a connection as peer and waits for it to carry an echo, so it is open.
func (h *harness) link(peer Peer) net.Conn {
	h.t.Helper()
	c := h.connect(peer)
	roundTrip(h.t, c, "ping")
	return c
}

func roundTrip(t *testing.T, c net.Conn, msg string) {
	t.Helper()
	if _, err := io.WriteString(c, msg); err != nil {
		t.Fatalf("write %q: %v", msg, err)
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatalf("read back %q: %v", msg, err)
	}
	if string(got) != msg {
		t.Fatalf("read back %q, want %q", got, msg)
	}
	_ = c.SetReadDeadline(time.Now().Add(patience))
}

// expectNothing reads c until it is closed and fails if any byte arrives first.
func expectNothing(t *testing.T, c net.Conn) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := c.Read(make([]byte, 64))
	var ne net.Error
	if n != 0 || err == nil || errors.As(err, &ne) && ne.Timeout() {
		t.Fatalf("read %d bytes and %v, want the connection closed with nothing written", n, err)
	}
}

// expectReset reads c and wants a reset, not an orderly close, and no byte before it: a link
// that aborts sends nothing more.
func expectReset(t *testing.T, c net.Conn) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := c.Read(make([]byte, 64))
	if n != 0 || !isReset(err) {
		t.Fatalf("read %d bytes and %v, want a reset with nothing before it", n, err)
	}
}

// eventually polls cond for up to 5 s.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// world is the fake Runs and Keeper: which run each machine has, its gate and the channels
// that end them.
type world struct {
	mu      sync.Mutex
	runs    map[string]links.Run
	gates   map[string]links.Gate
	watches map[watchKey]chan struct{}
	retired map[links.Gate]chan struct{}
	gateErr error
	ready   chan struct{}
}

type watchKey struct {
	id  string
	run links.Run
}

func newWorld() *world {
	w := &world{
		runs:    map[string]links.Run{},
		gates:   map[string]links.Gate{},
		watches: map[watchKey]chan struct{}{},
		retired: map[links.Gate]chan struct{}{},
		ready:   make(chan struct{}),
	}
	close(w.ready)
	return w
}

var closedChan = func() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}()

func (w *world) start(id string, run links.Run, gate links.Gate) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.runs[id] = run
	w.gates[id] = gate
	w.retired[gate] = make(chan struct{})
}

// stop ends id's run, closing its Watch channel when watch is set and retiring its gate when
// retire is set.
func (w *world) stop(id string, watch, retire bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	run, gate := w.runs[id], w.gates[id]
	delete(w.runs, id)
	delete(w.gates, id)
	if ch, ok := w.watches[watchKey{id, run}]; watch && ok {
		close(ch)
		delete(w.watches, watchKey{id, run})
	}
	if retire {
		w.retireLocked(gate)
	}
}

func (w *world) retire(gate links.Gate) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.retireLocked(gate)
}

func (w *world) retireAll() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for gate := range w.retired {
		w.retireLocked(gate)
	}
}

func (w *world) retireLocked(gate links.Gate) {
	if ch, ok := w.retired[gate]; ok {
		close(ch)
		delete(w.retired, gate)
	}
}

func (w *world) gate(id string) links.Gate {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.gates[id]
}

func (w *world) setGateErr(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.gateErr = err
}

func (w *world) Current(id string) (links.Run, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	run, ok := w.runs[id]
	if !ok {
		return "", links.ErrNotRunning
	}
	return run, nil
}

func (w *world) Watch(id string, run links.Run) (<-chan struct{}, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.runs[id] != run {
		return closedChan, nil
	}
	key := watchKey{id, run}
	if w.watches[key] == nil {
		w.watches[key] = make(chan struct{})
	}
	return w.watches[key], nil
}

func (w *world) Live() (map[string]links.Run, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return maps.Clone(w.runs), nil
}

func (w *world) Ready() <-chan struct{} { return w.ready }

func (w *world) Gate(ctx context.Context, id string, run links.Run) (links.Gate, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.gateErr != nil {
		return links.Gate{}, w.gateErr
	}
	if w.runs[id] != run {
		return links.Gate{}, links.ErrRunEnded
	}
	return w.gates[id], nil
}

func (w *world) Retired(g links.Gate) <-chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	if ch, ok := w.retired[g]; ok {
		return ch
	}
	return closedChan
}

// fakeAdmitter grants every admission it can against the world, with hooks for the races.
type fakeAdmitter struct {
	w   *world
	doc *fakeDoc

	mu sync.Mutex
	// err refuses every admission.
	err error
	// before runs before an admission is decided, and refuses it by returning an error.
	before func(ctx context.Context, req links.AdmitRequest) error
	// after runs once an admission is granted, before Admit returns it.
	after     func(links.Admission)
	perSource int
	woke      bool
	// stale are the sources whose renewals are refused, as a source with no recent activity
	// of its own is.
	stale         map[string]bool
	staleRenewals int
	held          map[string]int
	live          map[context.Context]context.CancelCauseFunc
	granted       int
	released      int
	renewals      []int64
}

func newFakeAdmitter(w *world, doc *fakeDoc) *fakeAdmitter {
	return &fakeAdmitter{w: w, doc: doc, held: map[string]int{}, live: map[context.Context]context.CancelCauseFunc{}}
}

func (a *fakeAdmitter) Admit(ctx context.Context, req links.AdmitRequest) (links.Admission, error) {
	a.mu.Lock()
	before := a.before
	a.mu.Unlock()
	if before != nil {
		if err := before(ctx, req); err != nil {
			return links.Admission{}, err
		}
	}

	a.mu.Lock()
	if a.err != nil {
		a.mu.Unlock()
		return links.Admission{}, a.err
	}
	if a.perSource > 0 && a.held[req.Source] >= a.perSource {
		a.mu.Unlock()
		return links.Admission{}, links.ErrTooManyLinks
	}
	run, err := a.w.Current(req.Target)
	if err != nil {
		a.mu.Unlock()
		return links.Admission{}, err
	}
	gen, _ := a.doc.gen(req.Target, req.Port)
	actx, cancel := context.WithCancelCause(context.Background())
	adm := links.Admission{
		Source:     req.Source,
		Target:     req.Target,
		SourceRun:  req.SourceRun,
		TargetRun:  run,
		SourceGate: req.SourceGate,
		TargetGate: a.w.gate(req.Target),
		Port:       req.Port,
		PortGen:    gen,
		Woke:       a.woke,
		Ctx:        actx,
	}
	a.held[req.Source]++
	a.live[actx] = cancel
	a.granted++
	after := a.after
	a.mu.Unlock()
	if after != nil {
		after(adm)
	}
	return adm, nil
}

func (a *fakeAdmitter) Renew(adm links.Admission, payload int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stale[adm.Source] {
		a.staleRenewals++
		return
	}
	a.renewals = append(a.renewals, payload)
}

func (a *fakeAdmitter) Release(adm links.Admission) {
	a.mu.Lock()
	defer a.mu.Unlock()
	cancel, ok := a.live[adm.Ctx]
	if !ok {
		return
	}
	delete(a.live, adm.Ctx)
	a.held[adm.Source]--
	a.released++
	cancel(nil)
}

// cancel cancels every admission still held, with cause.
func (a *fakeAdmitter) cancel(cause error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, cancel := range a.live {
		cancel(cause)
	}
}

func (a *fakeAdmitter) set(f func(a *fakeAdmitter)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	f(a)
}

func (a *fakeAdmitter) counts() (granted, released int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.granted, a.released
}

func (a *fakeAdmitter) renewed() []int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.renewals)
}

// fakeIngress identifies a connection by the port it came from.
type fakeIngress struct {
	mu    sync.Mutex
	peers map[int]identity
	calls int
}

type identity struct {
	peer Peer
	err  error
}

func (in *fakeIngress) add(port int, id identity) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.peers[port] = id
}

func (in *fakeIngress) count() int {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.calls
}

func (in *fakeIngress) Identify(c net.Conn) (Peer, error) {
	port := c.RemoteAddr().(*net.TCPAddr).Port
	in.mu.Lock()
	in.calls++
	in.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for {
		in.mu.Lock()
		id, ok := in.peers[port]
		in.mu.Unlock()
		if ok {
			return id.peer, id.err
		}
		if time.Now().After(deadline) {
			return Peer{}, fmt.Errorf("fake ingress: no peer for port %d", port)
		}
		time.Sleep(time.Millisecond)
	}
}

// fakeDialer dials the backend routed to the target gate's port.
type fakeDialer struct {
	mu     sync.Mutex
	routes map[netip.AddrPort]string
	err    error
	// before runs before each dial.
	before func(links.Admission)
	dials  int
}

func (d *fakeDialer) Dial(ctx context.Context, a links.Admission) (net.Conn, error) {
	d.mu.Lock()
	d.dials++
	before, err := d.before, d.err
	addr, ok := d.routes[netip.AddrPortFrom(a.TargetGate.Addr, uint16(a.Port))]
	d.mu.Unlock()
	if before != nil {
		before(a)
	}
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("fake dialer: nothing at %s:%d", a.TargetGate.Addr, a.Port)
	}
	var nd net.Dialer
	return nd.DialContext(ctx, "tcp", addr)
}

func (d *fakeDialer) set(f func(d *fakeDialer)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f(d)
}

func (d *fakeDialer) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dials
}

// fakeDoc is links.json. Every read is a copy, as a reload would be.
type fakeDoc struct {
	mu  sync.Mutex
	doc links.Doc
	err error
}

func (d *fakeDoc) Doc() (*links.Doc, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err != nil {
		return nil, d.err
	}
	doc := d.doc
	doc.Machines = map[string]*links.Machine{}
	for id, m := range d.doc.Machines {
		c := *m
		c.Ports = slices.Clone(m.Ports)
		c.PortGen = maps.Clone(m.PortGen)
		doc.Machines[id] = &c
	}
	return &doc, nil
}

func (d *fakeDoc) update(f func(*links.Doc)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f(&d.doc)
}

func (d *fakeDoc) setErr(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.err = err
}

func (d *fakeDoc) gen(id string, port int) (uint64, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.doc.Machines[id].Gen(port)
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// recorder is the link log.
type recorder struct {
	mu      sync.Mutex
	entries []audit.LinkEntry
}

func (r *recorder) write(e audit.LinkEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, e)
}

func (r *recorder) all() []audit.LinkEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.entries)
}

// wait returns the first entry with event, waiting up to 5 s for it.
func (r *recorder) wait(t *testing.T, event audit.LinkEvent) audit.LinkEntry {
	t.Helper()
	return r.waitN(t, event, 1)[0]
}

// waitN returns the first n entries with event, waiting up to 5 s for them.
func (r *recorder) waitN(t *testing.T, event audit.LinkEvent, n int) []audit.LinkEntry {
	t.Helper()
	var found []audit.LinkEntry
	eventually(t, fmt.Sprintf("%d %q lines", n, event), func() bool {
		found = found[:0]
		for _, e := range r.all() {
			if e.Event == event {
				found = append(found, e)
			}
		}
		return len(found) >= n
	})
	return found[:n]
}

// lines is Logf.
type lines struct {
	mu    sync.Mutex
	lines []string
}

func (l *lines) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *lines) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.lines)
}

// backend is a target machine's server.
type backend struct {
	addr     string
	accepted atomic.Int64
	received atomic.Int64

	mu    sync.Mutex
	conns []net.Conn
}

func newBackend(t *testing.T, handler func(*backend, net.Conn)) *backend {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b := &backend{addr: l.Addr().String()}
	var wg sync.WaitGroup
	t.Cleanup(func() {
		_ = l.Close()
		b.mu.Lock()
		for _, c := range b.conns {
			_ = c.Close()
		}
		b.mu.Unlock()
		wg.Wait()
	})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			b.accepted.Add(1)
			b.mu.Lock()
			b.conns = append(b.conns, c)
			b.mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer c.Close()
				handler(b, countingConn{Conn: c, n: &b.received})
			}()
		}
	}()
	return b
}

// countingConn counts what the backend reads.
type countingConn struct {
	net.Conn
	n *atomic.Int64
}

func (c countingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.n.Add(int64(n))
	return n, err
}

func (c countingConn) CloseWrite() error {
	return c.Conn.(*net.TCPConn).CloseWrite()
}

// echo sends back everything it reads and closes its write half at EOF.
func echo(_ *backend, c net.Conn) {
	_, _ = io.Copy(c, c)
	_ = c.(interface{ CloseWrite() error }).CloseWrite()
	_, _ = io.Copy(io.Discard, c)
}

// greet is a server that speaks first, with its name, and then echoes.
func greet(name string) func(*backend, net.Conn) {
	return func(b *backend, c net.Conn) {
		if _, err := io.WriteString(c, name); err != nil {
			return
		}
		echo(b, c)
	}
}
