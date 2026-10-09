// Package linkguard installs and maintains the kernel fence around link traffic, and reads the
// conntrack mark that binds an accepted connection to a run.
//
// Everything else in M12 runs inside hibd proxy, and a stalled proxy runs none of it while its
// sockets go on sending. E2B hands a slot to the next sandbox links.SlotReturnDelay after a run
// ends, so a fence that depends on a user-space process is not a fence. This one is a pair of
// nftables objects whose elements expire on their own: the listener re-adds a gate only after
// re-proving its run is live, and anything that stops proving loses its gates within
// links.GuardTTL.
//
// Gates are keyed on a slot's address together with the token of the run at it, so a gate can
// never be inherited by whoever E2B puts in the slot next.
package linkguard

import (
	"net/netip"

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
// operation, with both sets empty. hibd proxy runs it at startup, before the listener accepts
// anything: links stay off until it has succeeded, because a listener without the fence cannot
// promise that a wedged process stops leaking bytes. Emptying the sets is deliberate, so a
// crash cannot leave a gate open across a restart.
func (g *Guard) Install() error { return links.ErrGuardDown }

// Remove deletes the whole table, which is a complete uninstall.
func (g *Guard) Remove() error { return links.ErrGuardDown }

// Installed reports whether the table, both sets and every chain are present with the rules
// this version expects. hibd proxy refuses to serve links while it is false, and the listener
// re-checks it every links.GuardSelfCheck.
func (g *Guard) Installed() (bool, error) { return false, links.ErrGuardDown }

// Open adds a gate and points its address at its token in the map. It fails if a gate for that
// address with a different token is still there.
func (g *Guard) Open(gate links.Gate) error { return links.ErrGuardDown }

// Extend refreshes gates with links.GuardTTL in one atomic batch, each element deleted and then
// added again. The caller must have re-checked that every run behind them is live immediately
// before calling it.
func (g *Guard) Extend(gates []links.Gate) error { return links.ErrGuardDown }

// Drop removes every gate for addr whatever its token, and addr's map entry, and returns only
// once the kernel no longer has any of them.
func (g *Guard) Drop(addr netip.Addr) error { return links.ErrGuardDown }

// Has reports whether gate is still open.
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

// Next returns a token no run has held before.
func (t *Tokens) Next() (uint16, error) { return 0, links.ErrGuardDown }

// Marks reads conntrack over ctnetlink. It satisfies links.Marks.
type Marks struct{}

// NewMarks returns the production conntrack reader.
func NewMarks() *Marks { return &Marks{} }

// Mark returns the conntrack mark of the connection with this original tuple. The caller gets
// the tuple from the accepted connection's peer address and SO_ORIGINAL_DST, which
// internal/egress/hook already reads the same way.
func (*Marks) Mark(from, to netip.AddrPort) (uint32, error) { return 0, links.ErrNoConntrack }

var (
	_ links.Guard  = (*Guard)(nil)
	_ links.Tokens = (*Tokens)(nil)
	_ links.Marks  = (*Marks)(nil)
)
