package links_test

import (
	"encoding/json"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/links"
)

func TestAllocateHandsOutAddressesInOrderAndNeverReusesThem(t *testing.T) {
	d := &links.Doc{V: links.Version}
	first, err := d.Allocate("sbx-a")
	if err != nil {
		t.Fatalf("allocating for sbx-a: %v", err)
	}
	if first != links.First {
		t.Errorf("first address = %s, want %s", first, links.First)
	}
	second, err := d.Allocate("sbx-b")
	if err != nil {
		t.Fatalf("allocating for sbx-b: %v", err)
	}
	if second != first.Next() {
		t.Errorf("second address = %s, want %s", second, first.Next())
	}
	again, err := d.Allocate("sbx-a")
	if err != nil || again != first {
		t.Errorf("re-allocating sbx-a = %s, %v, want %s with no error", again, err, first)
	}
	// Removing a machine must not put its address back in circulation: a paused machine's
	// /etc/hosts still names it.
	delete(d.Machines, "sbx-a")
	third, err := d.Allocate("sbx-c")
	if err != nil {
		t.Fatalf("allocating for sbx-c: %v", err)
	}
	if third == first {
		t.Errorf("sbx-c reused %s, which belonged to sbx-a", third)
	}
}

func TestAllocateStopsAtTheEndOfTheRange(t *testing.T) {
	last := netip.MustParseAddr("198.19.255.255")
	d := &links.Doc{V: links.Version, Next: last}
	if _, err := d.Allocate("sbx-a"); err != nil {
		t.Fatalf("allocating the last address: %v", err)
	}
	if _, err := d.Allocate("sbx-b"); !errors.Is(err, links.ErrNoAddresses) {
		t.Errorf("allocating past the range = %v, want %v", err, links.ErrNoAddresses)
	}
}

func TestPublishIsAllOrNothing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ports []int
		want  error
	}{
		{"the ssh bridge", []int{9090, 8081}, links.ErrReservedPort},
		{"envd", []int{49983}, links.ErrReservedPort},
		{"out of range", []int{9090, 70000}, nil},
		{"zero", []int{9090, 0}, nil},
		{"nothing at all", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &links.Doc{V: links.Version}
			if _, err := d.Publish("sbx-a", []int{8080}); err != nil {
				t.Fatalf("publishing 8080: %v", err)
			}
			before := len(d.Machines["sbx-a"].Ports)
			_, err := d.Publish("sbx-a", tc.ports)
			if err == nil {
				t.Fatalf("publishing %v succeeded, want a failure", tc.ports)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("publishing %v = %v, want %v", tc.ports, err, tc.want)
			}
			if got := len(d.Machines["sbx-a"].Ports); got != before {
				t.Errorf("a failed batch changed the ports: %d, want %d", got, before)
			}
		})
	}
}

// A refused batch must leave no trace, and a machine that had no entry must still have none:
// an entry is what an address is later allocated against, and addresses are finite.
func TestAFailedFirstPublishLeavesNoEntry(t *testing.T) {
	tooMany := make([]int, links.MaxPorts+1)
	for i := range tooMany {
		tooMany[i] = 9000 + i
	}
	for _, ports := range [][]int{{8081}, {70000}, nil, tooMany} {
		d := &links.Doc{V: links.Version}
		if _, err := d.Publish("sbx-a", ports); err == nil {
			t.Fatalf("publishing %v succeeded, want a failure", ports)
		}
		if _, ok := d.Machines["sbx-a"]; ok {
			t.Errorf("publishing %v left an entry behind", ports)
		}
	}
}

