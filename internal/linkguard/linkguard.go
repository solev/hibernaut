// Package linkguard installs and maintains the kernel fence around link traffic, and runs the
// Keeper that owns every gate in it.
//
// Every other part of machine links runs inside hibd proxy, and a stalled proxy runs none of it
// while its sockets go on sending. E2B hands a slot to the next sandbox links.SlotReturnDelay
// after a run ends, so a fence that depends on a user-space process is not a fence. This one is
// a pair of nftables objects whose elements expire on their own: the Keeper extends a gate only
// after re-proving its run is live and unfenced, and anything that stops proving loses its gates
// within links.GuardTTL.
//
// Gates are keyed on a slot's address together with the token of the run at it, so a gate can
// never be inherited by whoever E2B puts in the slot next.
package linkguard

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/solev/hibernaut/internal/links"
)

// Guard is the nftables table and the chains that consult it. It satisfies links.Guard.
type Guard struct {
	// table, set and gmap are links.GuardTable, links.GuardSet and links.GuardMap in
	// production, and names a test owns in tests, so a test never touches the live table.
	table, set, gmap string
	// tokenFile is links.TokenFile in production. Drop checks it when the table is missing, and
	// a test points it at a file of its own.
	tokenFile string
	// nft talks to the kernel: nfnetlink in the caller's network namespace in production.
	nft backend

	mu sync.Mutex
	// highest is the highest token Open has been asked for.
	highest uint32
}

// backend is the kernel side of the Guard. Errors from the kernel wrap its errno, so callers
// can tell ENOENT from the rest.
type backend interface {
	// replace swaps the table for r in one transaction, both sets empty.
	replace(r ruleset) error
	// exists reports whether the table is there.
	exists(table string) (bool, error)
	// dump reads the table back. ok is false when there is no table.
	dump(table string) (d tableDump, ok bool, err error)
	deleteTable(table string) error
	// batch applies ops in one transaction, all of them or none.
	batch(table string, ops []elemOp) error
	// list returns set's elements, leaving out expired ones.
	list(table, set string) ([]element, error)
	// get returns the element of set with key, or ENOENT when there is none or it has expired.
	get(table, set string, key []byte) (element, error)
	fwmarkAccept() (bool, error)
	setFwmarkAccept() error
}

// New returns a guard over the production table.
func New() *Guard {
	return &Guard{
		table:     links.GuardTable,
		set:       links.GuardSet,
		gmap:      links.GuardMap,
		tokenFile: links.TokenFile,
		nft:       newBackend(""),
	}
}

func (g *Guard) rules() ruleset { return ruleset{table: g.table, set: g.set, gmap: g.gmap} }

// Install replaces the table with this version's sets, chains and rules in one atomic
// operation, with both sets empty, and sets net.ipv4.tcp_fwmark_accept to 1. The Keeper runs it
// at startup, before the listener accepts anything: links stay off until it has succeeded,
// because a listener without the fence cannot promise that a wedged process stops leaking
// bytes. Emptying the sets is deliberate, so a crash cannot leave a gate open across a restart.
func (g *Guard) Install() error {
	if err := g.nft.replace(g.rules()); err != nil {
		return fmt.Errorf("linkguard: install table inet %s: %w", g.table, err)
	}
	if err := g.nft.setFwmarkAccept(); err != nil {
		return fmt.Errorf("linkguard: set net.ipv4.tcp_fwmark_accept to 1: %w", err)
	}
	return nil
}

// Remove deletes the whole table, which is a complete uninstall. It leaves
// net.ipv4.tcp_fwmark_accept at 1.
func (g *Guard) Remove() error {
	if err := g.nft.deleteTable(g.table); err != nil && !errors.Is(err, syscall.ENOENT) {
		return fmt.Errorf("linkguard: delete table inet %s: %w", g.table, err)
	}
	return nil
}

