package hibd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// TestSetupDeliveryWritesAgainAfterAPush: a push writes machines behind the daemon's back, so
// what the daemon last wrote no longer says what a machine holds. hib rm doesn't push. It leaves
// the removed machine's line to the next pass, which has to write the block again even though
// it is back to what the daemon itself last wrote.
func TestSetupDeliveryWritesAgainAfterAPush(t *testing.T) {
	e, a, g, ctl, out, errOut := portApp(t)
	a.linkParts.ctl = ctl
	if code := run(t, a, out, errOut, "port", "add", "web", "8080"); code != 0 {
		t.Fatalf("port add web: exit %d: %s", code, errOut)
	}
	d := newTestDelivery(t, a, e)
	d.look(t.Context())
	if code := run(t, a, out, errOut, "new", "tmp"); code != 0 {
		t.Fatalf("new tmp: exit %d: %s", code, errOut)
	}
	if code := run(t, a, out, errOut, "port", "add", "tmp", "8080"); code != 0 {
		t.Fatalf("port add tmp: exit %d: %s", code, errOut)
	}
	api := mustID(t, e, "api")
	if got := g.hostsOf(api); !strings.Contains(got, " tmp.hib\n") {
		t.Fatalf("after port add tmp, api's /etc/hosts = %q, want it to name tmp.hib", got)
	}
	if code := run(t, a, out, errOut, "rm", "tmp"); code != 0 {
		t.Fatalf("rm tmp: exit %d: %s", code, errOut)
	}
	d.look(t.Context())
	if got := g.hostsOf(api); strings.Contains(got, "tmp.hib") || !strings.Contains(got, " web.hib\n") {
		t.Errorf("after rm tmp and a delivery pass, api's /etc/hosts = %q, want web.hib and not tmp.hib", got)
	}
}

// TestPushSetupMarksBeforeAndAfterItsWrites: the daemon has to see a push that is cut short as
// well as one that overlaps its pass, so the mark changes before the first write and again after
// the last.
func TestPushSetupMarksBeforeAndAfterItsWrites(t *testing.T) {
	guest := &secretGuest{env: []byte("PATH=/usr/bin\n")}
	var stateDir string
	var during []string
	e := fake.New(fake.WithExec(func(ctx context.Context, m engine.Machine, token string, req engine.ExecRequest) (engine.ExecResult, error) {
		if req.Cmd == "cat" {
			mark, err := readPushMark(stateDir)
			if err != nil {
				t.Error(err)
			}
			during = append(during, mark)
		}
		return guest.exec(ctx, m, token, req)
	}))
	e.Add(engine.Machine{ID: "app1", State: engine.Running, StartedAt: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC),
		Metadata: map[string]string{"hib.name": "app"}})
	a, _, errOut := newApp(t, e)
	withSecretConfig(t, a)
	cfg, err := loadConfig(a.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	stateDir = cfg.stateDir
	before, err := readPushMark(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	a.pushSetup(t.Context(), cfg, "setup saved")
	after, err := readPushMark(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(during) != 1 || during[0] == before || after == during[0] {
		t.Errorf("the mark was %q before the push, %q during its write and %q after it, want a new one each time", before, during, after)
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr %q, want nothing", errOut)
	}
}

// TestPushSetupWarnsOnceWhenItCantMark: a push that can't mark still writes the machines, and
// says once that the daemon may not write them again.
func TestPushSetupWarnsOnceWhenItCantMark(t *testing.T) {
	guest := &secretGuest{env: []byte("PATH=/usr/bin\n")}
	e := runningApp(guest)
	a, _, errOut := newApp(t, e)
	withSecretConfig(t, a)
	saveTestProfile(t, a, "The Octocat")
	cfg, err := loadConfig(a.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(cfg.stateDir, pushMark), 0o700); err != nil {
		t.Fatal(err)
	}
	a.pushSetup(t.Context(), cfg, "setup saved")
	if got := errOut.String(); strings.Count(got, "\n") != 1 || !strings.HasPrefix(got, "warning: setup saved, but telling hibd daemon about the update failed: ") {
		t.Errorf("stderr %q, want one warning that hibd daemon wasn't told", got)
	}
	if guest.identity() == nil {
		t.Error("the push didn't write the machine")
	}
}

// TestSetupDeliveryWritesEveryPassWhileTheMarkIsUnreadable: a mark the daemon can't read may
// hide a push, so each pass writes every machine again until it can, and says so once.
func TestSetupDeliveryWritesEveryPassWhileTheMarkIsUnreadable(t *testing.T) {
	guest := &secretGuest{env: []byte("PATH=/usr/bin\n")}
	e := runningApp(guest)
	a, _, _ := newApp(t, e)
	withSecretConfig(t, a)
	cfg, err := loadConfig(a.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	mark := filepath.Join(cfg.stateDir, pushMark)
	if err := os.Mkdir(mark, 0o700); err != nil {
		t.Fatal(err)
	}
	var logs []string
	d := newSetupDelivery(e, cfg, a.codexStore(cfg, nil), func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	})
	d.look(t.Context())
	requests := len(guest.allRequests())
	d.look(t.Context())
	if len(guest.allRequests()) == requests {
		t.Error("a pass that couldn't read the mark skipped the machine")
	}
	if len(logs) != 1 || !strings.HasPrefix(logs[0], "reading the push mark: ") {
		t.Errorf("logs = %q, want the unreadable mark once", logs)
	}
	if err := os.Remove(mark); err != nil {
		t.Fatal(err)
	}
	d.look(t.Context())
	requests = len(guest.allRequests())
	d.look(t.Context())
	if len(guest.allRequests()) != requests {
		t.Error("the setup was written again after the mark could be read")
	}
}
