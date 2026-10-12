package slots

import (
	"fmt"
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
	before, err := r.Runs.Current(id)
	if err != nil {
		return netip.Addr{}, "", err
	}
	slot, slotErr := r.Finder.Slot(id)
	after, err := r.Runs.Current(id)
	if err != nil {
		return netip.Addr{}, "", err
	}
	if after != before {
		return netip.Addr{}, "", fmt.Errorf("slots: %s moved from run %s to %s while its slot was read: %w", id, before, after, links.ErrNotRunning)
	}
	if slotErr != nil {
		return netip.Addr{}, "", slotErr
	}
	addr, err := Addr(slot)
	if err != nil {
		return netip.Addr{}, "", err
	}
	return addr, after, nil
}

// At returns the machine and run at a slot address now, read the same way.
func (r Resolver) At(addr netip.Addr) (links.Slot, error) {
	id, err := r.Finder.Sandbox(addr, netip.Addr{})
	if err != nil {
		return links.Slot{}, err
	}
	before, err := r.Runs.Current(id)
	if err != nil {
		return links.Slot{}, err
	}
	again, slotErr := r.Finder.Sandbox(addr, netip.Addr{})
	after, err := r.Runs.Current(id)
	if err != nil {
		return links.Slot{}, err
	}
	if after != before {
		return links.Slot{}, fmt.Errorf("slots: %s moved from run %s to %s while %s was read: %w", id, before, after, addr, links.ErrNotRunning)
	}
	if slotErr != nil {
		return links.Slot{}, slotErr
	}
	if again != id {
		return links.Slot{}, fmt.Errorf("slots: %s changed from %s to %s while it was read: %w", addr, id, again, links.ErrNotRunning)
	}
	return links.Slot{ID: id, Run: after}, nil
}