// Installed reports whether the table, both sets and every chain are present with the rules
// this version expects, and net.ipv4.tcp_fwmark_accept is still 1. hibd proxy refuses to serve
// links while it is false, and the Keeper re-checks it every links.GuardSelfCheck.
func (g *Guard) Installed() (bool, error) {
	d, ok, err := g.nft.dump(g.table)
	if err != nil {
		return false, fmt.Errorf("linkguard: read table inet %s: %w", g.table, err)
	}
	if !ok || !g.rules().matches(d) {
		return false, nil
	}
	on, err := g.nft.fwmarkAccept()
	if err != nil {
		return false, fmt.Errorf("linkguard: read net.ipv4.tcp_fwmark_accept: %w", err)
	}
	return on, nil
}

// Open creates a gate and points its address at its token in the map, both create-only in one
// batch, so it fails if either is already there. It refuses a token at or below the highest it
// has been asked to open, with links.ErrGateSpent.
func (g *Guard) Open(gate links.Gate) error {
	addr, err := checkGate(gate)
	if err != nil {
		return err
	}
	g.mu.Lock()
	if gate.Token <= g.highest {
		g.mu.Unlock()
		return links.ErrGateSpent
	}
	g.highest = gate.Token
	g.mu.Unlock()
	err = g.nft.batch(g.table, []elemOp{
		{excl: true, set: g.set, key: gateKey(addr, gate.Token), timeout: links.GuardTTL},
		{excl: true, set: g.gmap, key: addr.AsSlice(), data: markData(gate.Token), timeout: links.GuardTTL},
	})
	if err != nil {
		return fmt.Errorf("linkguard: open gate %s token %d: %w", addr, gate.Token, err)
	}
	return nil
}

// Extend refreshes gates with links.GuardTTL in one atomic batch, each element deleted and then
// added again. It never creates an element. The Keeper re-checks that every run behind them is
// live and unfenced immediately before calling it.
func (g *Guard) Extend(gates []links.Gate) error {
	if len(gates) == 0 {
		return nil
	}
	ops := make([]elemOp, 0, 4*len(gates))
	for _, gate := range gates {
		addr, err := checkGate(gate)
		if err != nil {
			return err
		}
		key := gateKey(addr, gate.Token)
		ops = append(ops,
			elemOp{del: true, set: g.set, key: key},
			elemOp{set: g.set, key: key, timeout: links.GuardTTL},
			elemOp{del: true, set: g.gmap, key: addr.AsSlice()},
			elemOp{set: g.gmap, key: addr.AsSlice(), data: markData(gate.Token), timeout: links.GuardTTL})
	}
	if err := g.nft.batch(g.table, ops); err != nil {
		return fmt.Errorf("linkguard: extend %d gates: %w", len(gates), err)
	}
	return nil
}

// Drop removes every gate for addr whatever its token, and addr's map entry, and reads them
// back until the kernel has none of them. It needs CAP_NET_ADMIN. A missing table is an error
// unless links.TokenFile does not exist, as links.Dropper explains.
func (g *Guard) Drop(addr netip.Addr) error {
	a := addr.Unmap()
	if !a.Is4() {
		return fmt.Errorf("linkguard: drop %s: not an IPv4 address", addr)
	}
	for range dropReads {
		ops, err := g.dropOps(a)
		if errors.Is(err, syscall.ENOENT) {
			return g.missing(a, err)
		}
		if err != nil {
			return fmt.Errorf("linkguard: drop %s: %w", a, err)
		}
		if len(ops) == 0 {
			return nil
		}
		err = g.nft.batch(g.table, ops)
		if err == nil {
			continue
		}
		if !errors.Is(err, syscall.ENOENT) {
			return fmt.Errorf("linkguard: drop %s: %w", a, err)
		}
		// One of them expired between the read and the delete, which fails the whole batch.
		// Deleting one at a time lets the rest go.
		for _, op := range ops {
			if err := g.nft.batch(g.table, []elemOp{op}); err != nil && !errors.Is(err, syscall.ENOENT) {
				return fmt.Errorf("linkguard: drop %s: %w", a, err)
			}
		}
	}
	return fmt.Errorf("linkguard: drop %s: gates still listed after %d reads", a, dropReads)
}

