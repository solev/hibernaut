//go:build linux && host

package linkguard

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/solev/hibernaut/internal/atomicfile"
	"github.com/solev/hibernaut/internal/links"
)

// The guard's host tests need root, ip, nft and iptables. Each builds network namespaces of
// its own: a host with the guard in it and two machines, S at 10.11.1.2 behind veth-1 and T at
// 10.11.2.2 behind veth-2, which is where fakeResolver puts m1 and m2. The host REDIRECTs TCP
// for links.CIDR to linkPort, as the HIBERNAUT chain does, so a source reaches the link
// listener by dialling linkAddr. The kernel is what these tests are about, so each one logs
// its version.

const (
	linkPort   = 5444
	egressPort = 5443
	targetPort = 7000
)

var (
	linkAddr   = netip.AddrPortFrom(links.First, 443).String()
	linkListen = fmt.Sprintf("0.0.0.0:%d", linkPort)
	targetAddr = netip.AddrPortFrom(addrT, targetPort).String()
)

// TestGuardExpiredDeleteAbortsBatch: deleting an expired element fails with ENOENT and takes
// the whole batch with it, so an extension that comes too late can never resurrect a gate,
// nor extend the live gates sent with it.
func TestGuardExpiredDeleteAbortsBatch(t *testing.T) {
	r := newHostRig(t)
	r.install(t)
	stale, fresh := links.Gate{Addr: addrS, Token: 1}, links.Gate{Addr: addrT, Token: 2}
	start := time.Now()
	mustOpen(t, r.g, stale)
	time.Sleep(links.GuardTTL / 2)
	mustOpen(t, r.g, fresh)
	freshExpiry := time.Now().Add(links.GuardTTL)
	sleepUntil(start.Add(links.GuardTTL + links.GuardTTL/5))
	if has(t, r.g, stale) {
		t.Fatal("the stale gate outlived GuardTTL")
	}
	if !has(t, r.g, fresh) {
		t.Fatal("the fresh gate expired early")
	}
	// The fresh gate goes first, so the batch has deleted and re-added it by the time it
	// reaches the expired one.
	if err := r.g.Extend([]links.Gate{fresh, stale}); !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("extending with an expired gate in the batch: %v, want ENOENT", err)
	}
	// Had the batch committed, the fresh gate would last GuardTTL from the extension.
	sleepUntil(freshExpiry.Add(links.GuardTTL / 10))
	if has(t, r.g, fresh) {
		t.Fatal("the failed batch extended the fresh gate")
	}
	if has(t, r.g, stale) {
		t.Fatal("the failed batch brought the expired gate back")
	}
}

// TestGuardDeleteAddResetsExpiry: delete-then-add in one batch gives an element a new
// timeout. Re-adding one that is there is not relied on; the test logs what this kernel does.
func TestGuardDeleteAddResetsExpiry(t *testing.T) {
	r := newHostRig(t)
	r.install(t)
	gate, control := links.Gate{Addr: addrS, Token: 1}, links.Gate{Addr: addrT, Token: 2}
	mustOpen(t, r.g, gate)
	mustOpen(t, r.g, control)
	opened := time.Now()
	time.Sleep(links.GuardTTL * 7 / 10)
	if err := r.g.Extend([]links.Gate{gate}); err != nil {
		t.Fatal(err)
	}
	extended := time.Now()
	r.batch(t,
		elemOp{set: r.g.set, key: gateKey(addrT, control.Token), timeout: links.GuardTTL},
		elemOp{set: r.g.gmap, key: addrT.AsSlice(), data: markData(control.Token), timeout: links.GuardTTL})
	sleepUntil(opened.Add(links.GuardTTL + links.GuardTTL*3/10))
	if !has(t, r.g, gate) {
		t.Fatal("delete-then-add did not reset the timeout: the gate expired with its first one")
	}
	verb := "does not reset"
	if has(t, r.g, control) {
		verb = "resets"
	}
	t.Logf("re-adding an element that is already there %s its timeout on this kernel", verb)
	sleepUntil(extended.Add(links.GuardTTL + links.GuardTTL/10))
	if has(t, r.g, gate) {
		t.Fatal("the extended gate outlived its new timeout")
	}
}

// TestGuardOpenIsCreateOnly: Open fails on an element or a map entry that is already there,
// and changes neither, not even its timeout.
func TestGuardOpenIsCreateOnly(t *testing.T) {
	r := newHostRig(t)
	r.install(t)
	first := links.Gate{Addr: addrS, Token: 1}
	mustOpen(t, r.g, first)
	opened := time.Now()
	before := r.snapshot(t)
	// S's map entry is there, so a second token at S is refused.
	if err := r.g.Open(links.Gate{Addr: addrS, Token: 2}); !errors.Is(err, syscall.EEXIST) {
		t.Fatalf("a second open at %s: %v, want EEXIST", addrS, err)
	}
	if after := r.snapshot(t); after != before {
		t.Fatalf("the refused open changed the kernel's gates:\n%s\nwas\n%s", after, before)
	}
	// An element already in the set refuses the open, and no map entry appears for it.
	r.batch(t, elemOp{excl: true, set: r.g.set, key: gateKey(addrT, 3), timeout: links.GuardTTL})
	before = r.snapshot(t)
	if err := r.g.Open(links.Gate{Addr: addrT, Token: 3}); !errors.Is(err, syscall.EEXIST) {
		t.Fatalf("an open over an element that is there: %v, want EEXIST", err)
	}
	if after := r.snapshot(t); after != before {
		t.Fatalf("the refused open changed the kernel's gates:\n%s\nwas\n%s", after, before)
	}
	sleepUntil(opened.Add(links.GuardTTL + links.GuardTTL/10))
	if has(t, r.g, first) {
		t.Fatal("a refused open reset the timeout of the gate it found")
	}
}

// TestGuardDroppedGateStaysGone: after Open and Drop, nothing brings the gate back.
func TestGuardDroppedGateStaysGone(t *testing.T) {
	r := newHostRig(t)
	r.install(t)
	gate := links.Gate{Addr: addrS, Token: 1}
	mustOpen(t, r.g, gate)
	if err := r.g.Drop(addrS); err != nil {
		t.Fatal(err)
	}
	if err := r.g.Open(gate); !errors.Is(err, links.ErrGateSpent) {
		t.Fatalf("reopening a dropped gate: %v, want ErrGateSpent", err)
	}
	if err := r.g.Extend([]links.Gate{gate}); !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("extending a dropped gate: %v, want ENOENT", err)
	}
	if has(t, r.g, gate) {
		t.Fatal("Has reports a dropped gate")
	}
	if s := r.snapshot(t); s != "" {
		t.Fatalf("the kernel still holds:\n%s", s)
	}
}

// TestGuardDropTakesEveryGateAtAnAddress: Drop deletes every gate at S whatever its token, and
// S's map entry, and leaves T's. One of S's gates expires between Drop's read and its delete,
// which fails the batch, so Drop deletes them one at a time and reads back until the kernel lists
// none. A pause that gets no ack is what calls Drop. That it drops before it pauses is
// TestPauseWithLinksOn in internal/hibd.
func TestGuardDropTakesEveryGateAtAnAddress(t *testing.T) {
	r := newHostRig(t)
	r.install(t)
	kept := links.Gate{Addr: addrT, Token: 2}
	opened := time.Now()
	mustOpen(t, r.g, links.Gate{Addr: addrS, Token: 1})
	mustOpen(t, r.g, kept)
	// Gates earlier runs left at S, one of them about to expire.
	const expiring = 100 * time.Millisecond
	r.batch(t,
		elemOp{set: r.g.set, key: gateKey(addrS, 3), timeout: links.GuardTTL},
		elemOp{set: r.g.set, key: gateKey(addrS, 4), timeout: expiring})
	late := &lateBackend{backend: r.g.nft, delay: 3 * expiring}
	r.g.nft = late
	if err := r.g.Drop(addrS); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(late.first, syscall.ENOENT) {
		t.Fatalf("the first delete = %v, want ENOENT from the gate that expired after the read", late.first)
	}
	if late.lists < 4 {
		t.Errorf("Drop listed %d times. It has to read back after deleting", late.lists)
	}
	gates, srcs := r.kernelState(t)
	if took := time.Since(opened); took > links.GuardTTL*8/10 {
		t.Fatalf("took %s, too near GuardTTL to tell a delete from an expiry", took)
	}
	if !slices.Equal(gates, []links.Gate{kept}) || len(srcs) != 1 || srcs[addrT] != kept.Token {
		t.Fatalf("after Drop(%s) the kernel holds gates %v and map entries %v, want only %v", addrS, gates, srcs, kept)
	}
}

