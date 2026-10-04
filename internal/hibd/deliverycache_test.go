package hibd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
)

type failedSetupList struct{ engine.Engine }

func (e failedSetupList) List(context.Context, engine.ListOptions) ([]engine.Machine, error) {
	return nil, errors.New("test: listing unavailable")
}

func TestPushSetupWarnsWithoutWritingWhenLoadingOrListingFails(t *testing.T) {
	for _, stage := range []string{"setup", "engine", "list"} {
		t.Run(stage, func(t *testing.T) {
			guest := &secretGuest{env: []byte("PATH=/usr/bin\n")}
			e := runningApp(guest)
			a, _, errOut := newApp(t, e)
			withSecretConfig(t, a)
			cfg, err := loadConfig(a.Getenv)
			if err != nil {
				t.Fatal(err)
			}
			doing := "listing machines"
			switch stage {
			case "setup":
				doing = "loading the machine setup"
				if err := os.WriteFile(cfg.githubProfile, []byte("{"), 0o644); err != nil {
					t.Fatal(err)
				}
				a.NewEngine = func() (engine.Engine, error) {
					t.Error("opened the engine before loading the setup")
					return e, nil
				}
			case "engine":
				a.NewEngine = func() (engine.Engine, error) { return nil, errors.New("test: engine unavailable") }
			case "list":
				a.NewEngine = func() (engine.Engine, error) { return failedSetupList{e}, nil }
			}
			a.pushSetup(t.Context(), cfg, "setup saved")
			if !strings.HasPrefix(errOut.String(), "warning: setup saved, but "+doing+" failed: ") {
				t.Errorf("missing the warning for %s", stage)
			}
			if len(guest.allRequests()) != 0 {
				t.Error("pushed setup after loading or listing failed")
			}
		})
	}
}

func TestPushSetupKeepsImmediatePushesUncachedAndOrdered(t *testing.T) {
	guest := &secretGuest{env: []byte("PATH=/usr/bin\n")}
	var visits []string
	e := fake.New(fake.WithExec(func(ctx context.Context, m engine.Machine, token string, req engine.ExecRequest) (engine.ExecResult, error) {
		visits = append(visits, m.ID)
		return guest.exec(ctx, m, token, req)
	}))
	started := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	for _, m := range []engine.Machine{
		{ID: "older", State: engine.Running, StartedAt: started, Metadata: map[string]string{"hib.name": "older"}},
		{ID: "newer", State: engine.Running, StartedAt: started.Add(time.Minute), Metadata: map[string]string{"hib.name": "newer"}},
		{ID: "paused", State: engine.Paused, Metadata: map[string]string{"hib.name": "paused"}},
		{ID: "unmanaged", State: engine.Running},
	} {
		e.Add(m)
	}
	a, _, errOut := newApp(t, e)
	withSecretConfig(t, a)
	cfg, err := loadConfig(a.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	a.pushSetup(t.Context(), cfg, "setup saved")
	a.pushSetup(t.Context(), cfg, "setup saved")
	if !slices.Equal(visits, []string{"newer", "older", "newer", "older"}) {
		t.Errorf("immediate pushes visited %q, want each running managed machine in listing order twice", visits)
	}
	if errOut.Len() != 0 {
		t.Fatal("successful immediate pushes printed a warning")
	}
}

func TestSetupDeliveryConcurrentLookDoesNotSkipChanges(t *testing.T) {
	for _, change := range []string{"content", "run"} {
		t.Run(change, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				guest := &secretGuest{env: []byte("PATH=/usr/bin\n")}
				release := make(chan struct{})
				var reads atomic.Int32
				e := fake.New(fake.WithExec(func(ctx context.Context, m engine.Machine, token string, req engine.ExecRequest) (engine.ExecResult, error) {
					if req.Cmd == "cat" {
						reads.Add(1)
						<-release
					}
					return guest.exec(ctx, m, token, req)
				}))
				m := engine.Machine{ID: "app1", State: engine.Running, StartedAt: time.Now(),
					Metadata: map[string]string{"hib.name": "app"}}
				e.Add(m)
				a, _, _ := newApp(t, e)
				withSecretConfig(t, a)
				saveTestProfile(t, a, "The Octocat")
				d := newTestDelivery(t, a, e)
				var wg sync.WaitGroup
				wg.Go(func() { d.look(t.Context()) })
				synctest.Wait()
				wantName := "The Octocat"
				if change == "content" {
					wantName = "Mona Lisa Octocat"
					saveTestProfile(t, a, wantName)
				} else {
					m.StartedAt = m.StartedAt.Add(time.Minute)
					e.Add(m)
				}
				wg.Go(func() { d.look(t.Context()) })
				synctest.Wait()
				close(release)
				wg.Wait()
				if n := reads.Load(); n != 2 {
					t.Errorf("delivered %d setups after a %s change, want 2", n, change)
				}
				if id := guest.identity(); id == nil || id.Name != wantName {
					t.Error("the machine did not receive the latest identity")
				}
				requests := len(guest.allRequests())
				d.look(t.Context())
				if len(guest.allRequests()) != requests {
					t.Error("the latest successful delivery was not cached")
				}
			})
		})
	}
}