// dropReads bounds how often Drop reads back. Each read that is not empty follows a delete, and
// nothing but Open adds an element at an address, so a second read is almost always the last.
const dropReads = 8

func (g *Guard) dropOps(addr netip.Addr) ([]elemOp, error) {
	gates, err := g.nft.list(g.table, g.set)
	if err != nil {
		return nil, err
	}
	srcs, err := g.nft.list(g.table, g.gmap)
	if err != nil {
		return nil, err
	}
	prefix := addr.AsSlice()
	var ops []elemOp
	for _, e := range gates {
		if bytes.HasPrefix(e.key, prefix) {
			ops = append(ops, elemOp{del: true, set: g.set, key: e.key})
		}
	}
	for _, e := range srcs {
		if bytes.Equal(e.key, prefix) {
			ops = append(ops, elemOp{del: true, set: g.gmap, key: e.key})
		}
	}
	return ops, nil
}

// missing explains an ENOENT from reading the sets. A missing table is nothing to drop only
// when no token has been handed out since boot.
func (g *Guard) missing(addr netip.Addr, cause error) error {
	ok, err := g.nft.exists(g.table)
	if err != nil {
		return fmt.Errorf("linkguard: drop %s: %w", addr, err)
	}
	if ok {
		return fmt.Errorf("linkguard: drop %s: table inet %s has lost a set: %w", addr, g.table, cause)
	}
	_, err = os.Stat(g.tokenFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("linkguard: drop %s: %w", addr, err)
	}
	return fmt.Errorf("linkguard: drop %s: table inet %s is missing and %s exists, so a link may still hold a socket: %w",
		addr, g.table, g.tokenFile, links.ErrGuardDown)
}

// Has reports whether gate is still open. A false stays false.
func (g *Guard) Has(gate links.Gate) (bool, error) {
	addr, err := checkGate(gate)
	if err != nil {
		return false, err
	}
	if _, err := g.nft.get(g.table, g.set, gateKey(addr, gate.Token)); err != nil {
		if errors.Is(err, syscall.ENOENT) {
			return false, nil
		}
		return false, fmt.Errorf("linkguard: read gate %s token %d: %w", addr, gate.Token, err)
	}
	src, err := g.nft.get(g.table, g.gmap, addr.AsSlice())
	if err != nil {
		if errors.Is(err, syscall.ENOENT) {
			return false, nil
		}
		return false, fmt.Errorf("linkguard: read %s's mark: %w", addr, err)
	}
	return bytes.Equal(src.data, markData(gate.Token)), nil
}

func checkGate(gate links.Gate) (netip.Addr, error) {
	addr := gate.Addr.Unmap()
	if !addr.Is4() {
		return netip.Addr{}, fmt.Errorf("linkguard: gate address %s is not IPv4", gate.Addr)
	}
	if gate.Token == 0 || gate.Token > links.GuardMaxToken {
		return netip.Addr{}, fmt.Errorf("linkguard: gate token %d is out of range", gate.Token)
	}
	return addr, nil
}

// gateKey is a link_live key: the address in network order and the mark as the kernel holds
// it, each in a 32-bit register of its own.
func gateKey(addr netip.Addr, token uint32) []byte {
	return append(addr.AsSlice(), markData(token)...)
}

func markData(token uint32) []byte { return binary.NativeEndian.AppendUint32(nil, links.Mark(token)) }

// Name reports the table, set and map the guard works on, so a test can assert it is not the
// live one and hibd proxy can name it in the message it prints when the fence is missing.
func (g *Guard) Name() (table, set, gmap string) { return g.table, g.set, g.gmap }

// Tokens hands out run tokens from a counter persisted under links.RunDir. It satisfies
// links.Tokens.
type Tokens struct {
	path string
	mu   sync.Mutex
}

// NewTokens returns the production token allocator.
func NewTokens() *Tokens { return &Tokens{path: links.TokenFile} }

