package linkproxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/audit"
	"github.com/solev/hibernaut/internal/links"
	"github.com/solev/hibernaut/internal/slots"
)

func TestEveryRefusalClosesWithoutAByte(t *testing.T) {
	type test struct {
		name  string
		setup func(h *harness)
		peer  func(p *Peer)
		err   error
		want  error
		// dialled is whether the refusal comes after the target leg was dialled.
		dialled bool
	}
	movedGate := links.Gate{Addr: netip.MustParseAddr("10.11.0.5"), Token: 3}
	tests := []test{
		{name: "quiescing", setup: func(h *harness) { h.quiescing.Store(true) }, want: links.ErrQuiescing},
		{name: "no link mark", peer: func(p *Peer) { p.Token = 0 }, err: links.ErrUnmarked, want: links.ErrUnmarked},
		{name: "token zero", peer: func(p *Peer) { p.Token = 0 }, want: links.ErrUnmarked},
		{
			name: "outside the virtual range",
			peer: func(p *Peer) { p.Dst = netip.MustParseAddrPort("10.0.0.1:8080") },
			want: links.ErrUnknownAddress,
		},
		{
			name: "address nobody holds",
			peer: func(p *Peer) { p.Dst = netip.MustParseAddrPort("198.18.0.9:8080") },
			want: links.ErrUnknownAddress,
		},
		{
			name:  "unreadable state",
			setup: func(h *harness) { h.doc.setErr(errors.New("unknown version 9")) },
			want:  links.ErrStateUnreadable,
		},
		{
			name: "self link",
			peer: func(p *Peer) { p.Source, p.SourceAddr, p.Token = "tgt", tgtGate.Addr, tgtGate.Token },
			want: links.ErrSelfLink,
		},
		{
			name: "reserved port",
			setup: func(h *harness) {
				h.doc.update(func(d *links.Doc) {
					d.Machines["tgt"].Ports = append(d.Machines["tgt"].Ports, 8081)
					d.Machines["tgt"].PortGen[8081] = 1
				})
			},
			peer: func(p *Peer) { p.Dst = netip.AddrPortFrom(tgtAddr, 8081) },
			want: links.ErrReservedPort,
		},
		{
			name: "unpublished port",
			peer: func(p *Peer) { p.Dst = netip.AddrPortFrom(tgtAddr, 9090) },
			want: links.ErrNotPublished,
		},
		{name: "token of an earlier run", peer: func(p *Peer) { p.Token = 7 }, want: links.ErrWrongRun},
		{
			name: "another slot's address",
			peer: func(p *Peer) { p.SourceAddr = netip.MustParseAddr("10.11.0.9") },
			want: links.ErrWrongRun,
		},
		{name: "source not running", setup: func(h *harness) { h.world.stop("src", true, true) }, want: links.ErrWrongRun},
		{name: "guard down", setup: func(h *harness) { h.world.setGateErr(links.ErrGuardDown) }, want: links.ErrGuardDown},
		{name: "paused by hand", setup: admitErr(links.ErrPausedByHand), want: links.ErrPausedByHand},
		{name: "source idle", setup: admitErr(links.ErrSourceIdle), want: links.ErrSourceIdle},
		{name: "too many links", setup: admitErr(links.ErrTooManyLinks), want: links.ErrTooManyLinks},
		{name: "admission fails another way", setup: admitErr(errors.New("wake failed")), want: links.ErrNotRunning},
		{
			name:    "nothing listening on the target",
			setup:   func(h *harness) { h.dialer.set(func(d *fakeDialer) { d.err = syscall.ECONNREFUSED }) },
			want:    links.ErrDialFailed,
			dialled: true,
		},
		{
			name: "target gate retired before the dial",
			setup: func(h *harness) {
				h.dialer.set(func(d *fakeDialer) { d.before = func(links.Admission) { h.world.retire(tgtGate) } })
			},
			want:    links.ErrRunEnded,
			dialled: true,
		},
		{
			name: "source gate retired before the dial",
			setup: func(h *harness) {
				h.dialer.set(func(d *fakeDialer) { d.before = func(links.Admission) { h.world.retire(srcGate) } })
			},
			want:    links.ErrRunEnded,
			dialled: true,
		},
		{
			name: "port republished during the wake",
			setup: func(h *harness) {
				h.admitter.set(func(a *fakeAdmitter) {
					a.after = func(links.Admission) {
						h.doc.update(func(d *links.Doc) { d.Machines["tgt"].PortGen[8080]++ })
					}
				})
			},
			want:    links.ErrRevoked,
			dialled: true,
		},
		{
			name: "target resumed on another slot during the dial",
			setup: func(h *harness) {
				h.route(movedGate, 8080, echo)
				h.dialer.set(func(d *fakeDialer) {
					d.before = func(links.Admission) {
						h.world.stop("tgt", false, false)
						h.world.start("tgt", "t2", movedGate)
					}
				})
			},
			want:    links.ErrRunEnded,
			dialled: true,
		},
		{
			name: "admission cancelled before the first byte",
			setup: func(h *harness) {
				h.dialer.set(func(d *fakeDialer) { d.before = func(links.Admission) { h.admitter.cancel(links.ErrFenced) } })
			},
			want:    links.ErrFenced,
			dialled: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := start(t)
			if tt.setup != nil {
				tt.setup(h)
			}
			peer := h.peer()
			if tt.peer != nil {
				tt.peer(&peer)
			}
			c := h.connectAs(peer, tt.err)
			_, _ = io.WriteString(c, "GET / HTTP/1.0\r\n\r\n")
			expectNothing(t, c)

			e := h.log.wait(t, "refused")
			if e.Reason != tt.want.Error() {
				t.Errorf("refused with %q, want %q", e.Reason, tt.want)
			}
			if e.Count != 1 {
				t.Errorf("Count = %d, want 1", e.Count)
			}
			if tt.name == "quiescing" {
				if e.From != "?" || h.ingress.count() != 0 {
					t.Errorf("quiescing refusal from %q after %d identifies, want ? before any", e.From, h.ingress.count())
				}
			} else if e.FromID != peer.Source || e.Port != int(peer.Dst.Port()) {
				t.Errorf("refusal is from %q to port %d, want %q to %d", e.FromID, e.Port, peer.Source, peer.Dst.Port())
			}
			if dialled := h.dialer.count() > 0; dialled != tt.dialled {
				t.Errorf("target dialled = %v, want %v", dialled, tt.dialled)
			}
			for addr, b := range h.backends {
				if n := b.received.Load(); n != 0 {
					t.Errorf("backend %s received %d bytes, want 0", addr, n)
				}
			}
			if granted, released := h.admitter.counts(); granted != released {
				t.Errorf("granted %d admissions and released %d", granted, released)
			}
			if got := h.log.all(); len(got) != 1 {
				t.Errorf("log = %+v, want only the refusal", got)
			}
		})
	}
}

