package hibd

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
)

func TestBlockDeliveryManyRunningMachinesRace(t *testing.T) {
	guests := map[string]*secretGuest{}
	e := fake.New(fake.WithExec(func(ctx context.Context, m engine.Machine, token string, req engine.ExecRequest) (engine.ExecResult, error) {
		return guests[m.ID].exec(ctx, m, token, req)
	}))
	for i := range 200 {
		id := fmt.Sprintf("app-%d", i)
		guests[id] = &secretGuest{env: []byte("PATH=/usr/bin\n")}
		e.Add(engine.Machine{ID: id, State: engine.Running, StartedAt: time.Now(),
			Metadata: map[string]string{"hib.name": id, "hib.image": "dev", "hib.idle": "0s"}})
	}
	a, _, _ := newApp(t, e)
	withSecretConfig(t, a)
	secret := storeTestSecret(t, a, "github", "GH_TOKEN", "github.com", "gho_realSecretValue1234567890")
	want := "PATH=/usr/bin\n# BEGIN hibernaut secrets: hibd manages this block and replaces edits\n" +
		"GH_TOKEN=" + secret.Placeholder + "\n# END hibernaut secrets\n"
	logs, stop := startDaemon(t, a)
	defer stop()
	waitFor(t, logs, "all running machines receive the secret block", func() bool {
		for _, guest := range guests {
			if guest.envString() != want {
				return false
			}
		}
		return true
	})
	for id, guest := range guests {
		if got := guest.envString(); got != want {
			t.Errorf("%s /etc/environment = %q, want %q", id, got, want)
		}
	}
}
