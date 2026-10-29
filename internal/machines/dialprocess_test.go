package machines

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/room"
)

func TestDialFromFreshProcess(t *testing.T) {
	if scenario := os.Getenv("HIB_DIAL_SCENARIO"); scenario != "" {
		m, e := sharedStateManager(t)
		m.dir, m.keysFile = os.Getenv("HIB_DIAL_DIR"), os.Getenv("HIB_DIAL_KEYS")
		state := engine.Running
		if scenario == "paused repin" {
			state = engine.Paused
		}
		e.Add(engine.Machine{ID: "target", State: state, MemoryMB: 2048,
			EndAt:    time.Now().Add(2 * DefaultIdle),
			Metadata: map[string]string{labelName: "target", labelIdle: DefaultIdle.String()}})
		checks := 0
		m.readRoom = func() (room.Host, error) {
			assertPlacementLocked(t, m.dir)
			if strings.Contains(scenario, "full") {
				return placementHost(0), nil
			}
			return placementHost(8192), nil
		}
		d := &refusingDial{Engine: e}
		m.e = &placementEngine{Engine: d, list: func(ctx context.Context, opts engine.ListOptions) ([]engine.Machine, error) {
			if len(opts.States) > 0 {
				checks++
			}
			return e.List(ctx, opts)
		}}
		if strings.HasPrefix(scenario, "stale") {
			m.e = &pauseAfterDialRead{Engine: m.e}
		}
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		c, err := m.Dial(ctx, "target")
		if c != nil {
			c.Close()
		}
		wantState := engine.Running
		if strings.Contains(scenario, "full") {
			wantState = engine.Paused
			if !errors.Is(err, room.ErrFull) || d.dials != 0 {
				t.Fatalf("fresh process full dial = %v, resumable dials=%d", err, d.dials)
			}
		} else if err != nil || c == nil {
			t.Fatalf("fresh process dial = %v, want a connection", err)
		}
		wantChecks := 1
		if strings.HasPrefix(scenario, "running") {
			wantChecks = 0
		}
		if checks != wantChecks {
			t.Fatalf("fresh process took %d room checks, want %d", checks, wantChecks)
		}
		if em, err := e.Get(ctx, "target"); err != nil || em.State != wantState {
			t.Fatalf("fresh process left machine %s, %v, want %s", em.State, err, wantState)
		}
		return
	}
	for _, scenario := range []string{"stale pinned full", "stale repin full", "stale pinned room",
		"stale repin room", "running", "running repin", "paused repin"} {
		t.Run(scenario, func(t *testing.T) {
			m, _ := sharedStateManager(t)
			if err := m.checkState(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(scenario, "repin") {
				if err := m.writePin("target", mustReadPlacementKey(t, m)); err != nil {
					t.Fatal(err)
				}
			}
			if strings.HasPrefix(scenario, "running") {
				unlock, err := m.lockRoom(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer unlock()
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDialFromFreshProcess$", "-test.v")
			cmd.Env = append(os.Environ(), "HIB_DIAL_SCENARIO="+scenario, "HIB_DIAL_DIR="+m.dir, "HIB_DIAL_KEYS="+m.keysFile)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("fresh bridge process: %v\n%s", err, output)
			}
		})
	}
}