func TestSetupDeliveryConcurrentLooksDoNotDoublePush(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		guest := &secretGuest{env: []byte("PATH=/usr/bin\n")}
		release := make(chan struct{})
		var reads atomic.Int32
		e := fake.New(fake.WithExec(func(ctx context.Context, m engine.Machine, token string, req engine.ExecRequest) (engine.ExecResult, error) {
			if req.Cmd == "cat" {
				reads.Add(1)
				<-release
			}
			return guest.exec(ctx, m, token, req)
		}))
		e.Add(engine.Machine{ID: "app1", State: engine.Running, StartedAt: time.Now(),
			Metadata: map[string]string{"hib.name": "app"}})
		a, _, _ := newApp(t, e)
		withSecretConfig(t, a)
		saveTestProfile(t, a, "The Octocat")
		d := newTestDelivery(t, a, e)
		var wg sync.WaitGroup
		wg.Go(func() { d.look(t.Context()) })
		synctest.Wait()
		wg.Go(func() { d.look(t.Context()) })
		synctest.Wait()
		if n := reads.Load(); n != 1 {
			t.Errorf("concurrent looks started %d setup writes, want 1", n)
		}
		close(release)
		wg.Wait()
		if n := reads.Load(); n != 1 {
			t.Errorf("concurrent looks completed %d setup writes, want 1", n)
		}
	})
}

func TestSetupDeliveryDetectsACodexOnlyChange(t *testing.T) {
	guest := &secretGuest{env: []byte("PATH=/usr/bin\n")}
	e := runningApp(guest)
	a, _, _ := newApp(t, e)
	withSecretConfig(t, a)
	d := newTestDelivery(t, a, e)
	d.look(t.Context())
	requests := len(guest.allRequests())
	d.look(t.Context())
	if len(guest.allRequests()) != requests {
		t.Fatal("unchanged setup was delivered twice")
	}
	connectTestCodex(t, a, testCodexTokens())
	d.look(t.Context())
	if !bytes.Equal(guest.codexAuthFile(), codexLoginFile(t, a)) {
		t.Fatal("the changed setup did not deliver the Codex login")
	}
	requests = len(guest.allRequests())
	d.look(t.Context())
	if len(guest.allRequests()) != requests {
		t.Error("the changed setup was delivered twice")
	}
}

func TestSetupDeliveryForgetsMachinesMissingFromTheRunningList(t *testing.T) {
	for _, missing := range []string{"paused", "unmanaged"} {
		t.Run(missing, func(t *testing.T) {
			guest := &secretGuest{env: []byte("PATH=/usr/bin\n")}
			e := fake.New(fake.WithExec(guest.exec))
			m := engine.Machine{ID: "app1", State: engine.Running, StartedAt: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC),
				Metadata: map[string]string{"hib.name": "app"}}
			e.Add(m)
			a, _, _ := newApp(t, e)
			withSecretConfig(t, a)
			saveTestProfile(t, a, "The Octocat")
			d := newTestDelivery(t, a, e)
			d.look(t.Context())
			guest.change(func(g *secretGuest) { g.git = nil })
			absent := m
			if missing == "paused" {
				absent.State = engine.Paused
			} else {
				absent.Metadata = nil
			}
			e.Add(absent)
			requests := len(guest.allRequests())
			d.look(t.Context())
			if len(guest.allRequests()) != requests {
				t.Fatal("setup was pushed into a paused or unmanaged machine")
			}
			e.Add(m)
			d.look(t.Context())
			if guest.identity() == nil {
				t.Fatal("the machine's missing setup was not delivered after it returned")
			}
		})
	}
}

func TestSetupDeliveryRetriesFailedAndPartialWrites(t *testing.T) {
	for _, stage := range []string{"environment", "git", "codex"} {
		t.Run(stage, func(t *testing.T) {
			guest := &secretGuest{env: []byte("PATH=/usr/bin\n")}
			var broken atomic.Bool
			broken.Store(true)
			e := fake.New(fake.WithExec(func(ctx context.Context, m engine.Machine, token string, req engine.ExecRequest) (engine.ExecResult, error) {
				atStage := stage == "environment" && req.Cmd == "cat"
				if req.Cmd == "sh" && len(req.Args) >= 3 {
					atStage = req.Args[2] == "hibd-"+stage
				}
				if broken.Load() && atStage {
					return engine.ExecResult{}, errors.New("test: machine write unavailable")
				}
				return guest.exec(ctx, m, token, req)
			}))
			e.Add(engine.Machine{ID: "app1", State: engine.Running, StartedAt: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC),
				Metadata: map[string]string{"hib.name": "app"}})
			a, _, _ := newApp(t, e)
			withSecretConfig(t, a)
			secret := storeTestSecret(t, a, "github", "GH_TOKEN", "github.com", "gho_realSecretValue1234567890")
			saveTestProfile(t, a, "The Octocat")
			connectTestCodex(t, a, testCodexTokens())
			cfg, err := loadConfig(a.Getenv)
			if err != nil {
				t.Fatal(err)
			}
			reports := 0
			d := newSetupDelivery(e, cfg, a.codexStore(cfg, nil), func(string, ...any) { reports++ })
			d.look(t.Context())
			d.look(t.Context())
			if reports != 1 {
				t.Errorf("reported the repeated failure %d times, want once", reports)
			}
			broken.Store(false)
			d.look(t.Context())
			if !strings.Contains(guest.envString(), secret.Placeholder) || guest.identity() == nil || len(guest.codexAuthFile()) == 0 {
				t.Fatal("the retry did not deliver the complete setup")
			}
			requests := len(guest.allRequests())
			d.look(t.Context())
			if len(guest.allRequests()) != requests {
				t.Error("the complete successful setup was delivered again")
			}
		})
	}
}