// lateBackend holds the first batch back by delay, keeps its error and counts the lists.
type lateBackend struct {
	backend
	delay time.Duration
	once  sync.Once
	first error
	lists int
}

func (b *lateBackend) batch(table string, ops []elemOp) error {
	held := false
	b.once.Do(func() {
		time.Sleep(b.delay)
		held = true
	})
	err := b.backend.batch(table, ops)
	if held {
		b.first = err
	}
	return err
}

func (b *lateBackend) list(table, set string) ([]element, error) {
	b.lists++
	return b.backend.list(table, set)
}

// TestGuardReopenAfterExpiry: a run whose gate expired gets a new token through Drop and
// Open, and the old token stays shut, in the sets and on the wire.
func TestGuardReopenAfterExpiry(t *testing.T) {
	r := newHostRig(t)
	r.install(t)
	at := netip.AddrPortFrom(addrS, targetPort).String()
	r.listen(t, r.srcNS, at)
	old := links.Gate{Addr: addrS, Token: 1}
	mustOpen(t, r.g, old)
	waitFor(t, links.GuardTTL*3/2, "the gate expiring", func() bool { return !has(t, r.g, old) })
	if err := r.g.Drop(addrS); err != nil {
		t.Fatal(err)
	}
	next := links.Gate{Addr: addrS, Token: 2}
	mustOpen(t, r.g, next)
	r.extend(t, next)
	if err := r.g.Extend([]links.Gate{old}); !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("extending the expired gate: %v, want ENOENT", err)
	}
	if err := r.g.Open(old); !errors.Is(err, links.ErrGateSpent) {
		t.Fatalf("reopening the expired gate: %v, want ErrGateSpent", err)
	}
	if has(t, r.g, old) || !has(t, r.g, next) {
		t.Fatalf("old gate %t, new gate %t", has(t, r.g, old), has(t, r.g, next))
	}
	// On the wire too: a socket with the old token cannot reach S, and one with the new can.
	r.refused(t, r.hostNS, at, links.Mark(old.Token))
	r.dial(t, r.hostNS, at, links.Mark(next.Token))
}

// TestGuardMapMissLeavesMarkZero: a miss in `meta mark set ip saddr map @link_src` leaves the
// mark at 0, so the SYN is dropped even with a gate for S in link_live.
func TestGuardMapMissLeavesMarkZero(t *testing.T) {
	r := newHostRig(t)
	r.install(t)
	r.nft(t, counterScript)
	ln := r.listen(t, r.hostNS, linkListen)
	// A gate for S, but no map entry to stamp its token.
	r.batch(t, elemOp{excl: true, set: r.g.set, key: gateKey(addrS, 1), timeout: time.Minute})
	r.refused(t, r.srcNS, linkAddr, 0)
	c := r.counters(t)
	if c["s_in_pre"] == 0 {
		t.Fatal("no SYN from S was seen, so the test shows nothing")
	}
	if c["s_syn_passed"] != 0 {
		t.Fatalf("%d SYNs from S passed the guard with no map entry", c["s_syn_passed"])
	}
	// The mark was 0: an element for S with mark 0 lets the same SYN through.
	r.batch(t, elemOp{excl: true, set: r.g.set, key: append(addrS.AsSlice(), 0, 0, 0, 0), timeout: time.Minute})
	r.dial(t, r.srcNS, linkAddr, 0)
	if mark := mustMark(t, accept(t, ln)); mark != 0 {
		t.Fatalf("the accepted socket's mark is %#x, want 0", mark)
	}
	if c := r.counters(t); c["s_syn_passed"] == 0 {
		t.Fatal("the SYN with mark 0 was not counted past the guard")
	}
}

// TestGuardAcceptTakesSynMark: with tcp_fwmark_accept at 1 the SYN-ACK and the accepted socket
// carry the SYN's mark, whether the handshake is held in a request socket or in a syncookie.
func TestGuardAcceptTakesSynMark(t *testing.T) {
	for _, tc := range []struct{ name, syncookies string }{
		{"request socket", "1"},
		{"syncookie", "2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newHostRig(t)
			r.install(t)
			r.nft(t, counterScript)
			r.sysctl(t, "net/ipv4/tcp_syncookies", tc.syncookies)
			gate := links.Gate{Addr: addrS, Token: 7}
			mark := links.Mark(gate.Token)
			r.nft(t, fmt.Sprintf(`add rule inet hlgtest out_last ip daddr %s tcp flags & (syn | ack) == syn | ack meta mark %#x counter name "synack_marked"`,
				addrS, mark))
			sent, recv := r.netstat(t, "SyncookiesSent"), r.netstat(t, "SyncookiesRecv")
			ln := r.listen(t, r.hostNS, linkListen)
			mustOpen(t, r.g, gate)
			c := r.dial(t, r.srcNS, linkAddr, 0)
			a := accept(t, ln)
			if got := mustMark(t, a); got != mark {
				t.Fatalf("the accepted socket's mark is %#x, want the SYN's %#x", got, mark)
			}
			counts := r.counters(t)
			if counts["synack"] == 0 || counts["synack_marked"] != counts["synack"] {
				t.Fatalf("%d of %d SYN-ACKs carried %#x", counts["synack_marked"], counts["synack"], mark)
			}
			sent, recv = r.netstat(t, "SyncookiesSent")-sent, r.netstat(t, "SyncookiesRecv")-recv
			t.Logf("syncookies sent %d, received %d", sent, recv)
			if cookies := tc.syncookies == "2"; cookies != (sent > 0 && recv > 0) {
				t.Fatalf("tcp_syncookies=%s: %d syncookies sent and %d received", tc.syncookies, sent, recv)
			}
			passes(t, c, a, "ping")
			passes(t, a, c, "pong")
		})
	}
}

// TestGuardInputUsesSocketMark: input checks the mark of the socket a packet is for, the
// accepted one and the dialled one, after REDIRECT. An old run's socket is dropped while the
// new run at the same address has its gate open, although prerouting stamps the new run's
// token on every packet from the address.
func TestGuardInputUsesSocketMark(t *testing.T) {
	r := newHostRig(t)
	r.install(t)
	r.nft(t, counterScript)
	ln := r.listen(t, r.hostNS, linkListen)
	targets := r.listen(t, r.dstNS, targetAddr)
	oldS, gateT := links.Gate{Addr: addrS, Token: 1}, links.Gate{Addr: addrT, Token: 2}
	mustOpen(t, r.g, oldS)
	mustOpen(t, r.g, gateT)
	stopS, stopT := r.extend(t, oldS), r.extend(t, gateT)

	src := r.dial(t, r.srcNS, linkAddr, 0)
	acc := accept(t, ln)
	if mark := mustMark(t, acc); mark != links.Mark(oldS.Token) {
		t.Fatalf("the accepted socket's mark is %#x, want %#x", mark, links.Mark(oldS.Token))
	}
	dst := r.dial(t, r.hostNS, targetAddr, links.Mark(gateT.Token))
	tacc := accept(t, targets)
	passes(t, src, acc, "source in")
	passes(t, acc, src, "source out")
	passes(t, dst, tacc, "target out")
	passes(t, tacc, dst, "target in")

	// S is recycled: the old run's gate goes and the new run's opens.
	stopS()
	if err := r.g.Drop(addrS); err != nil {
		t.Fatal(err)
	}
	newS := links.Gate{Addr: addrS, Token: 3}
	mustOpen(t, r.g, newS)
	r.extend(t, newS)
	before := r.counters(t)
	blocked(t, src, acc, "old run in")
	blocked(t, acc, src, "old run out")
	r.dropped(t, before, "s_in", "s_out")
	src2 := r.dial(t, r.srcNS, linkAddr, 0)
	acc2 := accept(t, ln)
	if mark := mustMark(t, acc2); mark != links.Mark(newS.Token) {
		t.Fatalf("the new run's socket's mark is %#x, want %#x", mark, links.Mark(newS.Token))
	}
	passes(t, src2, acc2, "new run in")
	passes(t, acc2, src2, "new run out")

	// T's packets carry no mark, so once T's gate is gone it is the dialled socket's mark that
	// drops them.
	stopT()
	if err := r.g.Drop(addrT); err != nil {
		t.Fatal(err)
	}
	before = r.counters(t)
	blocked(t, tacc, dst, "target in")
	blocked(t, dst, tacc, "target out")
	r.dropped(t, before, "t_in", "t_out")
}

