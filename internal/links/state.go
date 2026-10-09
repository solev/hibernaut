package links

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Doc is the whole of links.json. It is the only place a published port exists: E2B's metadata
// labels are fixed when a sandbox is created and a fork copies them, so a port kept there could
// neither be added later nor kept off a copy.
//
// Machines are keyed by sandbox ID. A resume keeps the ID, so a published port survives a pause,
// and a fork gets a new ID, so a copy starts with none.
type Doc struct {
	// V is Version. The decoder requires it rather than defaulting it, so a file from a newer
	// hibd stops a reader instead of being half understood.
	V int `json:"v"`
	// Machines is empty, not absent, in a document that has been written and holds nothing.
	// It uses omitzero rather than omitempty: omitempty cannot tell an allocated empty map
	// from a missing one, and the difference is the difference between "nothing published"
	// and "no state yet".
	Machines map[string]*Machine `json:"machines,omitzero"`
	// Next is the next virtual address to hand out. Addresses are never reused, so this only
	// goes up.
	Next netip.Addr `json:"next"`
}

// Machine is one machine's link state. An entry exists as soon as anything is true of the
// machine, which may be before it has published a port: a machine a person paused, or one hibd
// is in the middle of pausing, needs an entry whether or not it publishes anything.
type Machine struct {
	// Addr is the virtual address other machines resolve this one's name to, allocated on the
	// first publish and kept for the machine's life. The zero Addr means none yet.
	Addr netip.Addr `json:"addr,omitzero"`
	// Ports are the published ports, sorted, so a document round-trips byte for byte and two
	// hosts never differ by ordering alone.
	Ports []int `json:"ports,omitzero"`
	// PortGen is each port's generation, raised when that port is published or unpublished.
	// Generations are per port, not per machine, so unpublishing one port does not cut the
	// links running over the others.
	PortGen map[int]uint64 `json:"portGen,omitzero"`
	// PausedByHand records that a person paused this machine, so no link may resume it. It is
	// set by hib pause and cleared by the next resume a person asks for. It lives here rather
	// than in E2B's metadata because metadata is fixed at create.
	PausedByHand bool `json:"pausedByHand,omitzero"`
	// Fence is set while hibd is pausing, saving, forking or removing this machine, and
	// cleared when that settles, whether it worked or not. A fenced machine admits no new
	// link at either end, so a pause cannot race an admission that is already past its
	// checks.
	Fence *Fence `json:"fence,omitzero"`
}

// Fence is one pause in progress.
type Fence struct {
	// Since is when the fence went up. It is for the log and for reconciliation, not for
	// deciding whether the fence holds: a save may legitimately run for ten minutes
	// (internal/machines/snapshot.go:16-17), which no age rule can tell from a crash.
	Since time.Time `json:"since"`
	// Why is the operation holding it: "pause", "rm", "save", "fork", "pause-all" or
	// "leak scan". It goes into the refusal the caller sees.
	Why string `json:"why"`
	// Owner is the name of a lock file in the state directory that the operation flocks for
	// its whole life. The kernel drops the lock when the holder's last descriptor closes, so
	// a checker can tell a pause that is still running from one whose process is gone, and
	// no timestamp has to be trusted.
	Owner string `json:"owner"`
}

// FenceStale is how long a fence may be held before it is worth a log line and a reconcile. It
// does not decide whether the fence holds. The owner does.
const FenceStale = 2 * time.Minute

// Alive reports whether the operation that raised a fence is still running, by testing whether
// anything holds its lock file. internal/statefile.Held implements it.
type Alive func(owner string) (bool, error)

// FenceState is what a machine's fence means now.
type FenceState int

const (
	// NotFenced: no pause is in progress.
	NotFenced FenceState = iota
	// FencedLive: the operation that raised the fence is still running. The age of the fence
	// does not matter.
	FencedLive
	// FencedOrphaned: the fence is there but its owner is gone, so hibd died mid-pause and
	// the machine's real state is unknown. Admission still refuses, because the alternative
	// is admitting a link to a machine that may be half paused. The fence is cleared only
	// after the machine's state has been read back and reconciled, never by age alone.
	FencedOrphaned
)

// Fenced reports whether m is being paused now, and by an operation that still exists.
func (m *Machine) Fenced(alive Alive) (FenceState, error) {
	if m == nil || m.Fence == nil {
		return NotFenced, nil
	}
	if m.Fence.Owner == "" {
		return FencedOrphaned, nil
	}
	live, err := alive(m.Fence.Owner)
	if err != nil {
		// A fence we cannot check is a fence we obey. Guessing the other way admits links
		// to a machine that may be halfway through a pause.
		return FencedOrphaned, err
	}
	if live {
		return FencedLive, nil
	}
	return FencedOrphaned, nil
}

// Stale reports whether m's fence has been up long enough to be worth a log line and a
// reconcile. A stale fence with a live owner is still obeyed.
func (m *Machine) Stale(now time.Time) bool {
	return m != nil && m.Fence != nil && now.Sub(m.Fence.Since) >= FenceStale
}

// Gen returns the generation of one published port, and false if it is not published.
func (m *Machine) Gen(port int) (uint64, bool) {
	if m == nil || !slices.Contains(m.Ports, port) {
		return 0, false
	}
	return m.PortGen[port], true
}