func admitErr(err error) func(h *harness) {
	return func(h *harness) { h.admitter.set(func(a *fakeAdmitter) { a.err = err }) }
}

func TestConnectionNotFromAMachineIsClosedSilently(t *testing.T) {
	h := start(t)
	c := h.connectAs(Peer{}, errors.New("hook: local address 10.0.0.4 belongs to interface eth0, not veth-*"))
	expectNothing(t, c)
	eventually(t, "the handler to return", func() bool { return h.p.active.Load() == 0 })
	if got := h.log.all(); len(got) != 0 {
		t.Errorf("log = %+v, want nothing", got)
	}
}

func TestLinkCarriesBytesAndLogsOpenAndClose(t *testing.T) {
	h := start(t)
	h.admitter.set(func(a *fakeAdmitter) { a.woke = true })
	c := h.link(h.peer())
	roundTrip(t, c, "hello")

	open := h.log.wait(t, "open")
	want := audit.LinkEntry{Time: h.clock.Now(), Event: "open", From: "src", FromID: "src", To: "tgt", ToID: "tgt", Port: 8080, Woke: true}
	if open != want {
		t.Errorf("open = %+v, want %+v", open, want)
	}
	if got := h.p.Stat(); !reflect.DeepEqual(got, links.Stats{
		Ports:    map[links.Open]int{{Target: "tgt", Port: 8080}: 1},
		Machines: map[string]int{"src": 1, "tgt": 1},
	}) {
		t.Errorf("Stat = %+v", got)
	}

	h.clock.advance(3 * time.Second)
	_ = c.(*net.TCPConn).CloseWrite()
	if rest, err := io.ReadAll(c); err != nil || len(rest) != 0 {
		t.Fatalf("after the half-close read %q, %v; want a clean EOF", rest, err)
	}
	closed := h.log.wait(t, "close")
	want = audit.LinkEntry{
		Time: h.clock.Now(), Event: "close", From: "src", FromID: "src", To: "tgt", ToID: "tgt", Port: 8080,
		BytesIn: 9, BytesOut: 9, SecondsOpen: 3,
	}
	if closed != want {
		t.Errorf("close = %+v, want %+v", closed, want)
	}
	if granted, released := h.admitter.counts(); granted != 1 || released != 1 {
		t.Errorf("granted %d and released %d, want 1 and 1", granted, released)
	}
	if got := h.p.Stat(); len(got.Ports) != 0 || len(got.Machines) != 0 {
		t.Errorf("Stat after close = %+v, want empty", got)
	}
}