// TestGuardEgressKeepsMarkZero: with tcp_fwmark_accept on, every socket the host accepts that
// did not come through the link path keeps mark 0, the egress proxy's included, even while
// the source has a gate open.
func TestGuardEgressKeepsMarkZero(t *testing.T) {
	r := newHostRig(t)
	r.install(t)
	r.in(t, r.hostNS, "iptables", "-w", "5", "-t", "nat", "-A", "PREROUTING", "-i", "veth-+", "-p", "tcp",
		"--dport", "443", "-j", "REDIRECT", "--to-ports", strconv.Itoa(egressPort))
	ln := r.listen(t, r.hostNS, fmt.Sprintf("0.0.0.0:%d", egressPort))
	gate := links.Gate{Addr: addrS, Token: 1}
	mustOpen(t, r.g, gate)
	r.extend(t, gate)
	if v := r.readSysctl(t, "net/ipv4/tcp_fwmark_accept"); v != "1" {
		t.Fatalf("tcp_fwmark_accept is %s after Install", v)
	}
	for _, to := range []string{"203.0.113.10:443", fmt.Sprintf("10.11.1.1:%d", egressPort)} {
		r.dial(t, r.srcNS, to, 0)
		if mark := mustMark(t, accept(t, ln)); mark != 0 {
			t.Fatalf("the egress proxy accepted %s with mark %#x", to, mark)
		}
	}
}

// TestGuardSetBackendIsRhash: the kernel puts both the set and the map in rhash, which looks
// up in O(1) and honours timeouts, and neither has the interval flag.
func TestGuardSetBackendIsRhash(t *testing.T) {
	r := newHostRig(t)
	r.install(t)
	d, ok, err := r.g.nft.dump(r.g.table)
	if err != nil || !ok {
		t.Fatalf("reading the table back: %t, %v", ok, err)
	}
	for _, set := range d.sets {
		name := attrString(set, nftaSetName)
		v, ok := attrPath(set, nftaSetFlags)
		if !ok || len(v) != 4 {
			t.Fatalf("set %s has no flags", name)
		}
		flags := binary.BigEndian.Uint32(v)
		t.Logf("set %s: flags %#x", name, flags)
		if flags&nftSetInterval != 0 || flags&nftSetTimeout == 0 {
			t.Fatalf("set %s has flags %#x: want timeout and no interval", name, flags)
		}
	}
	calls := traceInserts(t, func() { mustOpen(t, r.g, links.Gate{Addr: addrS, Token: 1}) })
	t.Logf("insert calls: %v", calls)
	for fn, n := range calls {
		if fn != "nft_rhash_insert" && n > 0 {
			t.Fatalf("opening a gate inserted through %s", fn)
		}
	}
	if calls["nft_rhash_insert"] < 2 {
		t.Fatalf("opening a gate inserted through nft_rhash_insert %d times, want the set and the map", calls["nft_rhash_insert"])
	}
}

// TestGuardDropsAllFourWaysUnderStop: SIGSTOP a proxy whose link is carrying traffic and end
// the target's run, and all four directions drop within 2*GuardTTL.
func TestGuardDropsAllFourWaysUnderStop(t *testing.T) { underStop(t, false) }

// TestGuardConntrackLossFailsClosed: the same with the link's conntrack entries deleted, over
// and over. Nothing in the guard reads conntrack, so nothing passes in any direction.
func TestGuardConntrackLossFailsClosed(t *testing.T) { underStop(t, true) }

// directions are the four ways a link's packets cross the guard, each with the counters before
// and after it.
var directions = [...]struct{ name, before, after string }{
	{"source to listener", "s_in_pre", "s_in_post"},
	{"listener to source", "s_out_pre", "s_out_post"},
	{"listener to target", "t_out_pre", "t_out_post"},
	{"target to listener", "t_in_pre", "t_in_post"},
}

// underStop stops a proxy whose Keeper holds a busy link's gates and ends the target's run.
// Within 2*GuardTTL every direction must drop: packets go on trying, counted before the
// guard, while none is counted after it and neither peer reads another byte. With
// loseConntrack the link's conntrack entries are deleted too, again and again. Then the proxy
// runs again, and it has to abort the link, open nothing for the target and give the source's
// run, still live, a gate with a new token.
func underStop(t *testing.T, loseConntrack bool) {
	var tools []string
	if loseConntrack {
		tools = append(tools, "conntrack")
	}
	r := newHostRig(t, tools...)
	r.nft(t, counterScript)
	targets := r.listen(t, r.dstNS, targetAddr)
	p := r.startProxy(t, map[string]links.Run{"m1": "r1", "m2": "r2"})
	src := r.dial(t, r.srcNS, linkAddr, 0)
	dst := accept(t, targets)
	var sTok, tTok uint32
	if _, err := fmt.Sscanf(p.expect(t, "LINK", 5*time.Second), "%d %d", &sTok, &tTok); err != nil {
		t.Fatal(err)
	}
	sp, tp := startPeer(t, src), startPeer(t, dst)
	start := r.counters(t)
	waitFor(t, 5*time.Second, "traffic in all four directions", func() bool {
		c := r.counters(t)
		for _, d := range directions {
			if c[d.after] == start[d.after] {
				return false
			}
		}
		return sp.read.Load() > 0 && tp.read.Load() > 0
	})

	p.stop(t)
	stopped := time.Now()
	p.setRuns(t, map[string]links.Run{"m1": "r1"})
	if loseConntrack {
		r.loseConntrack(t, true)
	}
	sleepUntil(stopped.Add(2 * links.GuardTTL))
	// Watch for at least two seconds, so each side's retransmissions and keepalives try more
	// than once, and until packets have tried every direction.
	base, sRead, tRead := r.counters(t), sp.read.Load(), tp.read.Load()
	watched, deadline := time.Now().Add(2*time.Second), time.Now().Add(6*time.Second)
	for tried := false; !tried || time.Now().Before(watched); {
		if loseConntrack {
			r.loseConntrack(t, false)
		}
		time.Sleep(200 * time.Millisecond)
		c := r.counters(t)
		tried = true
		for _, d := range directions {
			if n := c[d.after] - base[d.after]; n != 0 {
				t.Fatalf("%s: %d packets passed the guard %v after the proxy stopped", d.name, n, time.Since(stopped).Round(time.Millisecond))
			}
			if c[d.before] == base[d.before] {
				tried = false
				if time.Now().After(deadline) {
					t.Fatalf("%s: no packet tried to cross, so nothing showed it would be dropped", d.name)
				}
			}
		}
	}
	if sp.read.Load() != sRead || tp.read.Load() != tRead {
		t.Fatal("a peer read bytes after its link's gates expired")
	}
	t.Logf("every direction dropped, checked until %v after the proxy stopped", time.Since(stopped).Round(time.Millisecond))

	p.signal(t, unix.SIGCONT)
	p.expect(t, "ABORTED", 5*time.Second)
	waitFor(t, 3*time.Second, "a new gate for the source's run and none for the target's", func() bool {
		gates, srcs := r.kernelState(t)
		return len(gates) == 1 && gates[0].Addr == addrS && gates[0].Token > max(sTok, tTok) &&
			len(srcs) == 1 && srcs[addrS] == gates[0].Token
	})
}

