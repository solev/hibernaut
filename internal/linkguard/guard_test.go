package linkguard

import (
	"errors"
	"maps"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/links"
)

type fakeElem struct {
	data    []byte
	expires time.Time
}

// fakeNft is nftables as the guard relies on it: batches are all or nothing, deleting an
// element that is missing or has expired is ENOENT, an exclusive add of one that is there is
// EEXIST, a plain add of one that is there changes nothing, and an expired element is gone to
// every read. Time is a fake clock.
type fakeNft struct {
	mu      sync.Mutex
	now     time.Time
	r       ruleset
	present bool
	sets    map[string]map[string]fakeElem
	fwmark  bool
	// tampered is a hand edit: the table dumps with a rule missing.
	tampered bool
	// batches records every batch sent.
	batches [][]elemOp
	lists   int
	// beforeBatch runs before each batch is applied, with the lock held.
	beforeBatch func(f *fakeNft)
	batchErr    error
}

func newFakeNft() *fakeNft { return &fakeNft{now: time.Unix(1000, 0)} }

func (f *fakeNft) advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
}

// put adds an element directly, as if something else had written it.
func (f *fakeNft) put(set string, key, data []byte, ttl time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sets[set][string(key)] = fakeElem{data: data, expires: f.now.Add(ttl)}
}

func (f *fakeNft) live(set string) map[string]fakeElem {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]fakeElem{}
	for k, e := range f.sets[set] {
		if f.now.Before(e.expires) {
			out[k] = e
		}
	}
	return out
}

func (f *fakeNft) replace(r ruleset) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.r, f.present, f.tampered = r, true, false
	f.sets = map[string]map[string]fakeElem{r.set: {}, r.gmap: {}}
	return nil
}

func (f *fakeNft) exists(string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.present, nil
}

func (f *fakeNft) dump(string) (tableDump, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.present {
		return tableDump{}, false, nil
	}
	d := installedDump(f.r)
	if f.tampered {
		d.rules = d.rules[1:]
	}
	return d, true, nil
}

func (f *fakeNft) deleteTable(string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.present {
		return syscall.ENOENT
	}
	f.present, f.sets = false, nil
	return nil
}

func (f *fakeNft) batch(_ string, ops []elemOp) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batches = append(f.batches, ops)
	if f.beforeBatch != nil {
		f.beforeBatch(f)
	}
	if f.batchErr != nil {
		return f.batchErr
	}
	if !f.present {
		return syscall.ENOENT
	}
	next := map[string]map[string]fakeElem{}
	for name, s := range f.sets {
		next[name] = maps.Clone(s)
	}
	for _, op := range ops {
		s, ok := next[op.set]
		if !ok {
			return syscall.ENOENT
		}
		e, there := s[string(op.key)]
		there = there && f.now.Before(e.expires)
		switch {
		case op.del && !there:
			return syscall.ENOENT
		case op.del:
			delete(s, string(op.key))
		case there && op.excl:
			return syscall.EEXIST
		case there:
			// Adding an element that is there does not reset its timeout.
		default:
			s[string(op.key)] = fakeElem{data: op.data, expires: f.now.Add(op.timeout)}
		}
	}
	f.sets = next
	return nil
}

func (f *fakeNft) list(_, set string) ([]element, error) {
	f.mu.Lock()
	f.lists++
	present := f.present && f.sets[set] != nil
	f.mu.Unlock()
	if !present {
		return nil, syscall.ENOENT
	}
	var out []element
	for k, e := range f.live(set) {
		out = append(out, element{key: []byte(k), data: e.data})
	}
	return out, nil
}

func (f *fakeNft) get(_, set string, key []byte) (element, error) {
	f.mu.Lock()
	present := f.present && f.sets[set] != nil
	f.mu.Unlock()
	if !present {
		return element{}, syscall.ENOENT
	}
	e, ok := f.live(set)[string(key)]
	if !ok {
		return element{}, syscall.ENOENT
	}
	return element{key: key, data: e.data}, nil
}

func (f *fakeNft) fwmarkAccept() (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fwmark, nil
}