func TestHalfCloseFromTheSource(t *testing.T) {
	h := newHarness(t)
	h.route(tgtGate, 8080, func(b *backend, c net.Conn) {
		got, _ := io.ReadAll(c)
		_, _ = io.WriteString(c, "got "+string(got))
		_ = c.(interface{ CloseWrite() error }).CloseWrite()
	})
	h.serve()
	c := h.connect(h.peer())
	_, _ = io.WriteString(c, "request")
	_ = c.(*net.TCPConn).CloseWrite()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, err := io.ReadAll(c)
	if err != nil || string(got) != "got request" {
		t.Fatalf("read %q, %v; want the reply after our half-close and then EOF", got, err)
	}
	if e := h.log.wait(t, "close"); e.Reason != "" || e.BytesIn != 7 || e.BytesOut != 11 {
		t.Errorf("close = %+v, want no reason, 7 in and 11 out", e)
	}
}

func TestHalfCloseFromTheTarget(t *testing.T) {
	h := newHarness(t)
	h.route(tgtGate, 8080, func(b *backend, c net.Conn) {
		_, _ = io.WriteString(c, "banner")
		_ = c.(interface{ CloseWrite() error }).CloseWrite()
		got, _ := io.ReadAll(c)
		if string(got) != "late reply" {
			_, _ = io.WriteString(c, "unexpected")
		}
	})
	h.serve()
	c := h.connect(h.peer())
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, err := io.ReadAll(c)
	if err != nil || string(got) != "banner" {
		t.Fatalf("read %q, %v; want the banner and then EOF", got, err)
	}
	if _, err := io.WriteString(c, "late reply"); err != nil {
		t.Fatalf("write after the target's half-close: %v", err)
	}
	_ = c.(*net.TCPConn).CloseWrite()
	if e := h.log.wait(t, "close"); e.Reason != "" || e.BytesIn != 10 || e.BytesOut != 6 {
		t.Errorf("close = %+v, want no reason, 10 in and 6 out", e)
	}
}

func TestRenewOnlyOnPayload(t *testing.T) {
	h := start(t)
	c := h.connect(h.peer())
	h.log.wait(t, "open")
	h.clock.advance(time.Minute)
	if got := h.admitter.renewed(); len(got) != 0 {
		t.Fatalf("a silent link renewed %v", got)
	}

	roundTrip(t, c, "hello")
	eventually(t, "the first renewal", func() bool { return len(h.admitter.renewed()) == 1 })
	roundTrip(t, c, "again")
	h.clock.advance(links.LinkRenew - time.Second)
	roundTrip(t, c, "still")
	h.clock.advance(time.Hour)
	time.Sleep(20 * time.Millisecond)
	if got := h.admitter.renewed(); !reflect.DeepEqual(got, []int64{5}) {
		t.Fatalf("renewals = %v, want only the first, within LinkRenew and with no payload since", got)
	}

	roundTrip(t, c, "x")
	if got := h.admitter.renewed(); !reflect.DeepEqual(got, []int64{5, 26}) {
		t.Fatalf("renewals = %v, want a second carrying every byte since the first", got)
	}

	h.quiescing.Store(true)
	h.clock.advance(time.Hour)
	roundTrip(t, c, "y")
	time.Sleep(20 * time.Millisecond)
	if got := h.admitter.renewed(); len(got) != 2 {
		t.Errorf("renewals while quiescing = %v, want none", got)
	}
}