// TestGuardInstallIsIdempotent: Install twice gives the same table, emptied, and Remove twice
// leaves nothing and tcp_fwmark_accept at 1.
func TestGuardInstallIsIdempotent(t *testing.T) {
	r := newHostRig(t)
	r.install(t)
	first := r.in(t, r.hostNS, "nft", "list", "table", "inet", links.GuardTable)
	t.Logf("installed:\n%s", first)
	mustOpen(t, r.g, links.Gate{Addr: addrS, Token: 1})
	r.install(t)
	if again := r.in(t, r.hostNS, "nft", "list", "table", "inet", links.GuardTable); again != first {
		t.Fatalf("a second install gave\n%s\nthe first\n%s", again, first)
	}
	if s := r.snapshot(t); s != "" {
		t.Fatalf("a reinstall kept:\n%s", s)
	}
	for range 2 {
		if err := r.g.Remove(); err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := r.g.Installed(); ok || err != nil {
		t.Fatalf("Installed after Remove: %t, %v", ok, err)
	}
	if out := r.in(t, r.hostNS, "nft", "list", "ruleset"); strings.Contains(out, links.GuardTable) {
		t.Fatalf("Remove left a trace:\n%s", out)
	}
	if v := r.readSysctl(t, "net/ipv4/tcp_fwmark_accept"); v != "1" {
		t.Fatalf("Remove set tcp_fwmark_accept to %s", v)
	}
}

// TestGuardInstalledSeesTampering: Installed reports false for any change to the table or the
// setting, and Install repairs it.
func TestGuardInstalledSeesTampering(t *testing.T) {
	r := newHostRig(t)
	table := "inet " + links.GuardTable
	for _, tc := range []struct{ name, script, fwmark string }{
		{name: "rule added", script: "add rule " + table + " input counter"},
		{name: "rules flushed", script: "flush chain " + table + " output"},
		{name: "chain added", script: "add chain " + table + " extra"},
		{name: "chain moved", script: "flush chain " + table + " input\ndelete chain " + table + " input\n" +
			"add chain " + table + " input { type filter hook input priority 0; policy accept; }"},
		{name: "table deleted", script: "delete table " + table},
		{name: "fwmark accept off", fwmark: "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r.install(t)
			if tc.script != "" {
				r.nft(t, tc.script)
			}
			if tc.fwmark != "" {
				r.sysctl(t, "net/ipv4/tcp_fwmark_accept", tc.fwmark)
			}
			if ok, err := r.g.Installed(); ok || err != nil {
				t.Fatalf("Installed after the change: %t, %v", ok, err)
			}
			r.install(t)
		})
	}
}

// slotFirewall stands in for E2B's slot-firewall, which E2B keeps in each sandbox's namespace
// with a prerouting chain at priority -150. Here it shares the host's namespace and hook with
// the guard, which is the harder case. Loading it again is E2B's atomic reload.
const slotFirewall = `table inet slot-firewall
delete table inet slot-firewall
table inet slot-firewall {
	set predefinedDenySet {
		type ipv4_addr
		flags interval
		elements = { 192.0.2.0/24 }
	}

	chain PREROUTE_FILTER {
		type filter hook prerouting priority -150; policy accept;
		iifname "veth-1" ip daddr @predefinedDenySet counter drop
	}
}
`

var (
	counterValues  = regexp.MustCompile(`counter packets \d+ bytes \d+`)
	counterPackets = regexp.MustCompile(`counter packets (\d+)`)
)

// TestGuardCoexistsWithSandboxTables: the guard installs beside hibernaut-sandboxes and E2B's
// slot-firewall without changing either or what it drops, survives a reload of E2B's table,
// and after E2B's flush ruleset reinstalls whole.
func TestGuardCoexistsWithSandboxTables(t *testing.T) {
	r := newHostRig(t)
	sandbox, err := os.ReadFile(filepath.Join("..", "..", "vm", "e2b", "sandbox-firewall.nft"))
	if err != nil {
		t.Fatal(err)
	}
	// A way out, so the host forwards what hibernaut-sandboxes is there to drop.
	r.in(t, r.hostNS, "ip", "link", "add", "internet", "type", "dummy")
	r.in(t, r.hostNS, "ip", "link", "set", "internet", "up")
	r.in(t, r.hostNS, "ip", "route", "add", "168.63.129.16/32", "dev", "internet")
	r.sysctl(t, "net/ipv4/ip_forward", "1")
	r.nft(t, string(sandbox))
	r.nft(t, slotFirewall)
	others := func() string {
		out := r.in(t, r.hostNS, "nft", "list", "table", "inet", "hibernaut-sandboxes") +
			r.in(t, r.hostNS, "nft", "list", "table", "inet", "slot-firewall")
		return counterValues.ReplaceAllString(out, "counter")
	}
	before := others()
	r.othersStillDrop(t)

	r.install(t)
	r.install(t)
	ln := r.listen(t, r.hostNS, linkListen)
	gate := links.Gate{Addr: addrS, Token: 1}
	mustOpen(t, r.g, gate)
	r.extend(t, gate)
	r.dial(t, r.srcNS, linkAddr, 0)
	if mark := mustMark(t, accept(t, ln)); mark != links.Mark(gate.Token) {
		t.Fatalf("a link beside the other tables was accepted with mark %#x", mark)
	}
	r.othersStillDrop(t)
	if after := others(); after != before {
		t.Fatalf("installing the guard changed the other tables:\n%s\nwas\n%s", after, before)
	}

	r.nft(t, slotFirewall)
	if ok, err := r.g.Installed(); !ok || err != nil {
		t.Fatalf("Installed after E2B reloaded its table: %t, %v", ok, err)
	}
	if !has(t, r.g, gate) {
		t.Fatal("E2B's reload took the gate")
	}
	r.othersStillDrop(t)

	if err := r.g.Remove(); err != nil {
		t.Fatal(err)
	}
	if after := others(); after != before {
		t.Fatalf("removing the guard changed the other tables:\n%s\nwas\n%s", after, before)
	}
	r.install(t)
	r.nft(t, "flush ruleset")
	if ok, err := r.g.Installed(); ok || err != nil {
		t.Fatalf("Installed after flush ruleset: %t, %v", ok, err)
	}
	r.install(t)
}

// TestGuardKeeperReinstallsAfterFlush: the Keeper notices E2B's flush ruleset at its next
// refresh, retires the gate it held, reinstalls and opens a new gate with a new token.
func TestGuardKeeperReinstallsAfterFlush(t *testing.T) {
	r := newHostRig(t)
	runs := &fakeRuns{live: map[string]links.Run{"m1": "r1"}}
	k, err := NewKeeper(KeeperConfig{
		Guard:    r.g,
		Tokens:   &Tokens{path: r.g.tokenFile},
		Runs:     runs,
		Resolver: &fakeResolver{runs: runs},
		Store:    &fakeStore{},
		Alive:    func(string) (bool, error) { return true, nil },
		Logf:     t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- k.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-errc; err != nil {
			t.Errorf("the Keeper: %v", err)
		}
	})
	select {
	case <-k.Ready():
	case err := <-errc:
		errc <- err
		t.Fatalf("the Keeper did not start: %v", err)
	}
	gate, err := k.Gate(ctx, "m1", "r1")
	if err != nil {
		t.Fatal(err)
	}
	if !has(t, r.g, gate) {
		t.Fatal("the Keeper's gate is not in the kernel")
	}
	r.nft(t, "flush ruleset")
	select {
	case <-k.Retired(gate):
	case <-time.After(links.GuardTTL):
		t.Fatal("the Keeper did not retire its gate after the flush")
	}
	var next links.Gate
	waitFor(t, 3*time.Second, "a new gate", func() bool {
		next, err = k.Gate(ctx, "m1", "r1")
		return err == nil
	})
	if next.Token <= gate.Token || !has(t, r.g, next) {
		t.Fatalf("after the flush the Keeper holds %+v, which the kernel has: %t", next, has(t, r.g, next))
	}
	if ok, err := r.g.Installed(); !ok || err != nil {
		t.Fatalf("Installed after the Keeper reinstalled: %t, %v", ok, err)
	}
}

