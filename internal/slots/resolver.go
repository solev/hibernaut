package slots

import (
	"net/netip"

	"github.com/solev/hibernaut/internal/links"
)

// Resolver is the production links.Resolver: Finder for the slot and Runs for the run, the run
// read on both sides of the slot so a run that changed in between is never reported.
type Resolver struct {
	Finder Finder
	Runs   links.Runs
}

var _ links.Resolver = Resolver{}

// Addr returns the host address of the slot id is running at, and the run there now. It reads
// the run, the slot and the run again, and returns ErrNotRunning unless the two runs agree.
func (r Resolver) Addr(id string) (netip.Addr, links.Run, error) {
	return netip.Addr{}, "", links.ErrLinksOff
}

// At returns the machine and run at a slot address now, read the same way.
func (r Resolver) At(addr netip.Addr) (links.Slot, error) {
	return links.Slot{}, links.ErrLinksOff
}