func TestRenewalIsPerTarget(t *testing.T) {
	h := start(t)
	a := h.link(h.peer())
	eventually(t, "the first renewal", func() bool { return len(h.admitter.renewed()) == 1 })
	b := h.link(h.peer())
	roundTrip(t, a, "more")
	roundTrip(t, b, "more")
	time.Sleep(20 * time.Millisecond)
	if got := h.admitter.renewed(); len(got) != 1 {
		t.Errorf("renewals = %v, want one for the target however many links carry payload", got)
	}
}

func TestAborts(t *testing.T) {
	moved := links.Gate{Addr: netip.MustParseAddr("10.11.0.5"), Token: 3}
	tests := []struct {
		name  string
		abort func(h *harness)
		want  error
	}{
		{name: "target's run ends", abort: func(h *harness) { h.world.stop("tgt", true, false) }, want: links.ErrRunEnded},
		{name: "source's run ends", abort: func(h *harness) { h.world.stop("src", true, false) }, want: links.ErrRunEnded},
		{
			name:  "target's gate retired as its run ends",
			abort: func(h *harness) { h.world.stop("tgt", false, true) },
			want:  links.ErrRunEnded,
		},
		{
			name:  "source's gate retired as its run ends",
			abort: func(h *harness) { h.world.stop("src", false, true) },
			want:  links.ErrRunEnded,
		},
		{
			name: "target resumed on another slot",
			abort: func(h *harness) {
				h.world.stop("tgt", false, true)
				h.world.start("tgt", "t2", moved)
			},
			want: links.ErrRunEnded,
		},
		{
			name: "target fenced",
			abort: func(h *harness) {
				h.doc.update(func(d *links.Doc) { d.Machines["tgt"].Fence = &links.Fence{Why: "pause"} })
				h.world.retire(tgtGate)
			},
			want: links.ErrFenced,
		},
		{name: "admission revoked", abort: func(h *harness) { h.admitter.cancel(links.ErrRevoked) }, want: links.ErrRevoked},
		{
			name: "admission cancelled after an unpublish",
			abort: func(h *harness) {
				h.doc.update(func(d *links.Doc) { d.Machines["tgt"].Ports = []int{5432} })
				h.admitter.cancel(nil)
			},
			want: links.ErrRevoked,
		},
		{
			name: "admission cancelled by the sweep",
			abort: func(h *harness) {
				h.world.stop("tgt", false, false)
				h.admitter.cancel(nil)
			},
			want: links.ErrRunEnded,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			held := hold(h)
			h.serve()
			c := h.link(h.peer())
			target := <-held

			tt.abort(h)
			expectReset(t, c)
			expectReset(t, target)
			e := h.log.wait(t, "close")
			if e.Reason != tt.want.Error() {
				t.Errorf("closed with %q, want %q", e.Reason, tt.want)
			}
			if e.BytesIn != 4 || e.BytesOut != 4 {
				t.Errorf("close = %+v, want the ping counted both ways", e)
			}
			if granted, released := h.admitter.counts(); granted != 1 || released != 1 {
				t.Errorf("granted %d and released %d, want 1 and 1", granted, released)
			}
		})
	}
}

// hold routes tgt's 8080 to a backend that answers one ping and then hands its end of the
// connection to the test, to read until the test is over.
func hold(h *harness) <-chan net.Conn {
	held := make(chan net.Conn, 1)
	done := make(chan struct{})
	h.route(tgtGate, 8080, func(b *backend, c net.Conn) {
		buf := make([]byte, len("ping"))
		if _, err := io.ReadFull(c, buf); err != nil {
			return
		}
		_, _ = c.Write(buf)
		held <- c
		<-done
	})
	h.t.Cleanup(func() { close(done) })
	return held
}

