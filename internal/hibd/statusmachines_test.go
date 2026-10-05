package hibd

import (
	"context"
	"errors"
	"testing"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
)

type statusEngine struct {
	engine.Engine
	listErr error
}

// List returns the API failure configured by the test.
func (e statusEngine) List(ctx context.Context, opts engine.ListOptions) ([]engine.Machine, error) {
	if e.listErr != nil {
		return nil, e.listErr
	}
	return e.Engine.List(ctx, opts)
}

func TestStatusCountsMachinesOnlyWhenE2BAnswers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		listErr error
	}{
		{"answered", nil},
		{"unreachable", errors.New("E2B unreachable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, out, errOut, _ := newStatusApp(t)
			e := fake.New()
			e.Add(engine.Machine{ID: "app-id", State: engine.Running})
			e.Add(engine.Machine{ID: "old-id", State: engine.Paused})
			a.NewEngine = func() (engine.Engine, error) {
				return statusEngine{Engine: e, listErr: tc.listErr}, nil
			}
			code, reply, err := runJSON(t, a, out, "status", "-json")
			if err != nil || reply.Status == nil {
				t.Fatalf("exit %d, reply %+v, %v, stderr %s", code, reply, err, errOut)
			}
			if tc.listErr == nil {
				if reply.Status.Machines == nil || *reply.Status.Machines != 2 {
					t.Fatalf("machine count %+v, want 2", reply.Status.Machines)
				}
			} else if reply.Status.Machines != nil {
				t.Fatalf("count sent when listing failed: %+v", reply.Status.Machines)
			}
		})
	}
}
