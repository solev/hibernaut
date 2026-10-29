package machines

import (
	"context"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/room"
	"github.com/solev/hibernaut/internal/statefile"
)

func TestCopyPlacementRoomBoundaries(t *testing.T) {
	for _, op := range []string{"save", "fork"} {
		t.Run(op, func(t *testing.T) {
			m, e := sharedStateManager(t)
			if _, err := m.Create(t.Context(), "origin", "dev", DefaultIdle); err != nil {
				t.Fatal(err)
			}
			originalRead := m.readRoom
			readLocked := false
			m.readRoom = func() (room.Host, error) {
				readLocked = placementIsLocked(t, m.dir)
				return originalRead()
			}
			saves, creates := 0, 0
			m.e = &placementEngine{Engine: e,
				save: func(ctx context.Context, id, alias string) (engine.TemplateInfo, error) {
					saves++
					if placementIsLocked(t, m.dir) {
						t.Error("room.lock held across SaveTemplate")
					}
					if !readLocked {
						t.Error("save copy used a room reading from outside the admission lock")
					}
					return e.SaveTemplate(ctx, id, alias)
				},
				create: func(ctx context.Context, o engine.CreateOptions) (engine.Machine, error) {
					creates++
					assertPlacementLocked(t, m.dir)
					if !readLocked {
						t.Error("fork copy used a room reading from outside the admission lock")
					}
					return e.Create(ctx, o)
				},
				exec: func(ctx context.Context, id string, req engine.ExecRequest) (engine.ExecResult, error) {
					if placementIsLocked(t, m.dir) {
						t.Error("room.lock held during copy setup")
					}
					return e.Exec(ctx, id, req)
				},
			}
			m.durable = func(context.Context, string) error {
				if placementIsLocked(t, m.dir) {
					t.Error("room.lock held during snapshot preparation")
				}
				return nil
			}
			if op == "save" {
				if _, err := m.SaveImage(t.Context(), "origin", "saved"); err != nil {
					t.Fatal(err)
				}
			} else if _, err := m.Fork(t.Context(), "origin", 1); err != nil {
				t.Fatal(err)
			}
			if saves != 1 || creates != map[string]int{"save": 0, "fork": 1}[op] {
				t.Fatalf("made %d snapshots and %d copies", saves, creates)
			}
		})
	}
}

func TestWakeDoesNotWaitForSaveTemplate(t *testing.T) {
	for _, op := range []string{"save", "fork"} {
		t.Run(op, func(t *testing.T) {
			m, e := sharedStateManager(t)
			if _, err := m.Create(t.Context(), "origin", "dev", DefaultIdle); err != nil {
				t.Fatal(err)
			}
			e.Add(engine.Machine{ID: "target", State: engine.Paused, MemoryMB: 1024, CPUs: 1,
				Metadata: map[string]string{labelName: "target", labelIdle: DefaultIdle.String()}})
			started, release := make(chan struct{}), make(chan struct{})
			m.e = &placementEngine{Engine: e, save: func(ctx context.Context, id, alias string) (engine.TemplateInfo, error) {
				close(started)
				<-release
				return e.SaveTemplate(ctx, id, alias)
			}}
			saved := make(chan error, 1)
			go func() {
				var err error
				if op == "save" {
					_, err = m.SaveImage(t.Context(), "origin", "saved")
				} else {
					_, err = m.Fork(t.Context(), "origin", 1)
				}
				saved <- err
			}()
			select {
			case <-started:
			case err := <-saved:
				t.Fatalf("save did not reach SaveTemplate: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("save never reached SaveTemplate")
			}
			wake := New(Config{Engine: e, StateDir: m.dir, ReadRoom: m.readRoom})
			ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
			defer cancel()
			em, wakeErr := wake.Wake(ctx, "target", time.Now().Add(time.Hour))
			state, readErr := e.Get(t.Context(), "target")
			close(release)
			saveErr := <-saved
			if wakeErr != nil || em.ID != "target" || readErr != nil || state.State != engine.Running {
				t.Fatalf("wake while SaveTemplate is held = %s, %v, state %s, %v",
					em.ID, wakeErr, state.State, readErr)
			}
			if saveErr != nil {
				t.Fatalf("%s failed after releasing SaveTemplate: %v", op, saveErr)
			}
		})
	}
}

func placementIsLocked(t *testing.T, dir string) bool {
	t.Helper()
	unlock, ok, err := (statefile.Dir{Path: dir}).TryLock(RoomLock, "test admission")
	if err != nil {
		t.Fatal(err)
	}
	if unlock != nil {
		unlock()
	}
	return !ok
}