func TestResumeOnAnotherSlotReachesTheNewSlot(t *testing.T) {
	h := newHarness(t)
	h.route(tgtGate, 8080, greet("A"))
	moved := links.Gate{Addr: netip.MustParseAddr("10.11.0.5"), Token: 3}
	h.route(moved, 8080, greet("B"))
	h.serve()

	first := h.connect(h.peer())
	expectGreeting(t, first, "A")
	h.world.stop("tgt", true, true)
	h.world.start("tgt", "t2", moved)
	expectReset(t, first)

	second := h.connect(h.peer())
	expectGreeting(t, second, "B")
	if e := h.log.wait(t, "close"); e.Reason != links.ErrRunEnded.Error() {
		t.Errorf("first link closed with %q, want %q", e.Reason, links.ErrRunEnded)
	}
}

func expectGreeting(t *testing.T, c net.Conn, want string) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	got := make([]byte, len(want))
	if _, err := io.ReadFull(c, got); err != nil || string(got) != want {
		t.Fatalf("greeting %q, %v; want %q", got, err, want)
	}
	_ = c.SetReadDeadline(time.Time{})
}

func TestUnreadableStateRefusesNewLinksAndAbortsOpenOnes(t *testing.T) {
	h := start(t)
	open := h.link(h.peer())
	h.doc.setErr(errors.New("unknown version 9"))

	refused := h.connect(h.peer())
	expectNothing(t, refused)
	if e := h.log.wait(t, "refused"); e.Reason != links.ErrStateUnreadable.Error() {
		t.Errorf("refused with %q, want %q", e.Reason, links.ErrStateUnreadable)
	}
	roundTrip(t, open, "still")

	h.world.retireAll()
	expectReset(t, open)
	if e := h.log.wait(t, "close"); e.Reason != links.ErrStateUnreadable.Error() {
		t.Errorf("closed with %q, want %q", e.Reason, links.ErrStateUnreadable)
	}
}

func TestPerSourceCap(t *testing.T) {
	h := newHarness(t)
	h.p.rate.burst = 1000
	h.serve()
	h.admitter.set(func(a *fakeAdmitter) { a.perSource = links.MaxPerSource })
	conns := make([]net.Conn, links.MaxPerSource)
	for i := range conns {
		conns[i] = h.link(h.peer())
	}

	over := h.connect(h.peer())
	expectNothing(t, over)
	if e := h.log.wait(t, "refused"); e.Reason != links.ErrTooManyLinks.Error() {
		t.Errorf("refused with %q, want %q", e.Reason, links.ErrTooManyLinks)
	}
	for _, c := range conns {
		roundTrip(t, c, "ok")
	}
	if got := h.p.Stat().Machines["src"]; got != links.MaxPerSource {
		t.Errorf("Stat counts %d links from src, want %d", got, links.MaxPerSource)
	}

	_ = conns[0].Close()
	eventually(t, "a release", func() bool { _, released := h.admitter.counts(); return released == 1 })
	roundTrip(t, h.connect(h.peer()), "room again")
}

func TestHostCapStopsAccepting(t *testing.T) {
	h := newHarness(t)
	h.p.maxLinks = 2
	h.serve()
	first := h.link(h.peer())
	h.link(h.peer())

	waiting := h.connect(h.peer())
	time.Sleep(50 * time.Millisecond)
	if n := h.ingress.count(); n != 2 {
		t.Fatalf("Identify called %d times at the cap, want the third left in the backlog", n)
	}
	_ = first.Close()
	roundTrip(t, waiting, "accepted")
	if e := h.log.all(); len(e) > 0 && e[len(e)-1].Event == "refused" {
		t.Errorf("the connection held in the backlog was refused: %+v", e[len(e)-1])
	}
}

func TestAcceptRatePerSource(t *testing.T) {
	h := start(t)
	unpublished := h.peer()
	unpublished.Dst = netip.AddrPortFrom(tgtAddr, 9090)
	for range links.AcceptBurst {
		expectNothing(t, h.connect(unpublished))
	}
	h.log.waitN(t, "refused", links.AcceptBurst)

	expectNothing(t, h.connect(h.peer()))
	if e := h.log.waitN(t, "refused", links.AcceptBurst+1)[links.AcceptBurst]; e.Reason != links.ErrRateLimited.Error() {
		t.Fatalf("connection past the burst refused with %q, want %q", e.Reason, links.ErrRateLimited)
	}
	if n := h.dialer.count(); n != 0 {
		t.Errorf("dialled %d times past the rate", n)
	}

	h.clock.advance(time.Second)
	roundTrip(t, h.connect(h.peer()), "one more")
}