// Next returns a token no run has held before, starting at 1, or links.ErrTokensSpent once
// links.GuardMaxToken has been handed out.
func (t *Tokens) Next() (uint32, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	// The file holds the next token to hand out.
	next := uint64(1)
	b, err := os.ReadFile(t.path)
	switch {
	case err == nil:
		next, err = strconv.ParseUint(strings.TrimSpace(string(b)), 10, 32)
		if err != nil || next == 0 {
			return 0, fmt.Errorf("linkguard: %s holds %q, which is not a token", t.path, b)
		}
	case !errors.Is(err, fs.ErrNotExist):
		return 0, fmt.Errorf("linkguard: read %s: %w", t.path, err)
	}
	if next > links.GuardMaxToken {
		return 0, links.ErrTokensSpent
	}
	if err := writeAtomic(t.path, strconv.FormatUint(next+1, 10)+"\n"); err != nil {
		return 0, fmt.Errorf("linkguard: write %s: %w", t.path, err)
	}
	return uint32(next), nil
}

// writeAtomic replaces path with content by renaming a synced file over it, so a crash leaves
// either the old counter or the new one and never a truncated number.
func writeAtomic(path, content string) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// KeeperConfig is everything the Keeper needs. Every dependency is an interface or a function
// value, so the Keeper's tests run with a fake Guard and no root.
type KeeperConfig struct {
	// Guard is the kernel fence the Keeper installs and keeps.
	Guard links.Guard
	// Tokens gives each gate it opens a token.
	Tokens links.Tokens
	// Runs says which runs are live now.
	Runs links.Runs
	// Resolver finds a run's slot address, once per run, because a run never changes slot.
	Resolver links.Resolver
	// Store is read once per cycle, for the fences. Read never waits for the lock.
	Store links.Store
	// Alive tells a live fence from an orphaned one. Both keep the machine's gate shut, and so
	// does a fence or a links.json that cannot be read.
	Alive links.Alive
	// Keep and SelfCheck are the cycle and self-check periods. Zero means links.GuardKeep and
	// links.GuardSelfCheck. Tests shorten them.
	Keep, SelfCheck time.Duration
	// Logf logs reinstalls, failures and gates retired for any reason but their run ending.
	// Default: no log.
	Logf func(format string, args ...any)
}

// Keeper owns every gate. It satisfies links.Keeper.
type Keeper struct {
	cfg   KeeperConfig
	ready chan struct{}
	// nudge asks the loop for a cycle now. It holds one request, so nudges made while a cycle
	// is pending fold into it.
	nudge chan struct{}
	// stopped is closed when Run returns.
	stopped chan struct{}

	mu sync.Mutex
	// running is set by the first Run.
	running bool
	// over is set when Run returns.
	over bool
	held map[string]*held
	// byGate finds a held gate for Retired.
	byGate map[links.Gate]*held
	// down is set from a failure until a reinstall works. No gate is open while it is.
	down bool
	// spent is set once Tokens has run out, which is final until the host reboots.
	spent bool
	// started and done count the cycles begun and finished. Cycles run one at a time, so done
	// reaching a number means that cycle and every one before it is over.
	started, done uint64
	// cycled is closed and replaced at the end of every cycle.
	cycled chan struct{}
	// last is what the last cycle found, which Gate reports for a run it did not open.
	last outcome
}