// TestGuardDropOnDeletedTable: Drop fails on a deleted table while the token file exists, as
// a link may still hold a socket, and returns nil once it does not.
func TestGuardDropOnDeletedTable(t *testing.T) {
	r := newHostRig(t)
	r.install(t)
	token, err := (&Tokens{path: r.g.tokenFile}).Next()
	if err != nil {
		t.Fatal(err)
	}
	mustOpen(t, r.g, links.Gate{Addr: addrS, Token: token})
	r.nft(t, "delete table inet "+links.GuardTable)
	if err := r.g.Drop(addrS); !errors.Is(err, links.ErrGuardDown) {
		t.Fatalf("Drop with the table deleted and the token file there: %v, want ErrGuardDown", err)
	}
	if err := os.Remove(r.g.tokenFile); err != nil {
		t.Fatal(err)
	}
	if err := r.g.Drop(addrS); err != nil {
		t.Fatalf("Drop with the table deleted and no token file: %v, want nil", err)
	}
}

// TestGuardStopsMatchingWithinTTL: a gate nobody extends stops matching GuardTTL after it was
// opened, measured, and an extension that comes after that cannot bring it back.
func TestGuardStopsMatchingWithinTTL(t *testing.T) {
	r := newHostRig(t)
	r.install(t)
	r.listen(t, r.hostNS, linkListen)
	gate := links.Gate{Addr: addrS, Token: 1}
	mustOpen(t, r.g, gate)
	opened := time.Now()
	r.dial(t, r.srcNS, linkAddr, 0)
	for has(t, r.g, gate) {
		if time.Since(opened) > 2*links.GuardTTL {
			t.Fatal("the gate outlived 2*GuardTTL with nobody extending it")
		}
		time.Sleep(5 * time.Millisecond)
	}
	gone := time.Since(opened)
	t.Logf("the gate stopped matching %v after it was opened", gone.Round(time.Millisecond))
	if slack := links.GuardTTL / 10; gone > links.GuardTTL+slack || gone < links.GuardTTL-slack {
		t.Fatalf("the gate stopped matching after %v, want GuardTTL (%v)", gone, links.GuardTTL)
	}
	r.refused(t, r.srcNS, linkAddr, 0)
	if err := r.g.Extend([]links.Gate{gate}); !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("extending the expired gate: %v, want ENOENT", err)
	}
	if has(t, r.g, gate) {
		t.Fatal("a late extension brought the gate back")
	}
	r.refused(t, r.srcNS, linkAddr, 0)
}

// hostRig is the namespaces a host test runs in, and the guard in the host's.
type hostRig struct {
	hostNS, srcNS, dstNS string
	g                    *Guard
}

func newHostRig(t *testing.T, tools ...string) *hostRig {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Fatal("the guard's host tests must run as root")
	}
	for _, tool := range append([]string{"ip", "nft", "iptables"}, tools...) {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s is required: %v", tool, err)
		}
	}
	var u unix.Utsname
	if err := unix.Uname(&u); err != nil {
		t.Fatal(err)
	}
	t.Logf("kernel %s %s", unix.ByteSliceToString(u.Release[:]), unix.ByteSliceToString(u.Version[:]))

	base := fmt.Sprintf("hib-lg-%d-%d", os.Getpid(), time.Now().UnixNano())
	r := &hostRig{hostNS: base + "-host", srcNS: base + "-s", dstNS: base + "-t"}
	for _, ns := range []string{r.hostNS, r.srcNS, r.dstNS} {
		mustRun(t, "ip", "netns", "add", ns)
		t.Cleanup(func() { _ = exec.Command("ip", "netns", "delete", ns).Run() })
		r.in(t, ns, "ip", "link", "set", "lo", "up")
	}
	for n, ns := range map[int]string{1: r.srcNS, 2: r.dstNS} {
		veth := fmt.Sprintf("veth-%d", n)
		mustRun(t, "ip", "-n", r.hostNS, "link", "add", veth, "type", "veth", "peer", "name", "eth0", "netns", ns)
		r.in(t, r.hostNS, "ip", "addr", "add", fmt.Sprintf("10.11.%d.1/24", n), "dev", veth)
		r.in(t, r.hostNS, "ip", "link", "set", veth, "up")
		r.in(t, ns, "ip", "addr", "add", fmt.Sprintf("10.11.%d.2/24", n), "dev", "eth0")
		r.in(t, ns, "ip", "link", "set", "eth0", "up")
		r.in(t, ns, "ip", "route", "add", "default", "via", fmt.Sprintf("10.11.%d.1", n))
	}
	r.in(t, r.hostNS, "iptables", "-w", "5", "-t", "nat", "-A", "PREROUTING", "-d", links.CIDR, "-p", "tcp",
		"-j", "REDIRECT", "--to-ports", strconv.Itoa(linkPort))
	r.g = &Guard{
		table:     links.GuardTable,
		set:       links.GuardSet,
		gmap:      links.GuardMap,
		tokenFile: filepath.Join(t.TempDir(), "token"),
		nft:       newBackend(nsPath(r.hostNS)),
	}
	return r
}

func nsPath(ns string) string { return "/run/netns/" + ns }

// inNS runs f on a thread in network namespace ns, or in the caller's when ns is empty.
// Sockets f creates stay in the namespace.
func inNS(ns string, f func() error) error {
	if ns == "" {
		return f()
	}
	return (&netlinkBackend{netns: nsPath(ns)}).inNetns(f)
}

func mustRun(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

// in runs a command in network namespace ns.
func (r *hostRig) in(t *testing.T, ns string, args ...string) string {
	t.Helper()
	return mustRun(t, "ip", append([]string{"netns", "exec", ns}, args...)...)
}

// nft runs an nft script in the host's namespace.
func (r *hostRig) nft(t *testing.T, script string) {
	t.Helper()
	cmd := exec.Command("ip", "netns", "exec", r.hostNS, "nft", "-f", "-")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("nft: %v\n%s\n%s", err, out, script)
	}
}

func (r *hostRig) sysctl(t *testing.T, key, value string) {
	t.Helper()
	if err := inNS(r.hostNS, func() error { return os.WriteFile("/proc/sys/"+key, []byte(value+"\n"), 0) }); err != nil {
		t.Fatal(err)
	}
}

func (r *hostRig) readSysctl(t *testing.T, key string) string {
	t.Helper()
	var v []byte
	if err := inNS(r.hostNS, func() (err error) { v, err = os.ReadFile("/proc/sys/" + key); return err }); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(v))
}

// netstat reads a TcpExt counter in the host's namespace.
func (r *hostRig) netstat(t *testing.T, name string) uint64 {
	t.Helper()
	var b []byte
	if err := inNS(r.hostNS, func() (err error) { b, err = os.ReadFile("/proc/thread-self/net/netstat"); return err }); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(b), "\n")
	for i := 0; i+1 < len(lines); i += 2 {
		keys, values := strings.Fields(lines[i]), strings.Fields(lines[i+1])
		if len(keys) == 0 || keys[0] != "TcpExt:" || len(keys) != len(values) {
			continue
		}
		if j := slices.Index(keys, name); j > 0 {
			v, err := strconv.ParseUint(values[j], 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			return v
		}
	}
	t.Fatalf("no TcpExt %s in /proc/net/netstat", name)
	return 0
}

func (r *hostRig) install(t *testing.T) {
	t.Helper()
	if err := r.g.Install(); err != nil {
		t.Fatal(err)
	}
	if ok, err := r.g.Installed(); !ok || err != nil {
		t.Fatalf("Installed after Install: %t, %v", ok, err)
	}
}

