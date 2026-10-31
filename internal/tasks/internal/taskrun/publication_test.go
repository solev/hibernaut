package taskrun

import (
	"fmt"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/taskguest/guestsim"
)

func TestPublicationMissingAfterOKFinish(t *testing.T) {
	for _, publish := range []bool{true, false} {
		t.Run(fmt.Sprint("publish=", publish), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := bubbleHarness(t, "")
				h.sim.Script = func(id task.RunID, s task.Stage) guestsim.Behavior {
					b := script(0, 0)(id, s)
					if !publish {
						b.Publish = nil
					}
					return b
				}
				id := h.submit(10 * time.Minute)
				bubbleUntil(t, h, 30*time.Second, h.ended(id))
				h.stop()
				r := h.run(id)
				want := task.Succeeded
				var unread []string
				if !publish {
					want = task.NoChanges
					unread = []string{"publication: read exited 1"}
				}
				if got := unreadResult(t, r); !slices.Equal(got, unread) {
					t.Fatalf("unread publication=%q, want %q", got, unread)
				}
				if outcome(r) != want || r.Status("").Outcome != want ||
					(r.Publication() != nil) != publish ||
					r.Machine != task.MachinePaused || !r.Frozen() {
					t.Fatalf("publication run status=%+v publication=%+v machine=%s",
						r.Status(""), r.Publication(), r.Machine)
				}
				h.logs.check(t, nil)
				h.checkLog(id)
			})
		})
	}
}
