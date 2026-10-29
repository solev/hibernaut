package machines

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/room"
)

type pauseOnRenew struct {
	engine.Engine
	paused bool
}

func (e *pauseOnRenew) SetTimeout(ctx context.Context, id string, d time.Duration) error {
	if !e.paused {
		e.paused = true
		if err := e.Engine.Pause(ctx, id); err != nil {
			return err
		}
		return engine.ErrNotRunning
	}
	return e.Engine.SetTimeout(ctx, id, d)
}

func TestDialWaitsForRoomOnlyWhenItCanResume(t *testing.T) {
	for _, tc := range []struct {
		name        string
		state       engine.State
		missingPin  bool
		renewPaused bool
	}{
		{"running", engine.Running, false, false},
		{"running repin", engine.Running, true, false},
		{"paused", engine.Paused, false, false},
		{"paused repin", engine.Paused, true, false},
		{"paused during renew", engine.Running, false, true},
		{"paused during renew repin", engine.Running, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, e := sharedStateManager(t)
			e.Add(engine.Machine{ID: "target", State: tc.state, MemoryMB: 2048,
				Metadata: map[string]string{labelName: "target", labelIdle: DefaultIdle.String()}})
			if err := m.checkState(); err != nil {
				t.Fatal(err)
			}
			if !tc.missingPin {
				if err := m.writePin("target", mustReadPlacementKey(t, m)); err != nil {
					t.Fatal(err)
				}
			}
			reads, lists := 0, 0
			m.readRoom = func() (room.Host, error) {
				reads++
				return placementHost(8192), nil
			}
			var wrapped engine.Engine = &placementEngine{Engine: e,
				list: func(ctx context.Context, opts engine.ListOptions) ([]engine.Machine, error) {
					if len(opts.States) > 0 {
						lists++
					}
					return e.List(ctx, opts)
				},
			}
			if tc.renewPaused {
				wrapped = &pauseOnRenew{Engine: wrapped}
			}
			m.e = &refusingDial{Engine: wrapped}
			unlock, err := m.lockRoom(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			release := sync.OnceFunc(unlock)
			defer release()
			ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
			defer cancel()
			conn, err := m.Dial(ctx, "target")
			if conn != nil {
				conn.Close()
			}
			if tc.state == engine.Running && !tc.renewPaused {
				if err != nil {
					t.Fatalf("running Dial waited for room.lock: %v", err)
				}
				if reads != 0 || lists != 0 {
					t.Fatalf("running Dial took %d room readings and %d room lists, want neither", reads, lists)
				}
				return
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("paused Dial = %v, want the deadline behind room.lock", err)
			}
			if em, err := e.Get(t.Context(), "target"); err != nil || em.State != engine.Paused {
				t.Fatalf("blocked Dial left target %s, %v, want paused", em.State, err)
			}
			release()
			ctx, stop := context.WithTimeout(t.Context(), time.Second)
			defer stop()
			conn, err = m.Dial(ctx, "target")
			if err != nil {
				t.Fatalf("Dial after releasing room.lock = %v", err)
			}
			conn.Close()
			if em, err := e.Get(ctx, "target"); err != nil || em.State != engine.Running {
				t.Fatalf("admitted Dial left target %s, %v, want running", em.State, err)
			}
		})
	}
}