// Publishing a port that is already published is how a retried command looks. It must end with
// the port published, not with a half-applied batch.
func TestPublishingAgainIsANoOp(t *testing.T) {
	d := &links.Doc{V: links.Version}
	if _, err := d.Publish("sbx-a", []int{8080}); err != nil {
		t.Fatalf("publishing 8080: %v", err)
	}
	gen, _ := d.Machines["sbx-a"].Gen(8080)
	noop, err := d.Publish("sbx-a", []int{8080, 9090})
	if err != nil {
		t.Fatalf("republishing 8080 beside a new port: %v", err)
	}
	if len(noop) != 1 || noop[0] != 8080 {
		t.Errorf("noop = %v, want [8080]", noop)
	}
	m := d.Machines["sbx-a"]
	if got := m.Ports; len(got) != 2 || got[0] != 8080 || got[1] != 9090 {
		t.Errorf("ports = %v, want [8080 9090]", got)
	}
	if got, _ := m.Gen(8080); got != gen {
		t.Errorf("8080's generation moved to %d, want %d: a no-op must not revoke its links", got, gen)
	}
	// A batch that asks for nothing new is an error, so a person is still told.
	if _, err := d.Publish("sbx-a", []int{8080}); !errors.Is(err, links.ErrAlreadyPublished) {
		t.Errorf("publishing only an existing port = %v, want %v", err, links.ErrAlreadyPublished)
	}
}

// Removing a port that is not published is a no-op for the same reason, and a batch that is
// nothing but no-ops is still an error.
func TestUnpublishingSomethingAbsentIsANoOp(t *testing.T) {
	d := &links.Doc{V: links.Version}
	if _, err := d.Publish("sbx-a", []int{8080}); err != nil {
		t.Fatalf("publishing: %v", err)
	}
	noop, err := d.Unpublish("sbx-a", []int{8080, 9090})
	if err != nil {
		t.Fatalf("unpublishing a mixed batch: %v", err)
	}
	if len(noop) != 1 || noop[0] != 9090 {
		t.Errorf("noop = %v, want [9090]", noop)
	}
	if got := d.Machines["sbx-a"].Ports; len(got) != 0 {
		t.Errorf("ports = %v, want none left", got)
	}
	if _, err := d.Unpublish("sbx-a", []int{9090}); !errors.Is(err, links.ErrNotFound) {
		t.Errorf("unpublishing only an absent port = %v, want %v", err, links.ErrNotFound)
	}
	if _, err := d.Unpublish("sbx-never", []int{80}); !errors.Is(err, links.ErrNotFound) {
		t.Errorf("unpublishing from a machine with no entry = %v, want %v", err, links.ErrNotFound)
	}
}

// A batch is deduplicated before anything changes, so one port named twice is published once
// and its generation moves once.
func TestABatchIsDeduplicated(t *testing.T) {
	d := &links.Doc{V: links.Version}
	noop, err := d.Publish("sbx-a", []int{8080, 8080, 9090})
	if err != nil {
		t.Fatalf("publishing a batch with a repeat: %v", err)
	}
	if len(noop) != 0 {
		t.Errorf("noop = %v, want none", noop)
	}
	m := d.Machines["sbx-a"]
	if got := m.Ports; len(got) != 2 {
		t.Errorf("ports = %v, want two", got)
	}
	if got, _ := m.Gen(8080); got != 1 {
		t.Errorf("8080's generation = %d, want 1", got)
	}
}

func TestPublishRefusesPastMaxPorts(t *testing.T) {
	d := &links.Doc{V: links.Version}
	ports := make([]int, links.MaxPorts)
	for i := range ports {
		ports[i] = 9000 + i
	}
	if _, err := d.Publish("sbx-a", ports); err != nil {
		t.Fatalf("publishing %d ports: %v", links.MaxPorts, err)
	}
	if _, err := d.Publish("sbx-a", []int{8080}); !errors.Is(err, links.ErrTooManyPorts) {
		t.Errorf("publishing one more = %v, want %v", err, links.ErrTooManyPorts)
	}
}

// Generations are per port, so unpublishing one port must not revoke links running over the
// others.
func TestGenerationsArePerPort(t *testing.T) {
	d := &links.Doc{V: links.Version}
	if _, err := d.Publish("sbx-a", []int{8080, 9090}); err != nil {
		t.Fatalf("publishing: %v", err)
	}
	m := d.Machines["sbx-a"]
	kept, ok := m.Gen(9090)
	if !ok {
		t.Fatal("9090 is not published")
	}
	if _, err := d.Unpublish("sbx-a", []int{8080}); err != nil {
		t.Fatalf("unpublishing 8080: %v", err)
	}
	if _, ok := m.Gen(8080); ok {
		t.Error("8080 is still published after unpublishing it")
	}
	if got, ok := m.Gen(9090); !ok || got != kept {
		t.Errorf("9090's generation = %d, %v, want %d unchanged", got, ok, kept)
	}
	if _, err := d.Publish("sbx-a", []int{8080}); err != nil {
		t.Fatalf("republishing 8080: %v", err)
	}
	if got, _ := m.Gen(8080); got < 2 {
		t.Errorf("8080's generation after a republish = %d, want it raised twice", got)
	}
}

