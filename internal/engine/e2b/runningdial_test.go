package e2b_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/e2b"
)

func runningProxy(f *fakeE2B) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		m, ok := f.sandboxes[r.Header.Get("E2b-Sandbox-Id")]
		running := ok && m["state"] == "running"
		token := m["trafficAccessToken"]
		f.mu.Unlock()
		if !running {
			http.Error(w, "sandbox is not running", http.StatusBadGateway)
			return
		}
		if r.Header.Get("E2b-Traffic-Access-Token") != token {
			http.Error(w, "bad traffic token", http.StatusForbidden)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err == nil {
			c.CloseNow()
		}
	})
}

func TestRunningDialAcrossProcesses(t *testing.T) {
	if dir := os.Getenv("HIB_TOKEN_TEST_DIR"); dir != "" {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		e, err := e2b.New(e2b.Config{StateDir: dir, APIURL: os.Getenv("HIB_TOKEN_API"),
			SandboxURL: os.Getenv("HIB_TOKEN_PROXY"), APIKey: apiKey})
		if err != nil {
			t.Fatal(err)
		}
		c, err := e.DialRunningWebSocket(ctx, os.Getenv("HIB_TOKEN_ID"), 8081)
		if c != nil {
			c.Close()
		}
		want := os.Getenv("HIB_TOKEN_WANT")
		if want == "running" && (err != nil || c == nil) {
			t.Fatalf("fresh process running dial = %v", err)
		}
		if want == "paused" && !errors.Is(err, engine.ErrNotRunning) {
			t.Fatalf("fresh process paused dial = %v, want ErrNotRunning", err)
		}
		if want == "missing" && !errors.Is(err, engine.ErrNotFound) {
			t.Fatalf("fresh process removed dial = %v, want ErrNotFound", err)
		}
		return
	}
	for _, reply := range []string{"create", "connect 200", "connect 201"} {
		t.Run(reply, func(t *testing.T) {
			dir := t.TempDir()
			f, e := newFakeE2B(t, dir)
			f.envd = runningProxy(f)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			id := "aaaaaaaaaaaaaaaaaaaa"
			if reply == "create" {
				m, err := e.Create(ctx, engine.CreateOptions{Template: "base"})
				if err != nil {
					t.Fatal(err)
				}
				id = m.ID
			} else {
				state := "running"
				if reply == "connect 201" {
					state = "paused"
				}
				f.add(id, state, nil)
				if _, err := e.Resume(ctx, id, time.Minute); err != nil {
					t.Fatal(err)
				}
			}
			info, err := os.Stat(filepath.Join(dir, "traffic-token-"+id))
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("stored token permissions = %v, %v", info, err)
			}
			child := func(want string) {
				t.Helper()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRunningDialAcrossProcesses$", "-test.v")
				cmd.Env = append(os.Environ(), "HIB_TOKEN_TEST_DIR="+dir, "HIB_TOKEN_API="+f.apiURL,
					"HIB_TOKEN_PROXY="+f.proxyURL, "HIB_TOKEN_ID="+id, "HIB_TOKEN_WANT="+want)
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("fresh process %s: %v\n%s", want, err, output)
				}
			}
			before, err := e.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			child("running")
			if after, err := e.Get(ctx, id); err != nil || !after.EndAt.Equal(before.EndAt) {
				t.Fatalf("running dial changed timeout: %v, %v", after, err)
			}
			if err := e.Pause(ctx, id); err != nil {
				t.Fatal(err)
			}
			child("paused")
			if after, err := e.Get(ctx, id); err != nil || after.State != engine.Paused {
				t.Fatalf("non-resuming dial left machine %s, %v", after.State, err)
			}
			f.mu.Lock()
			connects := 0
			for _, req := range f.requests {
				if strings.HasSuffix(req.URL.Path, "/connect") {
					connects++
				}
			}
			f.mu.Unlock()
			wantConnects := 1
			if reply == "create" {
				wantConnects = 0
			}
			if connects != wantConnects {
				t.Fatalf("fresh process made %d connects, want %d", connects, wantConnects)
			}
			if err := e.Kill(ctx, id); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(dir, "traffic-token-"+id)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("killed machine retained a traffic token: %v", err)
			}
			child("missing")
		})
	}
}