func (f *fakeNft) setFwmarkAccept() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fwmark = true
	return nil
}

// testGuard is a guard over a fake kernel, with its token file in a directory of the test's.
func testGuard(t *testing.T) (*Guard, *fakeNft) {
	t.Helper()
	nft := newFakeNft()
	g := &Guard{table: "t", set: "live", gmap: "src", tokenFile: filepath.Join(t.TempDir(), "token"), nft: nft}
	return g, nft
}

func installed(t *testing.T) (*Guard, *fakeNft) {
	t.Helper()
	g, nft := testGuard(t)
	if err := g.Install(); err != nil {
		t.Fatal(err)
	}
	return g, nft
}

var (
	addrS = netip.MustParseAddr("10.11.1.2")
	addrT = netip.MustParseAddr("10.11.2.2")
)

func has(t *testing.T, g *Guard, gate links.Gate) bool {
	t.Helper()
	ok, err := g.Has(gate)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestOpenIsCreateOnly(t *testing.T) {
	g, nft := installed(t)
	first := links.Gate{Addr: addrS, Token: 1}
	if err := g.Open(first); err != nil {
		t.Fatal(err)
	}
	// A second token at the same address finds the map entry there and changes nothing.
	if err := g.Open(links.Gate{Addr: addrS, Token: 2}); !errors.Is(err, syscall.EEXIST) {
		t.Fatalf("second open at the address: %v", err)
	}
	if n := len(nft.live("live")); n != 1 || !has(t, g, first) {
		t.Fatalf("%d gates after the refused open, first open %t", n, has(t, g, first))
	}
	// An element already in the set blocks the open, and the map entry does not appear.
	nft.put("live", gateKey(addrT, 3), nil, time.Hour)
	if err := g.Open(links.Gate{Addr: addrT, Token: 3}); !errors.Is(err, syscall.EEXIST) {
		t.Fatalf("open over an element: %v", err)
	}
	if _, ok := nft.live("src")[string(addrT.AsSlice())]; ok {
		t.Error("the refused open wrote the map entry")
	}
	for _, b := range nft.batches {
		for _, op := range b {
			if op.del || !op.excl {
				t.Errorf("open sent %+v", op)
			}
		}
	}
}

func TestOpenRefusesSpentToken(t *testing.T) {
	g, nft := installed(t)
	if err := g.Open(links.Gate{Addr: addrS, Token: 5}); err != nil {
		t.Fatal(err)
	}
	sent := len(nft.batches)
	for _, gate := range []links.Gate{{Addr: addrT, Token: 5}, {Addr: addrT, Token: 4}} {
		if err := g.Open(gate); !errors.Is(err, links.ErrGateSpent) {
			t.Errorf("open %v: %v", gate, err)
		}
	}
	// A token is spent before it is sent, so an open that failed cannot be retried.
	nft.batchErr = syscall.ENOMEM
	if err := g.Open(links.Gate{Addr: addrT, Token: 6}); !errors.Is(err, syscall.ENOMEM) {
		t.Fatalf("open with the kernel failing: %v", err)
	}
	nft.batchErr = nil
	if err := g.Open(links.Gate{Addr: addrT, Token: 6}); !errors.Is(err, links.ErrGateSpent) {
		t.Errorf("retry: %v", err)
	}
	if len(nft.batches) != sent+1 {
		t.Errorf("%d batches sent for refused opens", len(nft.batches)-sent-1)
	}
}

func TestGateValidation(t *testing.T) {
	g, nft := installed(t)
	for _, gate := range []links.Gate{
		{Addr: netip.MustParseAddr("fd00::1"), Token: 1},
		{Token: 1},
		{Addr: addrS, Token: 0},
		{Addr: addrS, Token: links.GuardMaxToken + 1},
	} {
		if err := g.Open(gate); err == nil {
			t.Errorf("opened %v", gate)
		}
		if err := g.Extend([]links.Gate{gate}); err == nil {
			t.Errorf("extended %v", gate)
		}
	}
	if len(nft.batches) != 0 {
		t.Errorf("sent %d batches for invalid gates", len(nft.batches))
	}
	// A refused gate does not spend its token.
	if err := g.Open(links.Gate{Addr: addrS, Token: 1}); err != nil {
		t.Error(err)
	}
}

func TestExtendDeletesAndAdds(t *testing.T) {
	g, nft := installed(t)
	gate := links.Gate{Addr: addrS, Token: 1}
	if err := g.Open(gate); err != nil {
		t.Fatal(err)
	}
	nft.advance(600 * time.Millisecond)
	if err := g.Extend([]links.Gate{gate}); err != nil {
		t.Fatal(err)
	}
	ops := nft.batches[len(nft.batches)-1]
	if len(ops) != 4 || !ops[0].del || ops[1].del || ops[1].excl || !ops[2].del || ops[3].del || ops[3].excl ||
		ops[0].set != "live" || ops[2].set != "src" || ops[1].timeout != links.GuardTTL {
		t.Fatalf("extend sent %+v", ops)
	}
	nft.advance(700 * time.Millisecond)
	if !has(t, g, gate) {
		t.Fatal("the extended gate expired on its first timeout")
	}
	nft.advance(400 * time.Millisecond)
	if has(t, g, gate) {
		t.Fatal("the gate outlived its extension")
	}
	// A late extension cannot bring it back.
	if err := g.Extend([]links.Gate{gate}); !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("late extend: %v", err)
	}
	if has(t, g, gate) || len(nft.live("live")) != 0 || len(nft.live("src")) != 0 {
		t.Error("a late extension resurrected the gate")
	}
	if err := g.Extend(nil); err != nil {
		t.Error(err)
	}
}

