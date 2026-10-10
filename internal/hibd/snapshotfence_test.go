package hibd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/links"
)

// snapshotEngine logs each pause, resume and save into f before it happens, and fails every
// pause with pauseErr when that's set.
type snapshotEngine struct {
	engine.Engine
	f        *fakeLinks
	pauseErr error
}

func (e snapshotEngine) Pause(ctx context.Context, id string) error {
	e.f.event("pause %s", id)
	if e.pauseErr != nil {
		return e.pauseErr
	}
	return e.Engine.Pause(ctx, id)
}

func (e snapshotEngine) Resume(ctx context.Context, id string, timeout time.Duration) (engine.Machine, error) {
	e.f.event("resume %s", id)
	return e.Engine.Resume(ctx, id, timeout)
}

func (e snapshotEngine) SaveTemplate(ctx context.Context, id, alias string) (engine.TemplateInfo, error) {
	e.f.event("save %s", id)
	return e.Engine.SaveTemplate(ctx, id, alias)
}

// snapshotApp is imageApp with a running machine web, made with links off, and then f's links
// pieces, turned on when on is set. It returns web's ID.
func snapshotApp(t *testing.T, f *fakeLinks, on bool) (*App, *fake.Engine, *bytes.Buffer, string) {
	t.Helper()
	a, e, out, errOut := imageApp(t)
	code, r, err := runJSON(t, a, out, "new", "web", "-json")
	if code != 0 || err != nil || r.Machine == nil {
		t.Fatalf("new = %d, %v; stderr %s", code, err, errOut)
	}
	withLinks(a, f, on)
	a.NewEngine = func() (engine.Engine, error) { return snapshotEngine{Engine: e, f: f}, nil }
	return a, e, out, r.Machine.ID
}

func snapshotArgs(op string) []string {
	if op == "fork" {
		return []string{"fork", "web", "-json"}
	}
	return []string{"image", "save", "web", "myimg", "-json"}
}

// eventsOf is what f logged about machine id.
func eventsOf(f *fakeLinks, id string) []string {
	return slices.DeleteFunc(f.log(), func(event string) bool { return !strings.Contains(event, id) })
}

// leftAlone checks that machine id is running as it was, and that nothing was saved or made.
func leftAlone(t *testing.T, e *fake.Engine, id string) {
	t.Helper()
	if em, err := e.Get(t.Context(), id); err != nil || em.State != engine.Running {
		t.Errorf("origin = %+v, %v, want it running", em, err)
	}
	if saved, err := e.SavedTemplates(t.Context()); err != nil || len(saved) != 0 {
		t.Errorf("saved templates = %+v, %v, want none", saved, err)
	}
	if ms, err := e.List(t.Context(), engine.ListOptions{}); err != nil || len(ms) != 1 {
		t.Errorf("machines = %+v, %v, want only the origin", ms, err)
	}
}

func TestSaveAndForkFenceTheirRoundTripAndKeepTheMark(t *testing.T) {
	for _, op := range []string{"fork", "image save"} {
		for _, byHand := range []bool{false, true} {
			name := op + " of a running machine"
			if byHand {
				name = op + " of a machine paused by hand"
			}
			t.Run(name, func(t *testing.T) {
				f := &fakeLinks{}
				a, e, out, id := snapshotApp(t, f, true)
				state, steps := engine.Running, []string{"pause", "resume", "save"}
				if byHand {
					if code, r := runJSONReply(t, a, out, "pause", "web", "-json"); code != 0 {
						t.Fatalf("pause = %d, %s", code, r.Error)
					}
					if m, _ := f.entry(id); !m.PausedByHand {
						t.Fatalf("pause left no mark: %+v", m)
					}
					state, steps = engine.Paused, []string{"resume", "save", "pause"}
				}
				f.mu.Lock()
				f.events = nil
				f.mu.Unlock()
				if code, r := runJSONReply(t, a, out, snapshotArgs(op)...); code != 0 {
					t.Fatalf("%s = %d, %s", op, code, r.Error)
				}
				want := []string{fmt.Sprintf("fence %s %s", id, op), "close " + id}
				for _, step := range steps {
					want = append(want, step+" "+id)
				}
				want = append(want, "release "+id)
				if got := eventsOf(f, id); !slices.Equal(got, want) {
					t.Errorf("events = %q, want %q", got, want)
				}
				if m, _ := f.entry(id); m.PausedByHand != byHand || m.Fence != nil {
					t.Errorf("entry = %+v, want PausedByHand %v and no fence", m, byHand)
				}
				if em, err := e.Get(t.Context(), id); err != nil || em.State != state {
					t.Errorf("origin = %+v, %v, want %s", em, err, state)
				}
			})
		}
	}
}