func (r *hostRig) batch(t *testing.T, ops ...elemOp) {
	t.Helper()
	if err := r.g.nft.batch(r.g.table, ops); err != nil {
		t.Fatal(err)
	}
}

func mustOpen(t *testing.T, g *Guard, gate links.Gate) {
	t.Helper()
	if err := g.Open(gate); err != nil {
		t.Fatal(err)
	}
}

// extend keeps gates open, extending them every GuardKeep as the Keeper would, until the
// returned function or the end of the test stops it.
func (r *hostRig) extend(t *testing.T, gates ...links.Gate) (stop func()) {
	done, stopped := make(chan struct{}), make(chan struct{})
	var err error
	go func() {
		defer close(stopped)
		tick := time.NewTicker(links.GuardKeep)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				if err = r.g.Extend(gates); err != nil {
					return
				}
			}
		}
	}()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			close(done)
			<-stopped
			if err != nil {
				t.Errorf("extending %v: %v", gates, err)
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

// kernelState lists the gates and map entries the kernel holds and has not expired.
func (r *hostRig) kernelState(t *testing.T) (gates []links.Gate, srcs map[netip.Addr]uint32) {
	t.Helper()
	live, err := r.g.nft.list(r.g.table, r.g.set)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range live {
		if len(e.key) != 8 {
			t.Fatalf("a %d-byte key in %s", len(e.key), r.g.set)
		}
		addr, _ := netip.AddrFromSlice(e.key[:4])
		token, _ := links.Token(binary.NativeEndian.Uint32(e.key[4:]))
		gates = append(gates, links.Gate{Addr: addr, Token: token})
	}
	entries, err := r.g.nft.list(r.g.table, r.g.gmap)
	if err != nil {
		t.Fatal(err)
	}
	srcs = map[netip.Addr]uint32{}
	for _, e := range entries {
		if len(e.key) != 4 || len(e.data) != 4 {
			t.Fatalf("a %d-byte key with %d bytes of data in %s", len(e.key), len(e.data), r.g.gmap)
		}
		addr, _ := netip.AddrFromSlice(e.key)
		srcs[addr], _ = links.Token(binary.NativeEndian.Uint32(e.data))
	}
	return gates, srcs
}

// snapshot renders kernelState for comparing.
func (r *hostRig) snapshot(t *testing.T) string {
	t.Helper()
	gates, srcs := r.kernelState(t)
	var lines []string
	for _, g := range gates {
		lines = append(lines, fmt.Sprintf("gate %s %d", g.Addr, g.Token))
	}
	for addr, token := range srcs {
		lines = append(lines, fmt.Sprintf("src %s %d", addr, token))
	}
	slices.Sort(lines)
	return strings.Join(lines, "\n")
}

// counterScript is a table of the test's own that counts a link's packets on both sides of
// the guard's chains, which sit at priority -150: before them at -200 and after them at 0, so
// a packet counted before and not after was dropped by the guard. What a machine sends is
// counted before prerouting and after input, since the guard may drop it in either.
const counterScript = `table inet hlgtest {
	counter s_in_pre { packets 0 bytes 0 }
	counter s_in_post { packets 0 bytes 0 }
	counter s_out_pre { packets 0 bytes 0 }
	counter s_out_post { packets 0 bytes 0 }
	counter t_in_pre { packets 0 bytes 0 }
	counter t_in_post { packets 0 bytes 0 }
	counter t_out_pre { packets 0 bytes 0 }
	counter t_out_post { packets 0 bytes 0 }
	counter s_syn_passed { packets 0 bytes 0 }
	counter synack { packets 0 bytes 0 }
	counter synack_marked { packets 0 bytes 0 }

	chain pre_first {
		type filter hook prerouting priority -200; policy accept;
		ip saddr 10.11.1.2 meta l4proto tcp counter name "s_in_pre"
		ip saddr 10.11.2.2 meta l4proto tcp counter name "t_in_pre"
	}

	chain pre_after_guard {
		type filter hook prerouting priority -140; policy accept;
		ip saddr 10.11.1.2 tcp flags & (syn | ack) == syn counter name "s_syn_passed"
	}

	chain in_last {
		type filter hook input priority 0; policy accept;
		ip saddr 10.11.1.2 meta l4proto tcp counter name "s_in_post"
		ip saddr 10.11.2.2 meta l4proto tcp counter name "t_in_post"
	}

	chain out_first {
		type filter hook output priority -200; policy accept;
		ip daddr 10.11.1.2 meta l4proto tcp counter name "s_out_pre"
		ip daddr 10.11.2.2 meta l4proto tcp counter name "t_out_pre"
	}

	chain out_last {
		type filter hook output priority 0; policy accept;
		ip daddr 10.11.1.2 meta l4proto tcp counter name "s_out_post"
		ip daddr 10.11.2.2 meta l4proto tcp counter name "t_out_post"
		ip daddr 10.11.1.2 tcp flags & (syn | ack) == syn | ack counter name "synack"
	}
}
`

// counters reads counterScript's counters, by name.
func (r *hostRig) counters(t *testing.T) map[string]uint64 {
	t.Helper()
	out, err := exec.Command("ip", "netns", "exec", r.hostNS, "nft", "-j", "list", "table", "inet", "hlgtest").Output()
	if err != nil {
		t.Fatalf("reading the counters: %v", err)
	}
	var doc struct {
		Nftables []map[string]json.RawMessage `json:"nftables"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	got := map[string]uint64{}
	for _, obj := range doc.Nftables {
		raw, ok := obj["counter"]
		if !ok {
			continue
		}
		var c struct {
			Name    string `json:"name"`
			Packets uint64 `json:"packets"`
		}
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatal(err)
		}
		got[c.Name] = c.Packets
	}
	return got
}

// dropped checks that since before, packets tried each of the named directions and none
// passed the guard.
func (r *hostRig) dropped(t *testing.T, before map[string]uint64, prefixes ...string) {
	t.Helper()
	after := r.counters(t)
	for _, p := range prefixes {
		if n := after[p+"_post"] - before[p+"_post"]; n != 0 {
			t.Errorf("%s: %d packets passed the guard", p, n)
		}
		if after[p+"_pre"] == before[p+"_pre"] {
			t.Errorf("%s: no packet tried to cross, so nothing showed it would be dropped", p)
		}
	}
	if t.Failed() {
		t.FailNow()
	}
}

// othersStillDrop checks the other tables still drop what they are there for: the sandbox
// firewall what S sends to Azure's WireServer, and the slot firewall what S sends into its
// deny set.
func (r *hostRig) othersStillDrop(t *testing.T) {
	t.Helper()
	for _, tc := range []struct{ table, to string }{
		{"hibernaut-sandboxes", "168.63.129.16:80"},
		{"slot-firewall", "192.0.2.10:80"},
	} {
		before := r.dropCount(t, tc.table)
		r.refused(t, r.srcNS, tc.to, 0)
		if after := r.dropCount(t, tc.table); after <= before {
			t.Fatalf("table %s did not drop what S sent to %s", tc.table, tc.to)
		}
	}
}

func (r *hostRig) dropCount(t *testing.T, table string) uint64 {
	t.Helper()
	var n uint64
	for _, m := range counterPackets.FindAllStringSubmatch(r.in(t, r.hostNS, "nft", "list", "table", "inet", table), -1) {
		v, err := strconv.ParseUint(m[1], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		n += v
	}
	return n
}

var flowsDeleted = regexp.MustCompile(`(\d+) flow entr`)

// loseConntrack deletes the host's conntrack entries for both legs of the link. With
// mustFind, both legs must have had one.
func (r *hostRig) loseConntrack(t *testing.T, mustFind bool) {
	t.Helper()
	for _, sel := range [][]string{{"-s", addrS.String()}, {"-d", addrT.String()}} {
		args := append([]string{"netns", "exec", r.hostNS, "conntrack", "-D"}, sel...)
		out, _ := exec.Command("ip", args...).CombinedOutput()
		m := flowsDeleted.FindSubmatch(out)
		if m == nil {
			t.Fatalf("conntrack -D %s: %s", strings.Join(sel, " "), out)
		}
		if mustFind && string(m[1]) == "0" {
			t.Fatalf("conntrack had no entry for %s", strings.Join(sel, " "))
		}
	}
}

func (r *hostRig) listen(t *testing.T, ns, addr string) *net.TCPListener {
	t.Helper()
	var ln net.Listener
	if err := inNS(ns, func() (err error) { ln, err = net.Listen("tcp4", addr); return err }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln.(*net.TCPListener)
}

func accept(t *testing.T, ln *net.TCPListener) *net.TCPConn {
	t.Helper()
	if err := ln.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	c, err := ln.AcceptTCP()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// dialIn dials addr from network namespace ns, with SO_MARK set to mark before connecting
// when it is not 0.
func dialIn(ns, addr string, mark uint32, timeout time.Duration) (*net.TCPConn, error) {
	d := net.Dialer{Timeout: timeout}
	if mark != 0 {
		d.Control = func(_, _ string, raw syscall.RawConn) error {
			var serr error
			if err := raw.Control(func(fd uintptr) {
				serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_MARK, int(mark))
			}); err != nil {
				return err
			}
			return serr
		}
	}
	var c net.Conn
	if err := inNS(ns, func() (err error) { c, err = d.Dial("tcp4", addr); return err }); err != nil {
		return nil, err
	}
	return c.(*net.TCPConn), nil
}

func (r *hostRig) dial(t *testing.T, ns, addr string, mark uint32) *net.TCPConn {
	t.Helper()
	c, err := dialIn(ns, addr, mark, 3*time.Second)
	if err != nil {
		t.Fatalf("dialling %s with mark %#x: %v", addr, mark, err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// refused checks that a dial's SYN is dropped, not reset: the dial times out, or fails with
// EPERM when the namespace that drops it is the one it is made from.
func (r *hostRig) refused(t *testing.T, ns, addr string, mark uint32) {
	t.Helper()
	c, err := dialIn(ns, addr, mark, 700*time.Millisecond)
	if err == nil {
		c.Close()
		t.Fatalf("dialling %s with mark %#x got through", addr, mark)
	}
	if ne, ok := errors.AsType[net.Error](err); (!ok || !ne.Timeout()) && !errors.Is(err, syscall.EPERM) {
		t.Fatalf("dialling %s with mark %#x: %v, want a timeout", addr, mark, err)
	}
}

func connMark(c *net.TCPConn) (uint32, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	var (
		mark int
		serr error
	)
	if err := raw.Control(func(fd uintptr) { mark, serr = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_MARK) }); err != nil {
		return 0, err
	}
	return uint32(mark), serr
}

func mustMark(t *testing.T, c *net.TCPConn) uint32 {
	t.Helper()
	mark, err := connMark(c)
	if err != nil {
		t.Fatal(err)
	}
	return mark
}

// passes sends msg from one end and requires the other to read it.
func passes(t *testing.T, from, to *net.TCPConn, msg string) {
	t.Helper()
	if _, err := from.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}
	if err := to.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(to, buf); err != nil || string(buf) != msg {
		t.Fatalf("%q did not arrive: read %q, %v", msg, buf, err)
	}
}

// blocked sends msg from one end and requires the other to read nothing for a second.
func blocked(t *testing.T, from, to *net.TCPConn, msg string) {
	t.Helper()
	if _, err := from.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}
	if err := to.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if n, err := to.Read(make([]byte, 64)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("%q: read %d bytes, %v; want nothing", msg, n, err)
	}
}

func waitFor(t *testing.T, within time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("waited %v for %s", within, what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func sleepUntil(when time.Time) { time.Sleep(time.Until(when)) }

// The set backends' insert functions.
var setInserts = []string{"nft_rhash_insert", "nft_hash_insert", "nft_rbtree_insert", "nft_pipapo_insert", "nft_bitmap_insert"}

// traceInserts runs f under the function tracer, in a tracefs instance of its own, and counts
// the calls f's thread made to each set backend's insert function. The kernel handles an
// nfnetlink batch in the context of the thread that sends it.
func traceInserts(t *testing.T, f func()) map[string]int {
	t.Helper()
	root := ""
	for _, dir := range []string{"/sys/kernel/tracing", "/sys/kernel/debug/tracing"} {
		if _, err := os.Stat(filepath.Join(dir, "instances")); err == nil {
			root = dir
			break
		}
	}
	if root == "" {
		t.Fatal("tracefs is not mounted, and this test traces the set backend's insert function")
	}
	avail, err := os.ReadFile(filepath.Join(root, "available_filter_functions"))
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for line := range strings.Lines(string(avail)) {
		if fields := strings.Fields(line); len(fields) > 0 {
			known[fields[0]] = true
		}
	}
	if !known["nft_rhash_insert"] {
		t.Fatal("nft_rhash_insert cannot be traced on this kernel")
	}
	var funcs []string
	for _, fn := range setInserts {
		if known[fn] {
			funcs = append(funcs, fn)
		}
	}
	inst := filepath.Join(root, "instances", fmt.Sprintf("hib-lg-%d", os.Getpid()))
	if err := os.Mkdir(inst, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(inst, "current_tracer"), []byte("nop"), 0)
		_ = os.Remove(inst)
	})
	write := func(file, v string) {
		if err := os.WriteFile(filepath.Join(inst, file), []byte(v), 0); err != nil {
			t.Fatalf("tracefs %s: %v", file, err)
		}
	}
	write("set_ftrace_filter", strings.Join(funcs, " "))
	write("current_tracer", "function")
	write("tracing_on", "1")
	runtime.LockOSThread()
	self := fmt.Sprintf("-%d", unix.Gettid())
	f()
	runtime.UnlockOSThread()
	write("tracing_on", "0")
	trace, err := os.ReadFile(filepath.Join(inst, "trace"))
	if err != nil {
		t.Fatal(err)
	}
	calls := map[string]int{}
	for line := range strings.Lines(string(trace)) {
		fields := strings.Fields(line)
		if len(fields) == 0 || !strings.HasSuffix(fields[0], self) {
			continue
		}
		for _, fn := range funcs {
			if slices.Contains(fields, fn) {
				calls[fn]++
			}
		}
	}
	return calls
}

// peer keeps one end of a link busy: it writes a little every 20ms, with keepalives of a
// second, and counts what it reads.
type peer struct {
	read atomic.Int64
}

func startPeer(t *testing.T, c *net.TCPConn) *peer {
	t.Helper()
	if err := keepAlive(c); err != nil {
		t.Fatal(err)
	}
	p := &peer{}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		buf := make([]byte, 4096)
		for {
			n, err := c.Read(buf)
			p.read.Add(int64(n))
			if err != nil {
				return
			}
		}
	})
	wg.Go(func() {
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		msg := bytes.Repeat([]byte("l"), 64)
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				if _, err := c.Write(msg); err != nil {
					return
				}
			}
		}
	})
	t.Cleanup(func() {
		close(stop)
		_ = c.SetLinger(0)
		c.Close()
		wg.Wait()
	})
	return p
}

func keepAlive(c *net.TCPConn) error {
	return c.SetKeepAliveConfig(net.KeepAliveConfig{Enable: true, Idle: time.Second, Interval: time.Second, Count: 120})
}

// proxyEnv names the helper process's configuration file.
const proxyEnv = "HIB_LINKGUARD_PROXY"

type proxyConfig struct {
	Runs      string `json:"runs"`
	TokenFile string `json:"token_file"`
}

// proxyProc is a stand-in for hibd proxy, in a process of its own so a test can stop it.
type proxyProc struct {
	pid   int
	runs  string
	lines chan string
}

// startProxy starts TestGuardHelperProcess in the host's namespace, with live as the live runs.
func (r *hostRig) startProxy(t *testing.T, live map[string]links.Run) *proxyProc {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	p := &proxyProc{runs: filepath.Join(dir, "runs.json"), lines: make(chan string, 256)}
	p.setRuns(t, live)
	cfg, err := json.Marshal(proxyConfig{Runs: p.runs, TokenFile: filepath.Join(dir, "token")})
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "proxy.json")
	if err := os.WriteFile(cfgPath, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("ip", "netns", "exec", r.hostNS, exe, "-test.run", "^TestGuardHelperProcess$")
	cmd.Env = append(os.Environ(), proxyEnv+"="+cfgPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			p.lines <- sc.Text()
		}
		close(p.lines)
	}()
	t.Cleanup(func() {
		// ip netns exec execs the helper, so this is the proxy, stopped or not.
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Logf("proxy log:\n%s", stderr.String())
	})
	if p.pid, err = strconv.Atoi(p.expect(t, "READY", 10*time.Second)); err != nil {
		t.Fatal(err)
	}
	return p
}

func (p *proxyProc) setRuns(t *testing.T, live map[string]links.Run) {
	t.Helper()
	b, err := json.Marshal(live)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicfile.WriteFile(p.runs, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// expect waits for the proxy's next line that starts with word and returns the rest of it.
func (p *proxyProc) expect(t *testing.T, word string, within time.Duration) string {
	t.Helper()
	timeout := time.After(within)
	for {
		select {
		case line, ok := <-p.lines:
			if !ok {
				t.Fatalf("the proxy exited before it said %s", word)
			}
			if rest, ok := strings.CutPrefix(line, "ERR "); ok {
				t.Fatalf("the proxy: %s", rest)
			}
			if rest, ok := strings.CutPrefix(line, word); ok {
				return strings.TrimSpace(rest)
			}
		case <-timeout:
			t.Fatalf("the proxy did not say %s within %v", word, within)
		}
	}
}

func (p *proxyProc) signal(t *testing.T, sig unix.Signal) {
	t.Helper()
	if err := unix.Kill(p.pid, sig); err != nil {
		t.Fatal(err)
	}
}

// stop sends SIGSTOP and waits until every thread of the proxy has stopped.
func (p *proxyProc) stop(t *testing.T) {
	t.Helper()
	p.signal(t, unix.SIGSTOP)
	tasks := fmt.Sprintf("/proc/%d/task", p.pid)
	waitFor(t, time.Second, "the proxy stopping", func() bool {
		ents, err := os.ReadDir(tasks)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range ents {
			stat, err := os.ReadFile(filepath.Join(tasks, e.Name(), "stat"))
			if err != nil {
				return false
			}
			// The state follows the command name, which is in parentheses.
			if i := bytes.LastIndex(stat, []byte(") ")); i < 0 || i+2 >= len(stat) || stat[i+2] != 'T' {
				return false
			}
		}
		return len(ents) > 0
	})
}

// TestGuardHelperProcess is the proxy underStop stops, when proxyEnv is set. It runs a real
// Keeper over the guard in its own network namespace, reads the live runs from a file the test
// rewrites, and relays each link between its source and m2's target as hibd proxy does,
// reporting on stdout.
func TestGuardHelperProcess(t *testing.T) {
	path := os.Getenv(proxyEnv)
	if path == "" {
		return
	}
	if err := runProxy(path); err != nil {
		fmt.Println("ERR", err)
		os.Exit(1)
	}
	os.Exit(0)
}

func runProxy(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var cfg proxyConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return err
	}
	runs := fileRuns{path: cfg.Runs}
	g := &Guard{table: links.GuardTable, set: links.GuardSet, gmap: links.GuardMap, tokenFile: cfg.TokenFile, nft: newBackend("")}
	k, err := NewKeeper(KeeperConfig{
		Guard:    g,
		Tokens:   &Tokens{path: cfg.TokenFile},
		Runs:     runs,
		Resolver: &fakeResolver{runs: runs},
		Store:    &fakeStore{},
		Alive:    func(string) (bool, error) { return true, nil },
		Logf:     func(format string, args ...any) { fmt.Fprintf(os.Stderr, "keeper: "+format+"\n", args...) },
	})
	if err != nil {
		return err
	}
	ctx := context.Background()
	errc := make(chan error, 1)
	go func() { errc <- k.Run(ctx) }()
	select {
	case <-k.Ready():
	case err := <-errc:
		return fmt.Errorf("the Keeper did not start: %w", err)
	}
	ln, err := net.Listen("tcp4", linkListen)
	if err != nil {
		return err
	}
	fmt.Println("READY", os.Getpid())
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		go relay(ctx, k, runs, c.(*net.TCPConn))
	}
}

// relay admits a link the way the listener does, dials m2 with its token, carries bytes both
// ways and aborts both legs as soon as either gate is retired.
func relay(ctx context.Context, k *Keeper, runs links.Runs, src *net.TCPConn) {
	defer abort(src)
	dst, sGate, tGate, err := admit(ctx, k, runs, src)
	if err != nil {
		fmt.Println("ERR", err)
		return
	}
	defer abort(dst)
	for _, c := range []*net.TCPConn{src, dst} {
		if err := keepAlive(c); err != nil {
			fmt.Println("ERR", err)
			return
		}
	}
	fmt.Println("LINK", sGate.Token, tGate.Token)
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(dst, src); done <- struct{}{} }()
	go func() { _, _ = io.Copy(src, dst); done <- struct{}{} }()
	select {
	case <-k.Retired(sGate):
		fmt.Println("RETIRED source")
	case <-k.Retired(tGate):
		fmt.Println("RETIRED target")
	case <-done:
		fmt.Println("CLOSED")
	}
	abort(src)
	abort(dst)
	fmt.Println("ABORTED")
}

func admit(ctx context.Context, k *Keeper, runs links.Runs, src *net.TCPConn) (*net.TCPConn, links.Gate, links.Gate, error) {
	from, err := netip.ParseAddrPort(src.RemoteAddr().String())
	if err != nil {
		return nil, links.Gate{}, links.Gate{}, err
	}
	mark, err := connMark(src)
	if err != nil {
		return nil, links.Gate{}, links.Gate{}, err
	}
	id := fmt.Sprintf("m%d", from.Addr().As4()[2])
	run, err := runs.Current(id)
	if err != nil {
		return nil, links.Gate{}, links.Gate{}, err
	}
	sGate, err := k.Gate(ctx, id, run)
	if err != nil {
		return nil, links.Gate{}, links.Gate{}, err
	}
	if sGate.Addr != from.Addr() || mark != links.Mark(sGate.Token) {
		return nil, links.Gate{}, links.Gate{}, fmt.Errorf("source %s came with mark %#x and its gate is %+v", from, mark, sGate)
	}
	targetRun, err := runs.Current("m2")
	if err != nil {
		return nil, links.Gate{}, links.Gate{}, err
	}
	tGate, err := k.Gate(ctx, "m2", targetRun)
	if err != nil {
		return nil, links.Gate{}, links.Gate{}, err
	}
	dst, err := dialIn("", netip.AddrPortFrom(tGate.Addr, targetPort).String(), links.Mark(tGate.Token), 5*time.Second)
	return dst, sGate, tGate, err
}

func abort(c *net.TCPConn) {
	_ = c.SetLinger(0)
	_ = c.Close()
}

// fileRuns reads the live runs from a file the test rewrites, so the test can end a run under
// a proxy it has stopped.
type fileRuns struct{ path string }

func (f fileRuns) Live() (map[string]links.Run, error) {
	b, err := os.ReadFile(f.path)
	if err != nil {
		return nil, err
	}
	live := map[string]links.Run{}
	return live, json.Unmarshal(b, &live)
}

func (f fileRuns) Current(id string) (links.Run, error) {
	live, err := f.Live()
	if err != nil {
		return "", err
	}
	if run, ok := live[id]; ok {
		return run, nil
	}
	return "", links.ErrNotRunning
}

func (f fileRuns) Watch(string, links.Run) (<-chan struct{}, error) {
	return nil, errors.New("the proxy helper does not watch runs")
}