func TestRunningDialRequiresTokenAndPreservesRefusals(t *testing.T) {
	f, e := newFakeE2B(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	id := "aaaaaaaaaaaaaaaaaaaa"
	f.add(id, "running", nil)
	if _, err := e.DialRunningWebSocket(ctx, id, 8081); !errors.Is(err, engine.ErrNoTrafficToken) {
		t.Fatalf("uncached running dial = %v, want ErrNoTrafficToken", err)
	}
	if _, err := e.Resume(ctx, id, time.Minute); err != nil {
		t.Fatal(err)
	}
	f.envd = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "E2B refusal", http.StatusForbidden)
	})
	if _, err := e.DialRunningWebSocket(ctx, id, 8081); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("running dial refusal = %v, want E2B's 403", err)
	}
}

func TestRunningDialRefreshesRejectedToken(t *testing.T) {
	for _, persistent := range []bool{false, true} {
		name := "memory"
		if persistent {
			name = "disk"
		}
		t.Run(name, func(t *testing.T) {
			dir := ""
			if persistent {
				dir = t.TempDir()
			}
			f, e := newFakeE2B(t, dir)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			id := "aaaaaaaaaaaaaaaaaaaa"
			f.add(id, "running", nil)
			if _, err := e.Resume(ctx, id, time.Minute); err != nil {
				t.Fatal(err)
			}
			const freshToken = "replacement-traffic-token"
			f.mu.Lock()
			f.sandboxes[id]["trafficAccessToken"] = freshToken
			f.envd = runningProxy(f)
			f.mu.Unlock()
			c, err := e.DialRunningWebSocket(ctx, id, 8081)
			if c != nil {
				c.Close()
			}
			if !errors.Is(err, engine.ErrNoTrafficToken) || !strings.Contains(err.Error(), "403") {
				t.Errorf("rejected-token dial = %v, want ErrNoTrafficToken and the proxy's 403", err)
			}
			if persistent {
				if data, err := os.ReadFile(filepath.Join(dir, "traffic-token-"+id)); err != nil || string(data) != trafficToken {
					t.Errorf("rejected dial changed stored token: %q, %v", data, err)
				}
			}
			before, err := e.Get(ctx, id)
			if err != nil || before.State != engine.Running {
				t.Fatalf("rejected-token dial left machine %s, %v, want running", before.State, err)
			}
			c, err = e.DialWebSocket(ctx, id, 8081, time.Minute)
			if err != nil || c == nil {
				t.Fatalf("admitted connect = %v, want a connection with the API's new token", err)
			}
			c.Close()
			if persistent {
				data, err := os.ReadFile(filepath.Join(dir, "traffic-token-"+id))
				if err != nil || string(data) != freshToken {
					t.Fatalf("stored token after connect = %q, %v, want the API's new token", data, err)
				}
				e, err = e2b.New(e2b.Config{StateDir: dir, APIURL: f.apiURL, SandboxURL: f.proxyURL, APIKey: apiKey})
				if err != nil {
					t.Fatal(err)
				}
			}
			c, err = e.DialRunningWebSocket(ctx, id, 8081)
			if err != nil || c == nil {
				t.Fatalf("next running dial = %v, want a connection without connect", err)
			}
			c.Close()
			after, err := e.Get(ctx, id)
			if err != nil || after.State != engine.Running || !after.EndAt.Equal(before.EndAt) {
				t.Errorf("running dial changed machine state or timeout: %+v, %v", after, err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			connects := 0
			for _, req := range f.requests {
				if strings.HasSuffix(req.URL.Path, "/connect") {
					connects++
				}
			}
			if connects != 2 {
				t.Errorf("running recovery made %d connects, want setup and one admitted connect", connects)
			}
		})
	}
}

func TestRunningDialFailureRequiresAdmission(t *testing.T) {
	f, e := newFakeE2B(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	id := "aaaaaaaaaaaaaaaaaaaa"
	f.add(id, "running", nil)
	if _, err := e.Resume(ctx, id, time.Minute); err != nil {
		t.Fatal(err)
	}
	f.envd = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "proxy unavailable", http.StatusBadGateway)
	})
	c, err := e.DialRunningWebSocket(ctx, id, 8081)
	if c != nil {
		c.Close()
	}
	if !errors.Is(err, engine.ErrNoTrafficToken) || !strings.Contains(err.Error(), "502") {
		t.Fatalf("failed running dial = %v, want ErrNoTrafficToken and the proxy's 502", err)
	}
}