func TestAbortPortsLeavesOtherPortsOpen(t *testing.T) {
	h := start(t)
	revoked := h.link(h.peer())
	other := h.peer()
	other.Dst = netip.AddrPortFrom(tgtAddr, 5432)
	kept := h.link(other)

	n, err := h.p.AbortPorts(context.Background(), "tgt", []int{8080}, links.ErrRevoked.Error())
	if err != nil || n != 1 {
		t.Fatalf("AbortPorts = %d, %v; want 1, nil", n, err)
	}
	expectReset(t, revoked)
	roundTrip(t, kept, "still here")
	if got := h.p.Stat().Ports; !reflect.DeepEqual(got, map[links.Open]int{{Target: "tgt", Port: 5432}: 1}) {
		t.Errorf("Stat ports = %v, want only 5432", got)
	}
	if e := h.log.wait(t, "close"); e.Port != 8080 || e.Reason != links.ErrRevoked.Error() {
		t.Errorf("close = %+v, want 8080 revoked", e)
	}
}

func TestAbortMachineClosesBothEndsLinks(t *testing.T) {
	h := start(t)
	a := h.link(h.peer())
	other := h.peer()
	other.Dst = netip.AddrPortFrom(tgtAddr, 5432)
	b := h.link(other)

	n, err := h.p.AbortMachine(context.Background(), "src", "")
	if err != nil || n != 2 {
		t.Fatalf("AbortMachine = %d, %v; want 2, nil", n, err)
	}
	expectReset(t, a)
	expectReset(t, b)
	if got := h.p.Stat(); len(got.Ports) != 0 || len(got.Machines) != 0 {
		t.Errorf("Stat = %+v, want empty", got)
	}
	for _, e := range h.log.waitN(t, "close", 2) {
		if e.Reason != links.ErrFenced.Error() {
			t.Errorf("closed with %q, want %q", e.Reason, links.ErrFenced)
		}
	}
	if n, err := h.p.AbortMachine(context.Background(), "tgt", ""); err != nil || n != 0 {
		t.Errorf("second AbortMachine = %d, %v; want 0, nil", n, err)
	}
}

func TestAbortMachineReachesALinkStillWaking(t *testing.T) {
	h := start(t)
	waking := make(chan struct{})
	h.admitter.set(func(a *fakeAdmitter) {
		a.before = func(ctx context.Context, _ links.AdmitRequest) error {
			close(waking)
			<-ctx.Done()
			return ctx.Err()
		}
	})
	c := h.connect(h.peer())
	<-waking

	n, err := h.p.AbortMachine(context.Background(), "tgt", links.ErrPausedByHand.Error())
	if err != nil || n != 0 {
		t.Fatalf("AbortMachine = %d, %v; want 0 links that were open, nil", n, err)
	}
	expectNothing(t, c)
	if e := h.log.wait(t, "refused"); e.Reason != links.ErrPausedByHand.Error() {
		t.Errorf("refused with %q, want %q", e.Reason, links.ErrPausedByHand)
	}
	if n := h.dialer.count(); n != 0 {
		t.Errorf("dialled %d times after the abort", n)
	}
}

func TestNotServingHoldsNothing(t *testing.T) {
	check := func(t *testing.T, p *Proxy) {
		t.Helper()
		if n, err := p.AbortMachine(context.Background(), "tgt", ""); n != 0 || err != nil {
			t.Errorf("AbortMachine = %d, %v; want 0, nil", n, err)
		}
		if n, err := p.AbortPorts(context.Background(), "tgt", []int{8080}, ""); n != 0 || err != nil {
			t.Errorf("AbortPorts = %d, %v; want 0, nil", n, err)
		}
		if got := p.Stat(); got.Ports == nil || got.Machines == nil || len(got.Ports)+len(got.Machines) != 0 {
			t.Errorf("Stat = %+v, want empty counts", got)
		}
	}

	t.Run("links off", func(t *testing.T) {
		p, err := New(Config{})
		if err != nil {
			t.Fatal(err)
		}
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		if err := p.Serve(context.Background(), l); !errors.Is(err, links.ErrLinksOff) {
			t.Errorf("Serve = %v, want %v", err, links.ErrLinksOff)
		}
		check(t, p)
	})

	t.Run("before and after Serve", func(t *testing.T) {
		h := newHarness(t)
		check(t, h.p)
		h.serve()
		c := h.link(h.peer())
		if err := h.stop(); !errors.Is(err, context.Canceled) {
			t.Errorf("Serve = %v, want context.Canceled", err)
		}
		expectReset(t, c)
		if e := h.log.wait(t, "close"); e.Reason != links.ErrLinksOff.Error() {
			t.Errorf("closed with %q at shutdown, want %q", e.Reason, links.ErrLinksOff)
		}
		if granted, released := h.admitter.counts(); granted != released {
			t.Errorf("granted %d and released %d", granted, released)
		}
		check(t, h.p)
	})
}