func TestExtendIsOneBatch(t *testing.T) {
	g, nft := installed(t)
	kept, gone := links.Gate{Addr: addrS, Token: 1}, links.Gate{Addr: addrT, Token: 2}
	for _, gate := range []links.Gate{kept, gone} {
		if err := g.Open(gate); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.Drop(addrT); err != nil {
		t.Fatal(err)
	}
	nft.advance(600 * time.Millisecond)
	if err := g.Extend([]links.Gate{kept, gone}); !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("extend over a dropped gate: %v", err)
	}
	nft.advance(500 * time.Millisecond)
	if has(t, g, kept) {
		t.Error("the failed batch still extended the gate beside the dropped one")
	}
}

func TestDropReadsBackUntilEmpty(t *testing.T) {
	g, nft := installed(t)
	if err := g.Open(links.Gate{Addr: addrS, Token: 1}); err != nil {
		t.Fatal(err)
	}
	if err := g.Open(links.Gate{Addr: addrT, Token: 2}); err != nil {
		t.Fatal(err)
	}
	// A leftover gate from an earlier run at the same address expires between the read and
	// the delete, which fails the batch.
	nft.put("live", gateKey(addrS, 9), nil, 300*time.Millisecond)
	first := true
	nft.beforeBatch = func(f *fakeNft) {
		if first {
			first = false
			f.now = f.now.Add(400 * time.Millisecond)
		}
	}
	if err := g.Drop(addrS); err != nil {
		t.Fatal(err)
	}
	for k := range nft.live("live") {
		if netip.AddrFrom4([4]byte([]byte(k)[:4])) == addrS {
			t.Errorf("Drop left %x", k)
		}
	}
	if _, ok := nft.live("src")[string(addrS.AsSlice())]; ok {
		t.Error("Drop left the map entry")
	}
	if !has(t, g, links.Gate{Addr: addrT, Token: 2}) {
		t.Error("Drop took another address's gate")
	}
	if nft.lists < 4 {
		t.Errorf("Drop listed %d times; it has to read back after deleting", nft.lists)
	}
	// Nothing left is nothing to do.
	if err := g.Drop(addrS); err != nil {
		t.Error(err)
	}
}

func TestDropGivesUpOnGatesThatKeepComing(t *testing.T) {
	g, nft := installed(t)
	// Every delete finds another gate appeared behind it.
	token := uint32(7)
	nft.beforeBatch = func(f *fakeNft) {
		token++
		f.sets["live"][string(gateKey(addrS, token))] = fakeElem{expires: f.now.Add(time.Hour)}
	}
	nft.put("live", gateKey(addrS, token), nil, time.Hour)
	if err := g.Drop(addrS); err == nil {
		t.Fatal("Drop returned nil with a gate still there")
	}
}

