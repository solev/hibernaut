// Package linkguard installs and maintains the kernel fence around link traffic, and runs the
// Keeper that owns every gate in it.
//
// Everything else in M12 runs inside hibd proxy, and a stalled proxy runs none of it while its
// sockets go on sending. E2B hands a slot to the next sandbox links.SlotReturnDelay after a run
// ends, so a fence that depends on a user-space process is not a fence. This one is a pair of
// nftables objects whose elements expire on their own: the Keeper extends a gate only after
// re-proving its run is live and unfenced, and anything that stops proving loses its gates
// within links.GuardTTL.
//
// Gates are keyed on a slot's address together with the token of the run at it, so a gate can
// never be inherited by whoever E2B puts in the slot next.
package linkguard

import (
	"context"
	"net/netip"
	"time"

	"github.com/solev/hibernaut/internal/links"
)

// Guard is the nftables table and the chains that consult it. It satisfies links.Guard.
type Guard struct {
	// table, set and gmap are links.GuardTable, links.GuardSet and links.GuardMap in
	// production, and names a test owns in tests, so a test never touches the live table.
	table, set, gmap string
}

// New returns a guard over the production table.
func New() *Guard {
	return &Guard{table: links.GuardTable, set: links.GuardSet, gmap: links.GuardMap}
}

// Install replaces the table with this version's sets, chains and rules in one atomic
// operation, with both sets empty, and sets net.ipv4.tcp_fwmark_accept to 1. The Keeper runs it
// at startup, before the listener accepts anything: links stay off until it has succeeded,
// because a listener without the fence cannot promise that a wedged process stops leaking
// bytes. Emptying the sets is deliberate, so a crash cannot leave a gate open across a restart.
func (g *Guard) Install() error { return links.ErrGuardDown }

// Remove deletes the whole table, which is a complete uninstall. It leaves
// net.ipv4.tcp_fwmark_accept at 1.
func (g *Guard) Remove() error { return links.ErrGuardDown }

// Installed reports whether the table, both sets and every chain are present with the rules
// this version expects, and net.ipv4.tcp_fwmark_accept is still 1. hibd proxy refuses to serve
// links while it is false, and the Keeper re-checks it every links.GuardSelfCheck.
func (g *Guard) Installed() (bool, error) { return false, links.ErrGuardDown }

// Open creates a gate and points its address at its token in the map, both create-only in one
// batch, so it fails if either is already there. It refuses a token at or below the highest it
// has been asked to open, with links.ErrGateSpent.
func (g *Guard) Open(gate links.Gate) error { return links.ErrGuardDown }

// Extend refreshes gates with links.GuardTTL in one atomic batch, each element deleted and then
// added again. It never creates an element. The Keeper re-checks that every run behind them is
// live and unfenced immediately before calling it.
func (g *Guard) Extend(gates []links.Gate) error { return links.ErrGuardDown }

// Drop removes every gate for addr whatever its token, and addr's map entry, and reads them
// back until the kernel has none of them. It needs CAP_NET_ADMIN.
func (g *Guard) Drop(addr netip.Addr) error { return links.ErrGuardDown }

// Has reports whether gate is still open. A false stays false.
func (g *Guard) Has(gate links.Gate) (bool, error) { return false, links.ErrGuardDown }

// Name reports the table, set and map the guard works on, so a test can assert it is not the
// live one and hibd proxy can name it in the message it prints when the fence is missing.
func (g *Guard) Name() (table, set, gmap string) { return g.table, g.set, g.gmap }

// Tokens hands out run tokens from a counter persisted under links.RunDir. It satisfies
// links.Tokens.
type Tokens struct {
	path string
}

// NewTokens returns the production token allocator.
func NewTokens() *Tokens { return &Tokens{path: links.TokenFile} }

// Next returns a token no run has held before, starting at 1, or links.ErrTokensSpent once
// links.GuardMaxToken has been handed out.
func (t *Tokens) Next() (uint32, error) { return 0, links.ErrGuardDown }

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
}

// NewKeeper checks the configuration and returns a Keeper. It touches no kernel state until
// Run.
func NewKeeper(cfg KeeperConfig) (*Keeper, error) {
	return &Keeper{cfg: cfg, ready: make(chan struct{})}, nil
}

// Run installs the guard and keeps it until ctx is done. If the first Install fails it returns
// that error and links stay off. Otherwise it closes Ready after the first cycle and then,
// every Keep, extends the gates of live, unfenced runs, retires the rest and opens the missing
// ones, and every SelfCheck checks Installed. A failed refresh or self-check retires every gate
// and reinstalls, retrying every SelfCheck. When ctx is done it retires every gate and returns
// nil, and the gates expire within GuardTTL.
func (k *Keeper) Run(ctx context.Context) error { return links.ErrGuardDown }

// Ready is closed once the table is installed and the first cycle has run.
func (k *Keeper) Ready() <-chan struct{} { return k.ready }

// Gate returns the open gate of run of machine id, nudging a cycle and waiting for it when the
// run has none yet.
func (k *Keeper) Gate(ctx context.Context, id string, run links.Run) (links.Gate, error) {
	return links.Gate{}, links.ErrGuardDown
}

// Retired returns a channel that is closed once g is no longer extended. It is already closed
// for a gate the Keeper does not hold.
func (k *Keeper) Retired(g links.Gate) <-chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}

var (
	_ links.Guard   = (*Guard)(nil)
	_ links.Dropper = (*Guard)(nil)
	_ links.Tokens  = (*Tokens)(nil)
	_ links.Keeper  = (*Keeper)(nil)
)