// A batch that cannot be understood at all changes nothing, the same as a publish. A reserved
// port is refused here too, because it can never have been published.
func TestUnpublishIsAllOrNothing(t *testing.T) {
	d := &links.Doc{V: links.Version}
	if _, err := d.Publish("sbx-a", []int{8080}); err != nil {
		t.Fatalf("publishing: %v", err)
	}
	for _, tc := range []struct {
		ports []int
		want  error
	}{
		{[]int{8080, 8081}, links.ErrReservedPort},
		{[]int{49983}, links.ErrReservedPort},
		{[]int{8080, 70000}, nil},
		{[]int{8080, 0}, nil},
		{nil, nil},
	} {
		_, err := d.Unpublish("sbx-a", tc.ports)
		if err == nil {
			t.Errorf("unpublishing %v succeeded, want a failure", tc.ports)
		}
		if tc.want != nil && !errors.Is(err, tc.want) {
			t.Errorf("unpublishing %v = %v, want %v", tc.ports, err, tc.want)
		}
		if len(d.Machines["sbx-a"].Ports) != 1 {
			t.Fatalf("unpublishing %v changed the ports", tc.ports)
		}
	}
}

// A fence holds for exactly as long as the operation that raised it is alive. Age decides
// nothing: a save may run for ten minutes, which no staleness rule could tell from a crash.
func TestAFenceHoldsWhileItsOwnerLives(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	live := func(string) (bool, error) { return true, nil }
	dead := func(string) (bool, error) { return false, nil }

	old := &links.Machine{Fence: &links.Fence{Since: now.Add(-time.Hour), Why: "save", Owner: "fence-a.lock"}}
	if got, err := old.Fenced(live); err != nil || got != links.FencedLive {
		t.Errorf("an hour-old fence with a live owner = %v, %v, want FencedLive", got, err)
	}
	if !old.Stale(now) {
		t.Error("an hour-old fence is not reported stale")
	}

	// A dead owner is not an all-clear. The machine may be halfway through a pause, so
	// admission goes on refusing until its real state has been read back.
	if got, err := old.Fenced(dead); err != nil || got != links.FencedOrphaned {
		t.Errorf("a fence with a dead owner = %v, %v, want FencedOrphaned", got, err)
	}

	fresh := &links.Machine{Fence: &links.Fence{Since: now, Why: "pause", Owner: "fence-b.lock"}}
	if got, err := fresh.Fenced(live); err != nil || got != links.FencedLive {
		t.Errorf("a fresh fence = %v, %v, want FencedLive", got, err)
	}
	if fresh.Stale(now) {
		t.Error("a fresh fence is reported stale")
	}

	if got, err := (&links.Machine{}).Fenced(live); err != nil || got != links.NotFenced {
		t.Errorf("a machine with no fence = %v, %v, want NotFenced", got, err)
	}
	if got, err := (*links.Machine)(nil).Fenced(live); err != nil || got != links.NotFenced {
		t.Errorf("a nil machine = %v, %v, want NotFenced", got, err)
	}

	// A check we cannot complete is obeyed, not waved through.
	broken := func(string) (bool, error) { return false, errors.New("no state directory") }
	if got, _ := fresh.Fenced(broken); got != links.FencedOrphaned {
		t.Errorf("a fence we cannot check = %v, want FencedOrphaned", got)
	}
}

type clock map[string]time.Time

func (c clock) Active(id string) time.Time   { return c[id] }
func (c clock) Touch(id string, t time.Time) { c[id] = t }