func TestServeWaitsForTheKeeper(t *testing.T) {
	h := newHarness(t)
	h.world.ready = make(chan struct{})
	h.serve()
	c := h.connect(h.peer())
	time.Sleep(30 * time.Millisecond)
	if n := h.ingress.count(); n != 0 {
		t.Fatalf("Identify called %d times before the Keeper was ready", n)
	}
	close(h.world.ready)
	roundTrip(t, c, "ready")
}

func TestNewNeedsEveryDependencyWhenOn(t *testing.T) {
	h := newHarness(t)
	full := h.p.cfg
	for _, clear := range []func(c *Config){
		func(c *Config) { c.Admitter = nil },
		func(c *Config) { c.Runs = nil },
		func(c *Config) { c.Keeper = nil },
		func(c *Config) { c.Ingress = nil },
		func(c *Config) { c.Dialer = nil },
		func(c *Config) { c.Doc = nil },
	} {
		cfg := full
		clear(&cfg)
		if _, err := New(cfg); err == nil {
			t.Errorf("New accepted %+v", cfg)
		}
	}
}

func TestIdenticalRefusalsShareAFoldKey(t *testing.T) {
	h := start(t)
	peer := h.peer()
	peer.Dst = netip.AddrPortFrom(tgtAddr, 9090)
	expectNothing(t, h.connect(peer))
	expectNothing(t, h.connect(peer))
	got := h.log.waitN(t, "refused", 2)
	type foldKey struct {
		from, to, reason string
		port             int
	}
	key := func(e audit.LinkEntry) foldKey { return foldKey{e.FromID, e.ToID, e.Reason, e.Port} }
	if key(got[0]) != key(got[1]) || got[0].Count != 1 || got[1].Count != 1 {
		t.Errorf("refusals %+v and %+v do not fold together", got[0], got[1])
	}
}

func TestRealIngressRefusesALoopbackConnection(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := l.Accept()
		if err == nil {
			accepted <- c
		}
	}()
	c, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	server := <-accepted
	defer server.Close()

	if _, err := NewIngress(slots.Finder{}).Identify(server); err == nil || known(err) != nil {
		t.Errorf("Identify = %v, want a refusal that is not a link reason", err)
	}
}

func TestRealDialerRefusesABadGate(t *testing.T) {
	for _, a := range []links.Admission{
		{Port: 8080},
		{TargetGate: links.Gate{Addr: netip.MustParseAddr("10.11.0.2")}, Port: 8080},
		{TargetGate: tgtGate, Port: 0},
		{TargetGate: links.Gate{Addr: netip.MustParseAddr("::1"), Token: 2}, Port: 8080},
	} {
		if c, err := NewDialer().Dial(context.Background(), a); err == nil {
			_ = c.Close()
			t.Errorf("Dial(%+v) succeeded", a)
		}
	}
}

func TestReasonFor(t *testing.T) {
	if got := reasonFor("", links.ErrRevoked); got != links.ErrRevoked {
		t.Errorf("empty reason = %v, want the fallback", got)
	}
	if got := reasonFor(links.ErrFenced.Error(), links.ErrRevoked); got != links.ErrFenced {
		t.Errorf("reason %q = %v, want the shared error", links.ErrFenced, got)
	}
	if got := reasonFor("something new", links.ErrRevoked); got.Error() != "something new" {
		t.Errorf("unknown reason = %v, want its text", got)
	}
}

func isReset(err error) bool {
	return errors.Is(err, syscall.ECONNRESET) || strings.Contains(err.Error(), "connection reset")
}