// Lookup finds the machine holding a virtual address. The listener calls it for every
// connection, after the destination has been checked against CIDR.
func (d *Doc) Lookup(addr netip.Addr) (string, *Machine, bool) {
	for id, m := range d.Machines {
		if m.Addr.IsValid() && m.Addr == addr {
			return id, m, true
		}
	}
	return "", nil, false
}

// Entry returns id's entry, making an empty one if it has none. Making an entry does not
// allocate an address: a machine that is only being fenced never needs one, and addresses are
// finite.
func (d *Doc) Entry(id string) *Machine {
	if d.Machines == nil {
		d.Machines = map[string]*Machine{}
	}
	m, ok := d.Machines[id]
	if !ok {
		m = &Machine{}
		d.Machines[id] = m
	}
	return m
}

// Allocate gives id a virtual address if it has none. It fails with ErrTooManyMachines once
// MaxMachines machines hold one, and with ErrNoAddresses at the end of CIDR. Addresses are
// handed out in order and never reused, so a stale /etc/hosts line in a machine that has been
// paused for a week still names the machine it named when it was written.
func (d *Doc) Allocate(id string) (netip.Addr, error) {
	m := d.Entry(id)
	if m.Addr.IsValid() {
		return m.Addr, nil
	}
	if d.withAddresses() >= MaxMachines {
		return netip.Addr{}, fmt.Errorf("%w: the limit is %d", ErrTooManyMachines, MaxMachines)
	}
	next := d.Next
	if !next.IsValid() {
		next = First
	}
	prefix := netip.MustParsePrefix(CIDR)
	if !prefix.Contains(next) {
		return netip.Addr{}, ErrNoAddresses
	}
	m.Addr = next
	d.Next = next.Next()
	return m.Addr, nil
}

func (d *Doc) withAddresses() int {
	n := 0
	for _, m := range d.Machines {
		if m.Addr.IsValid() {
			n++
		}
	}
	return n
}

// Publish adds ports to id, raising each one's generation.
//
// The whole batch is validated and deduplicated before anything changes, so a batch that is
// refused leaves no trace at all, not even an entry for a machine that had none. Ports that are
// already published are left alone and returned in noop: publishing the same port twice is how
// a retried command looks, and it should end with the port published rather than with an error.
// A batch that is nothing but no-ops is an error, so hib port add can still tell a person that
// what they asked for was already true.
func (d *Doc) Publish(id string, ports []int) (noop []int, err error) {
	want, noop, err := d.plan(id, ports, true)
	if err != nil {
		return nil, err
	}
	if len(want) == 0 {
		return noop, fmt.Errorf("%s: %w", portList(noop), ErrAlreadyPublished)
	}
	have := 0
	if m := d.Machines[id]; m != nil {
		have = len(m.Ports)
	}
	if have+len(want) > MaxPorts {
		return nil, fmt.Errorf("%w: the limit is %d per machine", ErrTooManyPorts, MaxPorts)
	}
	m := d.Entry(id)
	if m.PortGen == nil {
		m.PortGen = map[int]uint64{}
	}
	for _, p := range want {
		m.Ports = append(m.Ports, p)
		m.PortGen[p]++
	}
	slices.Sort(m.Ports)
	return noop, nil
}

// Unpublish removes ports from id, raising each one's generation so links already using it are
// revoked. Ports that are not published are returned in noop and change nothing, a batch that
// is nothing but no-ops is an error, and the machine's address and its other ports are left
// alone either way.
func (d *Doc) Unpublish(id string, ports []int) (noop []int, err error) {
	want, noop, err := d.plan(id, ports, false)
	if err != nil {
		return nil, err
	}
	if len(want) == 0 {
		return noop, fmt.Errorf("%s: %w", portList(noop), ErrNotFound)
	}
	m := d.Machines[id]
	for _, p := range want {
		m.Ports = slices.DeleteFunc(m.Ports, func(q int) bool { return q == p })
		m.PortGen[p]++
	}
	return noop, nil
}

// plan validates and deduplicates a batch without touching the document. want is what has to
// change, in the order it was asked for; noop is what was already the way it was asked to be.
func (d *Doc) plan(id string, ports []int, publishing bool) (want, noop []int, err error) {
	var m *Machine
	if d.Machines != nil {
		m = d.Machines[id]
	}
	if len(ports) == 0 {
		return nil, nil, errors.New("no ports given")
	}
	seen := map[int]bool{}
	for _, p := range ports {
		if p < 1 || p > 65535 {
			return nil, nil, fmt.Errorf("port %d is not between 1 and 65535", p)
		}
		if IsReserved(p) {
			return nil, nil, fmt.Errorf("%d: %w", p, ErrReservedPort)
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		if m != nil && slices.Contains(m.Ports, p) == publishing {
			noop = append(noop, p)
			continue
		}
		if m == nil && !publishing {
			noop = append(noop, p)
			continue
		}
		want = append(want, p)
	}
	return want, noop, nil
}

func portList(ports []int) string {
	parts := make([]string, len(ports))
	for i, p := range ports {
		parts[i] = strconv.Itoa(p)
	}
	return strings.Join(parts, ", ")
}