func TestDropOnMissingTable(t *testing.T) {
	g, nft := testGuard(t)
	if err := g.Drop(addrS); err != nil {
		t.Fatalf("no table and no token file: %v", err)
	}
	if err := os.WriteFile(g.tokenFile, []byte("2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := g.Drop(addrS); !errors.Is(err, links.ErrGuardDown) {
		t.Fatalf("no table with a token file: %v", err)
	}
	// A table that has lost a set is broken, token file or not.
	if err := g.Install(); err != nil {
		t.Fatal(err)
	}
	delete(nft.sets, "src")
	os.Remove(g.tokenFile)
	if err := g.Drop(addrS); err == nil {
		t.Fatal("Drop on a table without its map returned nil")
	}
}

func TestDroppedGateStaysGone(t *testing.T) {
	g, nft := installed(t)
	gate := links.Gate{Addr: addrS, Token: 1}
	if err := g.Open(gate); err != nil {
		t.Fatal(err)
	}
	if err := g.Drop(addrS); err != nil {
		t.Fatal(err)
	}
	if err := g.Open(gate); !errors.Is(err, links.ErrGateSpent) {
		t.Errorf("reopen: %v", err)
	}
	if err := g.Extend([]links.Gate{gate}); !errors.Is(err, syscall.ENOENT) {
		t.Errorf("extend: %v", err)
	}
	if has(t, g, gate) || len(nft.live("live")) != 0 {
		t.Error("the dropped gate came back")
	}
}

func TestHasChecksTheMark(t *testing.T) {
	g, nft := installed(t)
	gate := links.Gate{Addr: addrS, Token: 1}
	if err := g.Open(gate); err != nil {
		t.Fatal(err)
	}
	nft.put("src", addrS.AsSlice(), markData(2), time.Hour)
	if has(t, g, gate) {
		t.Error("Has is true with the address mapped to another token")
	}
	if has(t, g, links.Gate{Addr: addrS, Token: 2}) {
		t.Error("Has is true for a token with no element")
	}
}

func TestInstalled(t *testing.T) {
	g, nft := testGuard(t)
	if ok, err := g.Installed(); ok || err != nil {
		t.Fatalf("before Install: %t, %v", ok, err)
	}
	if err := g.Install(); err != nil {
		t.Fatal(err)
	}
	if ok, err := g.Installed(); !ok || err != nil {
		t.Fatalf("after Install: %t, %v", ok, err)
	}
	nft.fwmark = false
	if ok, _ := g.Installed(); ok {
		t.Error("installed with tcp_fwmark_accept at 0")
	}
	if err := g.Install(); err != nil {
		t.Fatal(err)
	}
	nft.tampered = true
	if ok, _ := g.Installed(); ok {
		t.Error("installed with a rule missing")
	}
	// Install replaces the table, so a gate open before it is gone after it.
	if err := g.Install(); err != nil {
		t.Fatal(err)
	}
	gate := links.Gate{Addr: addrS, Token: 1}
	if err := g.Open(gate); err != nil {
		t.Fatal(err)
	}
	if err := g.Install(); err != nil {
		t.Fatal(err)
	}
	if has(t, g, gate) {
		t.Error("a gate survived Install")
	}
	for range 2 {
		if err := g.Remove(); err != nil {
			t.Fatal(err)
		}
	}
	if ok, _ := g.Installed(); ok {
		t.Error("installed after Remove")
	}
}

func TestKeys(t *testing.T) {
	key := gateKey(addrS, 0x123456)
	if len(key) != 8 || [4]byte(key[:4]) != addrS.As4() {
		t.Fatalf("key %x", key)
	}
	if mark := markData(0x123456); len(mark) != 4 || [4]byte(key[4:]) != [4]byte(mark) {
		t.Fatalf("mark %x in key %x", mark, key)
	}
}