// NewKeeper checks the configuration and returns a Keeper. It touches no kernel state until
// Run.
func NewKeeper(cfg KeeperConfig) (*Keeper, error) {
	switch {
	case cfg.Guard == nil, cfg.Tokens == nil, cfg.Runs == nil, cfg.Resolver == nil, cfg.Store == nil, cfg.Alive == nil:
		return nil, errors.New("linkguard: the Keeper needs a Guard, Tokens, Runs, a Resolver, a Store and Alive")
	case cfg.Keep < 0, cfg.SelfCheck < 0:
		return nil, fmt.Errorf("linkguard: negative Keeper period (Keep %v, SelfCheck %v)", cfg.Keep, cfg.SelfCheck)
	}
	if cfg.Keep == 0 {
		cfg.Keep = links.GuardKeep
	}
	if cfg.SelfCheck == 0 {
		cfg.SelfCheck = links.GuardSelfCheck
	}
	if cfg.Keep >= links.GuardTTL {
		return nil, fmt.Errorf("linkguard: Keep %v is not below GuardTTL %v, so every gate would expire between refreshes",
			cfg.Keep, links.GuardTTL)
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	return &Keeper{
		cfg:     cfg,
		ready:   make(chan struct{}),
		nudge:   make(chan struct{}, 1),
		stopped: make(chan struct{}),
		held:    map[string]*held{},
		byGate:  map[links.Gate]*held{},
		cycled:  make(chan struct{}),
	}, nil
}

// Run installs the guard and keeps it until ctx is done. If the first Install fails it returns
// that error and links stay off. Otherwise it closes Ready after the first cycle and then,
// every Keep, extends the gates of live, unfenced runs, retires the rest and opens the missing
// ones, and every SelfCheck checks Installed. A failed refresh or self-check retires every gate
// and reinstalls, retrying every SelfCheck. When ctx is done it retires every gate and returns
// nil, and the gates expire within GuardTTL.
func (k *Keeper) Run(ctx context.Context) error {
	k.mu.Lock()
	if k.running {
		k.mu.Unlock()
		return errors.New("linkguard: Keeper.Run called twice")
	}
	k.running = true
	k.mu.Unlock()

	if err := k.cfg.Guard.Install(); err != nil {
		k.stop()
		return fmt.Errorf("linkguard: install the guard: %w", err)
	}
	k.cycle()
	close(k.ready)

	keep := time.NewTicker(k.cfg.Keep)
	defer keep.Stop()
	check := time.NewTicker(k.cfg.SelfCheck)
	defer check.Stop()
	for {
		select {
		case <-ctx.Done():
			k.stop()
			return nil
		case <-keep.C:
			k.cycle()
		case <-k.nudge:
			k.cycle()
		case <-check.C:
			k.selfCheck()
		}
	}
}

// Ready is closed once the table is installed and the first cycle has run.
func (k *Keeper) Ready() <-chan struct{} { return k.ready }

// Gate returns the open gate of run of machine id, nudging a cycle and waiting for it when the
// run has none yet.
func (k *Keeper) Gate(ctx context.Context, id string, run links.Run) (links.Gate, error) {
	if err := ctx.Err(); err != nil {
		return links.Gate{}, err
	}
	select {
	case <-k.ready:
	case <-k.stopped:
		return links.Gate{}, links.ErrGuardDown
	case <-ctx.Done():
		return links.Gate{}, ctx.Err()
	}
	k.mu.Lock()
	if k.over || k.down {
		k.mu.Unlock()
		return links.Gate{}, links.ErrGuardDown
	}
	if h := k.held[id]; h != nil && h.run == run {
		k.mu.Unlock()
		return h.gate, nil
	}
	// The cycle that opens it is one that starts after this call.
	target := k.started + 1
	k.mu.Unlock()
	select {
	case k.nudge <- struct{}{}:
	default:
	}
	for {
		k.mu.Lock()
		if k.over {
			k.mu.Unlock()
			return links.Gate{}, links.ErrGuardDown
		}
		if k.done >= target {
			g, err := k.verdict(id, run)
			k.mu.Unlock()
			return g, err
		}
		cycled := k.cycled
		k.mu.Unlock()
		select {
		case <-cycled:
		case <-k.stopped:
		case <-ctx.Done():
			return links.Gate{}, ctx.Err()
		}
	}
}

// Retired returns a channel that is closed once g is no longer extended. It is already closed
// for a gate the Keeper does not hold.
func (k *Keeper) Retired(g links.Gate) <-chan struct{} {
	k.mu.Lock()
	defer k.mu.Unlock()
	if h := k.byGate[g]; h != nil {
		return h.retired
	}
	return closedChan
}

var (
	_ links.Guard   = (*Guard)(nil)
	_ links.Dropper = (*Guard)(nil)
	_ links.Tokens  = (*Tokens)(nil)
	_ links.Keeper  = (*Keeper)(nil)
)
