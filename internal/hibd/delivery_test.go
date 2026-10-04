package hibd

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/github"
	"github.com/solev/hibernaut/internal/machines"
)

// testClock is a clock that moves only when told to.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// change edits the machine's files, as someone working in it might.
func (g *secretGuest) change(edit func(g *secretGuest)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	edit(g)
}

// newTestDelivery returns the daemon's setup delivery for a and e. Any problem it logs fails
// the test.
func newTestDelivery(t *testing.T, a *App, e engine.Engine) *setupDelivery {
	t.Helper()
	cfg, err := loadConfig(a.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	return newSetupDelivery(e, cfg, a.codexStore(cfg, nil), func(format string, args ...any) {
		t.Errorf("setup: "+format, args...)
	})
}

func TestSetupDeliveryWritesTheSetupAgainAfterAResume(t *testing.T) {
	ctx := t.Context()
	clock := &testClock{now: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)}
	guest := &secretGuest{env: []byte("PATH=/usr/bin\n")}
	e := fake.New(fake.WithClock(clock.Now), fake.WithExec(guest.exec))
	e.Add(engine.Machine{ID: "app1", State: engine.Running, StartedAt: clock.Now(), EndAt: clock.Now().Add(time.Hour),
		Metadata: map[string]string{"hib.name": "app", "hib.image": "dev", "hib.idle": "10m0s"}})
	a, _, _ := newApp(t, e)
	withSecretConfig(t, a)
	secret := storeTestSecret(t, a, "github", "GH_TOKEN", "github.com", "gho_realSecretValue1234567890")
	saveTestProfile(t, a, "The Octocat")
	d := newTestDelivery(t, a, e)
	d.look(ctx)
	env, id := guest.envString(), guest.identity()
	if !strings.Contains(env, secret.Placeholder) || id == nil {
		t.Fatalf("the first look wrote /etc/environment %q and git's identity %+v, want the setup", env, id)
	}

	// Someone in the machine removes hibd's block and git's identity.
	guest.change(func(g *secretGuest) {
		g.env = []byte("PATH=/usr/bin\n")
		g.git = nil
	})
	requests := len(guest.allRequests())
	d.look(ctx)
	// Extending a running machine's timeout isn't a resume.
	clock.add(time.Minute)
	if err := e.SetTimeout(ctx, "app1", time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Resume(ctx, "app1", 2*time.Hour); err != nil {
		t.Fatal(err)
	}
	d.look(ctx)
	if n := len(guest.allRequests()) - requests; n != 0 {
		t.Errorf("looks without a resume ran %d commands in the machine, want none", n)
	}

	// A pause and a resume between two looks.
	clock.add(time.Minute)
	if err := e.Pause(ctx, "app1"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Resume(ctx, "app1", time.Hour); err != nil {
		t.Fatal(err)
	}
	d.look(ctx)
	if got := guest.envString(); got != env {
		t.Errorf("/etc/environment after a resume = %q, want %q", got, env)
	}
	if got := guest.identity(); got == nil || *got != *id {
		t.Errorf("git's identity after a resume = %+v, want %+v", got, id)
	}
	requests = len(guest.allRequests())
	d.look(ctx)
	if n := len(guest.allRequests()) - requests; n != 0 {
		t.Errorf("the look after the one that caught up with the resume ran %d commands in the machine, want none", n)
	}
}

func TestCodexLoginFileComesBackAfterLogoutAndAResume(t *testing.T) {
	ctx := t.Context()
	clock := &testClock{now: time.Now()}
	guest := &secretGuest{env: []byte("PATH=/usr/bin\n"), hostKey: newKey(t)}
	e := fake.New(fake.WithClock(clock.Now), fake.WithTemplates(images.Template("dev")), fake.WithExec(guest.exec),
		fake.WithDialer(func(context.Context, engine.Machine, int) (net.Conn, error) {
			c, _ := net.Pipe()
			return c, nil
		}))
	a, _, errOut := newApp(t, e)
	withSecretConfig(t, a)
	connectTestCodex(t, a, testCodexTokens())
	run := func(args ...string) {
		t.Helper()
		if code := a.Run(ctx, args); code != 0 {
			t.Fatalf("hibd %s exit %d: %s", strings.Join(args, " "), code, errOut)
		}
	}
	run("new", "app")
	d := newTestDelivery(t, a, e)
	d.look(ctx)
	want := codexLoginFile(t, a)
	if got := guest.codexAuthFile(); !bytes.Equal(got, want) {
		t.Fatalf("auth.json = %q, want the placeholder login file", got)
	}

	// codex logout in the machine deletes the file. It stays deleted while the machine runs.
	guest.change(func(g *secretGuest) { g.codexAuth = nil })
	d.look(ctx)
	if got := guest.codexAuthFile(); got != nil {
		t.Fatal("the daemon wrote auth.json again without a resume")
	}

	clock.add(time.Minute)
	run("pause", "app")
	run("bridge", "app")
	d.look(ctx)
	if got := guest.codexAuthFile(); !bytes.Equal(got, want) {
		t.Errorf("auth.json after a pause and a resume = %q, want the placeholder login file", got)
	}
}

func TestSetupDeliveryManyRunningMachinesRace(t *testing.T) {
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
}

func saveTestProfile(t *testing.T, a *App, name string) {
	t.Helper()
	p := github.Profile{Login: "octocat", ID: 583231, Name: name, Source: "gh"}
	if err := github.SaveProfile(filepath.Join(a.Getenv("HIBD_STATE_DIR"), github.ProfileFile), p); err != nil {
		t.Fatal(err)
	}
}

func casWrites(guest *secretGuest) int {
	n := 0
	for _, req := range guest.allRequests() {
		if req.Cmd == "sh" && len(req.Args) == 5 && req.Args[2] == "hibd-env" {
			n++
		}
	}
	return n
}

func TestDaemonRewritesAMachineWhenOnlyTheIdentityChanges(t *testing.T) {
	guest := &secretGuest{env: []byte("PATH=/usr/bin\n")}
	a, _, _ := newApp(t, runningApp(guest))
	withSecretConfig(t, a)
	secret := storeTestSecret(t, a, "github", "GH_TOKEN", "github.com", "gho_realSecretValue1234567890")
	saveTestProfile(t, a, "The Octocat")
	logs, stop := startDaemon(t, a)
	defer stop()
	waitFor(t, logs, "the daemon writes the block and git's identity", func() bool {
		id := guest.identity()
		return strings.Contains(guest.envString(), secret.Placeholder) && id != nil && id.Name == "The Octocat"
	})
	env, writes := guest.envString(), casWrites(guest)

	saveTestProfile(t, a, "Mona Lisa Octocat")
	waitFor(t, logs, "the daemon writes the new name", func() bool {
		id := guest.identity()
		return id != nil && *id == machines.GitIdentity{Name: "Mona Lisa Octocat", Email: "583231+octocat@users.noreply.github.com"}
	})
	if guest.envString() != env || casWrites(guest) != writes {
		t.Errorf("/etc/environment was rewritten, though only the identity changed")
	}
}

func TestDaemonLogsABrokenProfile(t *testing.T) {
	guest := &secretGuest{env: []byte("PATH=/usr/bin\n")}
	a, _, _ := newApp(t, runningApp(guest))
	withSecretConfig(t, a)
	path := filepath.Join(a.Getenv("HIBD_STATE_DIR"), github.ProfileFile)
	if err := os.WriteFile(path, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	logs, stop := startDaemon(t, a)
	defer stop()
	waitFor(t, logs, "the daemon logs the broken github.json", func() bool {
		return strings.Contains(logs.String(), "setup: ") && strings.Contains(logs.String(), path)
	})
	if guest.identity() != nil {
		t.Error("the daemon wrote an identity from a broken github.json")
	}
	saveTestProfile(t, a, "The Octocat")
	waitFor(t, logs, "the daemon writes the identity once github.json is fixed", func() bool {
		return guest.identity() != nil
	})
}

func TestNewWritesGitsIdentity(t *testing.T) {
	guest := &secretGuest{env: []byte("PATH=/usr/bin\n"), hostKey: newKey(t)}
	e := fake.New(fake.WithTemplates(images.Template("dev")), fake.WithExec(guest.exec))
	a, _, errOut := newApp(t, e)
	saveTestProfile(t, a, "The Octocat")
	if code := a.Run(t.Context(), []string{"new", "app"}); code != 0 {
		t.Fatalf("new exit %d: %s", code, errOut)
	}
	want := machines.GitIdentity{Name: "The Octocat", Email: "583231+octocat@users.noreply.github.com"}
	if id := guest.identity(); id == nil || *id != want {
		t.Errorf("git identity = %+v, want %+v", id, want)
	}
}

func TestNewRefusesABrokenProfile(t *testing.T) {
	e := fake.New(fake.WithTemplates(images.Template("dev")))
	a, _, errOut := newApp(t, e)
	path := filepath.Join(a.Getenv("HIBD_STATE_DIR"), github.ProfileFile)
	if err := os.WriteFile(path, []byte(`{"v":1,"login":"-bad-","id":1,"source":"gh"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := a.Run(t.Context(), []string{"new", "app"}); code != 1 || !strings.Contains(errOut.String(), "github.json") {
		t.Errorf("new exit %d, stderr %q; want 1 and an error about github.json", code, errOut)
	}
	if ms, _ := e.List(t.Context(), engine.ListOptions{}); len(ms) != 0 {
		t.Errorf("new made %d machines", len(ms))
	}
}