// Waking and renewing share one freshness rule, so the two cannot drift apart and let a ring of
// machines keep itself running.
func TestFreshAndLeaseUntilFollowTheActivityClock(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	c := clock{"sbx-busy": now.Add(-time.Minute), "sbx-stale": now.Add(-links.LinkGrace)}
	if !links.Fresh(c, "sbx-busy", now) {
		t.Error("a machine active a minute ago is not fresh")
	}
	if links.Fresh(c, "sbx-stale", now) {
		t.Error("a machine last active LinkGrace ago is still fresh")
	}
	if links.Fresh(c, "sbx-never", now) {
		t.Error("a machine that was never active is fresh")
	}
	if got, want := links.LeaseUntil(c, "sbx-busy", now), now.Add(-time.Minute).Add(links.LinkGrace); !got.Equal(want) {
		t.Errorf("lease = %s, want %s", got, want)
	}
	if got := links.LeaseUntil(c, "sbx-never", now); !got.IsZero() {
		t.Errorf("lease for a machine that was never active = %s, want the zero time", got)
	}
}

func TestReservedPorts(t *testing.T) {
	for _, p := range []int{8081, 49983} {
		if !links.IsReserved(p) {
			t.Errorf("%d is not reserved", p)
		}
	}
	if links.IsReserved(8080) {
		t.Error("8080 is reserved")
	}
}

// An empty document has to be distinguishable from a missing one, and the version is required
// rather than defaulted so a file from a newer hibd stops a reader.
func TestAnEmptyDocumentRoundTrips(t *testing.T) {
	d := &links.Doc{V: links.Version, Machines: map[string]*links.Machine{}, Next: links.First}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	const want = `{"v":1,"machines":{},"next":"198.18.0.1"}`
	if string(b) != want {
		t.Errorf("an empty document is %s, want %s", b, want)
	}
	var back links.Doc
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if back.V != links.Version || back.Machines == nil {
		t.Errorf("round trip lost the shape: %+v", back)
	}
}

// A gate outlives its run by less than 2*GuardTTL: the run is checked before the refresh, and
// a refresh may land just before the old element expires. That has to fit inside the delay E2B
// waits before handing a freed slot to another machine, with room to spare.
func TestGuardClosesBeforeTheSlotIsReused(t *testing.T) {
	if 2*links.GuardTTL+links.GuardMargin > links.SlotReturnDelay {
		t.Errorf("2*GuardTTL (%s) plus GuardMargin (%s) does not fit inside SlotReturnDelay (%s)",
			2*links.GuardTTL, links.GuardMargin, links.SlotReturnDelay)
	}
	if links.GuardKeep*2 >= links.GuardTTL {
		t.Errorf("GuardKeep %s leaves no room for a missed refresh inside GuardTTL %s", links.GuardKeep, links.GuardTTL)
	}
	// CtlTimeout has to outlast the close handshake it carries, or a caller gives up on a
	// handshake that was about to succeed.
	if links.CtlTimeout <= links.LinkCloseGrace {
		t.Errorf("CtlTimeout %s does not outlast LinkCloseGrace %s", links.CtlTimeout, links.LinkCloseGrace)
	}
}

// A mark carries the guard's byte and the run's 24-bit token, and nothing else is mistaken for
// ours, not even our own byte with token 0, which no run holds.
func TestMarksCarryTheirToken(t *testing.T) {
	for _, token := range []uint32{1, 4242, links.GuardMaxToken} {
		got, ok := links.Token(links.Mark(token))
		if !ok || got != token {
			t.Errorf("round trip of token %d = %d, %v", token, got, ok)
		}
	}
	if _, ok := links.Token(links.Mark(0)); ok {
		t.Error("token 0 looks like a run's token")
	}
	if _, ok := links.Token(0x4d00_0001); ok {
		t.Error("a mark with another top byte looks like a link")
	}
	if _, ok := links.Token(0); ok {
		t.Error("an unmarked packet looks like a link")
	}
	if got := links.Mark(links.GuardMaxToken) >> 24; got != 0x4c {
		t.Errorf("the top byte of the largest mark = %#x, want 0x4c", got)
	}
}
