package taskrun

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/taskguest/guestsim"
)

func TestPublicationRetriesLostReply(t *testing.T) {
	for _, always := range []bool{false, true} {
		t.Run(fmt.Sprint("always=", always), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := bubbleHarness(t, "")
				h.sim.Script = script(0, 0)
				lost := false
				h.sim.Lose = func(_ string, argv []string) bool {
					if len(argv) > 3 && argv[3] == "hib-publication" && (always || !lost) {
						lost = true
						return true
					}
					return false
				}
				id := h.submit(10 * time.Minute)
				bubbleUntil(t, h, task.CollectLimit+DefaultPoll, h.ended(id))
				h.stop()
				r := h.run(id)
				if !always {
					if outcome(r) != task.Succeeded || r.Publication() == nil || len(unreadResult(t, r)) != 0 {
						t.Fatalf("lost publication reply failed good run: status=%+v unread=%q",
							r.Status(""), unreadResult(t, r))
					}
				} else if outcome(r) != task.Failed ||
					!strings.Contains(strings.Join(unreadResult(t, r), ""), engine.ErrConnectionLost.Error()) {
					t.Fatalf("lost publication replies hid their cause: status=%+v unread=%q",
						r.Status(""), unreadResult(t, r))
				}
			})
		})
	}
}

func TestFailedFinishCollectsPublication(t *testing.T) {
	for _, present := range []bool{false, true} {
		t.Run(fmt.Sprint("present=", present), func(t *testing.T) {
			h := newHarness(t, func(id task.RunID, s task.Stage) guestsim.Behavior {
				b := script(0, 0)(id, s)
				if s == task.StageFinish {
					b.Status = 1
					b.Publish.Pushed = false
					if !present {
						b.Publish = nil
					}
				}
				return b
			})
			calls := 0
			h.configure = func(cfg *Config) {
				cfg.Publisher = publisherFunc(func(_ context.Context, _ task.Run, p task.Publication, _ string) (task.Publication, error) {
					calls++
					return p, nil
				})
			}
			h.start()
			id := h.submit(time.Hour)
			h.until(30, h.ended(id))
			h.stop()
			r := h.run(id)
			o, stage := r.Outcome()
			p := r.Publication()
			if calls != 0 || o != task.Failed || stage != task.StageFinish || (p != nil) != present ||
				(p != nil && (p.Pushed || p.Commit != "c0ffee0000000000000000000000000000000000")) ||
				len(unreadResult(t, r)) != 0 {
				t.Fatalf("refused push status=%+v publication=%+v unread=%q",
					r.Status(""), p, unreadResult(t, r))
			}
		})
	}
}

type publicationReadError struct{ Engine }

func (e publicationReadError) Exec(ctx context.Context, id string, req engine.ExecRequest) (engine.ExecResult, error) {
	if len(req.Args) > 2 && req.Args[2] == "hib-publication" {
		return engine.ExecResult{ExitCode: 1, Stderr: []byte("cat: permission denied")}, nil
	}
	return e.Engine.Exec(ctx, id, req)
}

func TestFailedFinishPublicationReadError(t *testing.T) {
	h := newHarness(t, func(id task.RunID, s task.Stage) guestsim.Behavior {
		b := script(0, 0)(id, s)
		if s == task.StageFinish {
			b.Status = 1
		}
		return b
	})
	calls := 0
	h.configure = func(cfg *Config) {
		cfg.Engine = publicationReadError{Engine: cfg.Engine}
		cfg.Publisher = publisherFunc(func(_ context.Context, _ task.Run, p task.Publication, _ string) (task.Publication, error) {
			calls++
			return p, nil
		})
	}
	h.start()
	id := h.submit(time.Hour)
	h.until(30, h.ended(id))
	h.stop()
	r := h.run(id)
	want := []string{"publication: read exited 1: cat: permission denied"}
	if st := r.Status(""); st.Outcome != task.Failed || st.Stage != task.StageFinish ||
		calls != 0 || !slices.Equal(unreadResult(t, r), want) {
		t.Fatalf("failed finish hid publication read error: status=%+v calls=%d unread=%q",
			st, calls, unreadResult(t, r))
	}
}

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
					want = task.Failed
					unread = []string{"publication: read exited 66"}
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
