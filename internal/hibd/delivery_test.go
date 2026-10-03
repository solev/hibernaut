package hibd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/github"
	"github.com/solev/hibernaut/internal/machines"
)

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