func TestRunningDialCancellationKeepsToken(t *testing.T) {
	dir := t.TempDir()
	f, e := newFakeE2B(t, dir)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	id := "aaaaaaaaaaaaaaaaaaaa"
	f.add(id, "running", nil)
	if _, err := e.Resume(ctx, id, time.Minute); err != nil {
		t.Fatal(err)
	}
	dialCtx, stop := context.WithCancel(ctx)
	defer stop()
	f.envd = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stop()
		select {
		case <-r.Context().Done():
		case <-ctx.Done():
		}
	})
	c, err := e.DialRunningWebSocket(dialCtx, id, 8081)
	if c != nil {
		c.Close()
	}
	if !errors.Is(err, context.Canceled) || errors.Is(err, engine.ErrNoTrafficToken) {
		t.Errorf("canceled running dial = %v, want cancellation without fallback", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "traffic-token-"+id))
	if err != nil || string(data) != trafficToken {
		t.Errorf("canceled dial changed stored token: %q, %v", data, err)
	}
}

func TestRunningDialRecoversPrunedToken(t *testing.T) {
	dir := t.TempDir()
	f, e := newFakeE2B(t, dir)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	id := "aaaaaaaaaaaaaaaaaaaa"
	f.add(id, "running", nil)
	if _, err := e.Resume(ctx, id, time.Minute); err != nil {
		t.Fatal(err)
	}
	f.envd = runningProxy(f)
	if err := e2b.PruneTrafficTokens(dir, func(string) (bool, error) {
		if _, err := e.Resume(ctx, id, time.Minute); err != nil {
			return true, err
		}
		return false, nil
	}, false, t.Logf); err != nil {
		t.Fatal(err)
	}
	c, err := e.DialRunningWebSocket(ctx, id, 8081)
	if c != nil {
		c.Close()
	}
	if !errors.Is(err, engine.ErrNoTrafficToken) {
		t.Fatalf("pruned-token running dial = %v, want ErrNoTrafficToken", err)
	}
	c, err = e.DialWebSocket(ctx, id, 8081, time.Minute)
	if err != nil || c == nil {
		t.Fatalf("admitted fallback after prune = %v", err)
	}
	c.Close()
	data, err := os.ReadFile(filepath.Join(dir, "traffic-token-"+id))
	if err != nil || string(data) != trafficToken {
		t.Fatalf("fallback token = %q, %v, want fresh API token", data, err)
	}
	c, err = e.DialRunningWebSocket(ctx, id, 8081)
	if err != nil || c == nil {
		t.Fatalf("next running dial after prune = %v", err)
	}
	c.Close()
	f.mu.Lock()
	defer f.mu.Unlock()
	connects := 0
	for _, req := range f.requests {
		if strings.HasSuffix(req.URL.Path, "/connect") {
			connects++
		}
	}
	if connects != 3 {
		t.Errorf("prune recovery made %d connects, want setup, racing publication and one fallback", connects)
	}
}

func TestRunningDialRefusesUnsafeTokenFiles(t *testing.T) {
	for _, kind := range []string{"public", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			f, e := newFakeE2B(t, dir)
			id := "aaaaaaaaaaaaaaaaaaaa"
			f.add(id, "running", nil)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if _, err := e.Resume(ctx, id, time.Minute); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "traffic-token-"+id)
			if kind == "public" {
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			} else {
				target := filepath.Join(dir, "other")
				if err := os.Rename(path, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			c, err := e.DialRunningWebSocket(ctx, id, 8081)
			if c != nil {
				c.Close()
			}
			if err == nil || errors.Is(err, engine.ErrNoTrafficToken) {
				t.Fatalf("unsafe token file dial = %v, want an explicit storage error", err)
			}
		})
	}
}
