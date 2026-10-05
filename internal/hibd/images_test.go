package hibd

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/hibapi"
)

func imageApp(t *testing.T) (*App, *fake.Engine, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	hostKey := newKey(t)
	e := fake.New(
		fake.WithTemplates(images.Template("dev")),
		fake.WithExec(func(_ context.Context, _ engine.Machine, _ string, req engine.ExecRequest) (engine.ExecResult, error) {
			if req.Cmd == images.MachineInit {
				return engine.ExecResult{Stdout: []byte(hibapi.FormatHostKey(hostKey) + "\n")}, nil
			}
			return engine.ExecResult{}, nil
		}),
	)
	a, out, errOut := newApp(t, e)
	storage := t.TempDir()
	writeLeakBuild(t, storage, leakBuildA, []byte("memory"), []byte("disk"))
	getenv := a.Getenv
	a.Getenv = func(k string) string {
		if k == "HIBD_E2B_STORAGE" {
			return storage
		}
		return getenv(k)
	}
	a.EUID = func() int { return 0 }
	a.Query = oneBuildQuery(leakBuildA)
	return a, e, out, errOut
}

func TestImageSaveThenNewFromSavedImage(t *testing.T) {
	a, e, out, errOut := imageApp(t)
	code, r, err := runJSON(t, a, out, "new", "web", "-json")
	if code != 0 || err != nil || r.Machine == nil {
		t.Fatalf("new = %d, %v; stderr %s", code, err, errOut)
	}

	origin := r.Machine.ID
	a.Query = func(ctx context.Context, sql string) ([][]string, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 2*time.Minute {
			t.Error("snapshot readiness has no two-minute bound")
		}
		if strings.HasPrefix(sql, "SELECT status_group") {
			return [][]string{{"ready"}}, nil
		}
		if !strings.Contains(sql, "snapshots") || !strings.Contains(sql, origin) {
			t.Fatalf("query = %q, want the origin's latest snapshot", sql)
		}
		return [][]string{{leakBuildA}}, nil
	}
	code, r, err = runJSON(t, a, out, "image", "save", "web", "myimg", "-json")
	if code != 0 || err != nil || r.Image == nil || r.Image.Name != "myimg" || r.Image.Kind != hibapi.KindSaved || !r.Image.Built {
		t.Fatalf("save = %d, %+v, %v; stderr %s", code, r, err, errOut)
	}
	m, err := e.Get(t.Context(), origin)
	if err != nil || m.State != engine.Running || r.Image.MemoryMB != m.MemoryMB {
		t.Fatalf("origin = %+v, %v; image = %+v", m, err, r.Image)
	}
	code, r, err = runJSON(t, a, out, "new", "web2", "-image", "myimg", "-json")
	if code != 0 || err != nil || r.Machine == nil || r.Machine.Image != "myimg" {
		t.Fatalf("new from saved image = %d, %+v, %v", code, r, err)
	}
	code, r, err = runJSON(t, a, out, "image", "ls", "-json")
	if code != 0 || err != nil || len(r.Images) != 2 || r.Images[0].Kind != hibapi.KindBuiltIn || r.Images[1].Name != "myimg" {
		t.Fatalf("image ls = %d, %+v, %v", code, r, err)
	}
}

func TestImageSaveOutput(t *testing.T) {
	a, _, out, errOut := imageApp(t)
	if code, _, err := runJSON(t, a, out, "new", "web", "-json"); code != 0 || err != nil {
		t.Fatalf("new = %d, %v; stderr %s", code, err, errOut)
	}
	out.Reset()
	if code := a.Run(t.Context(), []string{"image", "save", "web", "myimg"}); code != 0 {
		t.Fatalf("save = %d; stderr %s", code, errOut)
	}
	if got, want := out.String(), "saved image myimg (512 MiB). use it with: hib new NAME -image myimg\n"; got != want {
		t.Errorf("saved image output = %q, want %q", got, want)
	}
}

func TestForkRunningAndPausedOrigins(t *testing.T) {
	for _, state := range []engine.State{engine.Running, engine.Paused} {
		t.Run(string(state), func(t *testing.T) {
			a, e, out, errOut := imageApp(t)
			code, r, err := runJSON(t, a, out, "new", "web", "-idle", "2h", "-json")
			if code != 0 || err != nil || r.Machine == nil {
				t.Fatalf("new = %d, %v; stderr %s", code, err, errOut)
			}
			id := r.Machine.ID
			if state == engine.Paused {
				if code, _, err := runJSON(t, a, out, "pause", "web", "-json"); code != 0 || err != nil {
					t.Fatalf("pause = %d, %v", code, err)
				}
			}
			code, r, err = runJSON(t, a, out, "fork", "web", "-count", "2", "-json")
			if code != 0 || err != nil || len(r.Machines) != 2 {
				t.Fatalf("fork = %d, %+v, %v; stderr %s", code, r, err, errOut)
			}
			for i, name := range []string{"web-1", "web-2"} {
				copy := r.Machines[i]
				if copy.Name != name || copy.ForkOf != "web" || copy.Image != "dev" || copy.State != "running" || hibapi.FormatIdle(copy.IdleSeconds) != "2h" {
					t.Errorf("copy = %+v", copy)
				}
			}
			origin, err := e.Get(t.Context(), id)
			if err != nil || origin.State != state {
				t.Fatalf("origin = %+v, %v, want %s", origin, err, state)
			}
		})
	}
}
