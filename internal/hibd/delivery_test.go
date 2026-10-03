package hibd

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
)

func TestBlockDeliveryManyRunningMachinesRace(t *testing.T) {
	var mu sync.Mutex
	envs := map[string][]byte{}
	e := fake.New(fake.WithExec(func(_ context.Context, m engine.Machine, _ string, req engine.ExecRequest) (engine.ExecResult, error) {
		mu.Lock()
		defer mu.Unlock()
		switch req.Cmd {
		case "cat":
			return engine.ExecResult{Stdout: append([]byte(nil), envs[m.ID]...)}, nil
		case "sh":
			return engine.ExecResult{}, nil
		}
		return engine.ExecResult{ExitCode: 127}, nil
	}))
	for range 200 {
		id := fake.NewID()
		envs[id] = nil
		e.Add(engine.Machine{ID: id, State: engine.Running, StartedAt: time.Now(),
			Metadata: map[string]string{"hib.name": "app-" + id, "hib.image": "dev"}})
	}
	d := newBlockDelivery(e, t.TempDir()+"/missing-vault.json", func(string, ...any) {})
	for range 20 {
		d.look(t.Context())
	}
}