func TestSaveAndForkLeaveTheMachineAsItWasWhenTheFenceFails(t *testing.T) {
	for _, op := range []string{"fork", "image save"} {
		t.Run(op, func(t *testing.T) {
			f := &fakeLinks{fenceErr: errors.New("the link state is unreadable")}
			a, e, out, id := snapshotApp(t, f, true)
			code, r := runJSONReply(t, a, out, snapshotArgs(op)...)
			want := "left web as it was: couldn't mark it as being paused in the link state: the link state is unreadable"
			if code != 1 || r.Error != want {
				t.Fatalf("%s = %d, %q, want %q", op, code, r.Error, want)
			}
			if got, want := eventsOf(f, id), []string{fmt.Sprintf("fence %s %s", id, op)}; !slices.Equal(got, want) {
				t.Errorf("events = %q, want %q", got, want)
			}
			leftAlone(t, e, id)
		})
	}
}

func TestSaveAndForkLiftTheFenceWhenTheirPauseFails(t *testing.T) {
	for _, op := range []string{"fork", "image save"} {
		for _, byHand := range []bool{false, true} {
			name := op + " of a running machine"
			if byHand {
				name = op + " of a machine paused by hand"
			}
			t.Run(name, func(t *testing.T) {
				f := &fakeLinks{}
				a, e, out, id := snapshotApp(t, f, true)
				steps, want := []string{"pause"}, "couldn't pause web before saving"
				if byHand {
					if code, r := runJSONReply(t, a, out, "pause", "web", "-json"); code != 0 {
						t.Fatalf("pause = %d, %s", code, r.Error)
					}
					steps, want = []string{"resume", "save", "pause"}, "couldn't pause web again"
				}
				f.mu.Lock()
				f.events = nil
				f.mu.Unlock()
				a.NewEngine = func() (engine.Engine, error) {
					return snapshotEngine{Engine: e, f: f, pauseErr: errors.New("e2b refused the pause")}, nil
				}
				if code, r := runJSONReply(t, a, out, snapshotArgs(op)...); code != 1 || !strings.Contains(r.Error, want) {
					t.Fatalf("%s = %d, %q, want 1 and %q", op, code, r.Error, want)
				}
				events := []string{fmt.Sprintf("fence %s %s", id, op), "close " + id}
				for _, step := range steps {
					events = append(events, step+" "+id)
				}
				events = append(events, "release "+id)
				if got := eventsOf(f, id); !slices.Equal(got, events) {
					t.Errorf("events = %q, want %q", got, events)
				}
				if m, _ := f.entry(id); m.Fence != nil || m.PausedByHand != byHand {
					t.Errorf("entry = %+v, want no fence and PausedByHand %v", m, byHand)
				}
			})
		}
	}
}

func TestSaveAndForkWithLinksOffArePlainRoundTrips(t *testing.T) {
	for _, op := range []string{"fork", "image save"} {
		t.Run(op, func(t *testing.T) {
			f := &fakeLinks{}
			a, _, out, id := snapshotApp(t, f, false)
			if code, r := runJSONReply(t, a, out, snapshotArgs(op)...); code != 0 {
				t.Fatalf("%s = %d, %s", op, code, r.Error)
			}
			want := []string{"pause " + id, "resume " + id, "save " + id}
			if got := f.log(); !slices.Equal(got, want) {
				t.Errorf("events = %q, want only the engine's %q", got, want)
			}
		})
	}
}

// A pause or rm of a machine holds its fence while it waits for hibd.lock, which a save or a
// fork of the machine holds while it waits for the fence. The save gives up first.
func TestSaveAndForkGiveUpOnAFenceHeldTooLong(t *testing.T) {
	wait := snapshotFenceWait
	snapshotFenceWait = 50 * time.Millisecond
	t.Cleanup(func() { snapshotFenceWait = wait })
	for _, op := range []string{"fork", "image save"} {
		t.Run(op, func(t *testing.T) {
			f := &fakeLinks{}
			a, e, out, id := snapshotApp(t, f, true)
			store := links.NewStore(a.Getenv("HIBD_STATE_DIR"))
			a.linkParts.store = store
			release, err := store.Fence(t.Context(), id, "pause")
			if err != nil {
				t.Fatal(err)
			}
			code, r := runJSONReply(t, a, out, snapshotArgs(op)...)
			want := fmt.Sprintf("left web as it was: raising its link fence and closing its links took longer than 50ms. A pause or rm of web may be waiting for this %s: try the %s again once that is done", op, op)
			if code != 1 || r.Error != want {
				t.Fatalf("%s = %d, %q, want %q", op, code, r.Error, want)
			}
			if got := f.log(); len(got) != 0 {
				t.Errorf("events = %q, want none", got)
			}
			leftAlone(t, e, id)
			release()
			if code, r := runJSONReply(t, a, out, snapshotArgs(op)...); code != 0 {
				t.Fatalf("%s once the fence is down = %d, %s", op, code, r.Error)
			}
			doc, err := store.Read()
			if err != nil {
				t.Fatal(err)
			}
			if m := doc.Machines[id]; m != nil && m.Fence != nil {
				t.Errorf("fence = %+v, want it down", m.Fence)
			}
		})
	}
}
