package hibd

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/e2b"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/machines"
	"github.com/solev/hibernaut/internal/room"
)

type processSaveEngine struct {
	engine.Engine
	save engine.Engine
}

func (e processSaveEngine) SaveTemplate(ctx context.Context, id, alias string) (engine.TemplateInfo, error) {
	return e.save.SaveTemplate(ctx, id, alias)
}

func TestCleanupReadUsesCrossProcessFlock(t *testing.T) {
	if role := os.Getenv("HIB_TEST_SAVE_ROLE"); role != "" {
		runCleanupProcess(t, role)
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	gate, saving, waited, saveExited := make(chan struct{}), make(chan struct{}, 1), make(chan struct{}, 1), make(chan struct{})
	var lists atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sandboxes/web01/snapshots":
			saving <- struct{}{}
			select {
			case <-gate:
			case <-ctx.Done():
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(201)
			fmt.Fprint(w, `{"names":["hibimg-myimg"],"snapshotID":"hibimg-myimg:default"}`)
		case "/templates/aliases/hibimg-myimg":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"templateID":"snapshot","public":false}`)
		case "/templates/snapshot":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"templateID":"snapshot","builds":[]}`)
		case "/probe/list":
			lists.Add(1)
		case "/probe/wait":
			waited <- struct{}{}
			select {
			case <-saveExited:
			case <-ctx.Done():
			}
		default:
			t.Errorf("unexpected request %s", r.URL)
			w.WriteHeader(500)
		}
	}))
	defer func() { cancel(); srv.Close() }()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	state, storage := t.TempDir(), t.TempDir()
	child := func(role string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, binary, "-test.run=^TestCleanupReadUsesCrossProcessFlock$", "-test.count=1")
		cmd.Env = append(os.Environ(), "HIB_TEST_SAVE_ROLE="+role, "HIB_TEST_STATE="+state, "HIB_TEST_STORAGE="+storage, "HIB_TEST_API="+srv.URL)
		return cmd
	}
	save := child("save")
	save.Stderr = os.Stderr
	if err := save.Start(); err != nil {
		t.Fatal(err)
	}
	saveDone := make(chan error, 1)
	go func() {
		saveDone <- save.Wait()
		close(saveExited)
	}()
	select {
	case <-saving:
	case <-ctx.Done():
		t.Fatal("save child didn't dispatch")
	}
	clean := child("cleanup")
	clean.Stderr = os.Stderr
	out, err := clean.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := clean.Start(); err != nil {
		t.Fatal(err)
	}
	output := make(chan string, 1)
	go func() {
		var lines []string
		scanner := bufio.NewScanner(out)
		for scanner.Scan() {
			lines = append(lines, scanner.Text())
		}
		output <- strings.Join(lines, "\n")
	}()
	select {
	case <-waited:
	case <-ctx.Done():
		t.Fatal("cleanup child didn't wait")
	}
	if lists.Load() != 0 {
		t.Fatal("cleanup listed E2B while the save held hibd.lock")
	}
	close(gate)
	if err := <-saveDone; err != nil {
		t.Fatal(err)
	}
	if err := clean.Wait(); err != nil {
		t.Fatal(err)
	}
	text := <-output
	if lists.Load() != 1 || strings.Count(text, "waiting for an image save or fork to finish") != 1 {
		t.Fatalf("List calls = %d, output %q", lists.Load(), text)
	}
	records, err := os.ReadDir(filepath.Join(state, "saves"))
	if err != nil || len(records) != 0 {
		t.Fatalf("save record didn't settle in another process: %v, %v", records, err)
	}
}

func runCleanupProcess(t *testing.T, role string) {
	state, url := os.Getenv("HIB_TEST_STATE"), os.Getenv("HIB_TEST_API")
	e := fake.New()
	e.Add(engine.Machine{ID: "web01", State: engine.Running, StartedAt: time.Now(), MemoryMB: 512, Metadata: map[string]string{"hib.name": "web", "hib.idle": "15m"}})
	if role == "save" {
		api, err := e2b.New(e2b.Config{APIURL: url, SandboxURL: url, APIKey: "test"})
		if err != nil {
			t.Fatal(err)
		}
		m := machines.New(machines.Config{
			Engine: processSaveEngine{e, api}, StateDir: state,
			ReadRoom: func() (room.Host, error) {
				return room.Host{PageSize: 2 << 20, Pages: 3200, FreePages: 3125, DiskFree: 80 << 30}, nil
			},
			Durable: func(context.Context, string) error { return nil },
		})
		if _, err := m.SaveImage(t.Context(), "web", "myimg"); err != nil {
			t.Fatal(err)
		}
		return
	}
	probe := func(ctx context.Context, path string) error {
		req, err := http.NewRequestWithContext(ctx, "GET", url+path, nil)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	eng := cleanupEngine{e, func(ctx context.Context, opts engine.ListOptions) ([]engine.Machine, error) {
		if err := probe(ctx, "/probe/list"); err != nil {
			return nil, err
		}
		return e.List(ctx, opts)
	}}
	env := map[string]string{
		"HIBD_CONFIG": filepath.Join(state, "none.env"), "HIBD_STATE_DIR": state,
		"HIBD_E2B_STORAGE": os.Getenv("HIB_TEST_STORAGE"), "HIBD_TRIM": "off",
		"E2B_API_KEY": "test",
	}
	a := App{
		Stdout: os.Stdout, Stderr: os.Stderr, Getenv: func(k string) string { return env[k] },
		NewEngine: func() (engine.Engine, error) { return eng, nil },
		Query:     func(context.Context, string) ([][]string, error) { return nil, nil },
		WaitCleanup: func(ctx context.Context) error {
			return probe(ctx, "/probe/wait")
		},
	}
	if code := a.Run(t.Context(), []string{"cleanup"}); code != 0 {
		t.Fatalf("cleanup exit = %d", code)
	}
}
