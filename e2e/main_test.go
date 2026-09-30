//go:build e2e

// Package e2e tests Hibernaut on the real VM (make e2e, make e2e-slow). The tests run on the
// laptop and reach E2B through an SSH tunnel. Every machine they create is labeled hib-test=e2e,
// and machines a crashed run left with that label are deleted before the tests start.
package e2e

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/e2b"
)

const (
	// localDevKey is E2B's fixed local-development API key (vm/lib/e2b.sh). E2B is only
	// reachable through SSH.
	localDevKey = "e2b_53ae1fed82754c17ad8077fbc8bcdd90"
	template    = "base"
)

// labels mark every machine the tests create.
var labels = map[string]string{"hib-test": "e2e"}

// vm is the host under test. Tests use it one at a time.
var vm *host

func TestMain(m *testing.M) {
	os.Exit(setUp(m))
}

func setUp(m *testing.M) int {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var err error
	if vm, err = newHost(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		return 1
	}
	defer vm.closeTunnel()
	if err := vm.deleteLeftovers(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		return 1
	}
	return m.Run()
}

// host is the VM, reached over SSH.
type host struct {
	// root is this repository, for the azure/ scripts.
	root string
	// target is user@ip.
	target string
	// engine talks to E2B through the tunnel. It's nil while the tunnel is closed.
	engine *e2b.Engine

	tunnel *exec.Cmd
	exited chan error
}

func newHost(ctx context.Context) (*host, error) {
	root, err := filepath.Abs("..")
	if err != nil {
		return nil, err
	}
	h := &host{root: root, target: os.Getenv("HIB_E2E_TARGET")}
	if h.target == "" {
		if h.target, err = h.azure(ctx, "target.sh"); err != nil {
			return nil, err
		}
	}
	return h, h.openTunnel(ctx)
}

var sshOptions = []string{
	"-o", "BatchMode=yes",
	"-o", "ConnectTimeout=10",
	"-o", "StrictHostKeyChecking=accept-new",
	"-o", "ServerAliveInterval=10",
	"-o", "ServerAliveCountMax=3",
}

// openTunnel forwards two free local ports to E2B's API and client-proxy on the VM, and points
// h.engine at them once the API is healthy.
func (h *host) openTunnel(ctx context.Context) error {
	h.closeTunnel()
	apiPort, err := freePort()
	if err != nil {
		return err
	}
	proxyPort, err := freePort()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd := exec.Command("ssh", append(slices.Clone(sshOptions), "-N", "-o", "ExitOnForwardFailure=yes",
		"-L", fmt.Sprintf("%d:127.0.0.1:3000", apiPort),
		"-L", fmt.Sprintf("%d:127.0.0.1:3002", proxyPort),
		h.target)...)
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	h.tunnel, h.exited = cmd, make(chan error, 1)
	go func() { h.exited <- cmd.Wait() }()

	apiURL := fmt.Sprintf("http://127.0.0.1:%d", apiPort)
	for !healthy(ctx, apiURL) {
		select {
		case err := <-h.exited:
			h.tunnel = nil
			return fmt.Errorf("SSH tunnel to %s: %v: %s", h.target, err, strings.TrimSpace(stderr.String()))
		case <-ctx.Done():
			h.closeTunnel()
			return fmt.Errorf("E2B's API isn't healthy through the SSH tunnel to %s: %w", h.target, ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
	h.engine, err = e2b.New(e2b.Config{
		APIURL:     apiURL,
		SandboxURL: fmt.Sprintf("http://127.0.0.1:%d", proxyPort),
		APIKey:     cmp.Or(os.Getenv("HIB_E2E_API_KEY"), localDevKey),
	})
	return err
}

func (h *host) closeTunnel() {
	h.engine = nil
	if h.tunnel == nil {
		return
	}
	_ = h.tunnel.Process.Kill()
	<-h.exited
	h.tunnel = nil
}

func healthy(ctx context.Context, url string) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// ssh runs a shell command on the VM and returns its trimmed stdout.
func (h *host) ssh(ctx context.Context, command string) (string, error) {
	out, err := output(exec.CommandContext(ctx, "ssh", append(slices.Clone(sshOptions), h.target, command)...))
	if err != nil {
		err = fmt.Errorf("ssh %s %q: %w", h.target, command, err)
	}
	return out, err
}

// azure runs one of the laptop's azure/ scripts and returns its trimmed stdout.
func (h *host) azure(ctx context.Context, script string) (string, error) {
	out, err := output(exec.CommandContext(ctx, filepath.Join(h.root, "azure", script)))
	if err != nil {
		err = fmt.Errorf("azure/%s: %w", script, err)
	}
	return out, err
}

func output(cmd *exec.Cmd) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err != nil {
		err = fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), err
}

func (h *host) deleteLeftovers(ctx context.Context) error {
	ms, err := h.engine.List(ctx, engine.ListOptions{Metadata: labels})
	if err != nil {
		return err
	}
	for _, m := range ms {
		fmt.Fprintf(os.Stderr, "e2e: deleting %s machine %s, left by an earlier run\n", m.State, m.ID)
		if err := h.engine.Kill(ctx, m.ID); err != nil && !errors.Is(err, engine.ErrNotFound) {
			return err
		}
	}
	return nil
}

// newMachine creates a labeled machine that's deleted when the test ends.
func newMachine(t *testing.T, ctx context.Context) engine.Machine {
	t.Helper()
	m, err := vm.engine.Create(ctx, engine.CreateOptions{Template: template, Timeout: 30 * time.Minute, Metadata: labels})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if vm.engine == nil {
			if err := vm.openTunnel(ctx); err != nil {
				t.Logf("leaving machine %s for the next run to delete: %v", m.ID, err)
				return
			}
		}
		if err := vm.engine.Kill(ctx, m.ID); err != nil && !errors.Is(err, engine.ErrNotFound) {
			t.Errorf("cleanup: Kill(%s): %v", m.ID, err)
		}
	})
	return m
}

// sh runs script with sh -c in a machine and returns its stdout. It fails the test unless the
// script exits 0.
func sh(t *testing.T, ctx context.Context, id, script string) string {
	t.Helper()
	res, err := vm.engine.Exec(ctx, id, engine.ExecRequest{Cmd: "sh", Args: []string{"-c", script}})
	if err != nil {
		t.Fatalf("Exec(%q): %v", script, err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("Exec(%q) exited %d: %s", script, res.ExitCode, res.Stderr)
	}
	return string(res.Stdout)
}

func mustSSH(t *testing.T, ctx context.Context, command string) string {
	t.Helper()
	out, err := vm.ssh(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func testContext(t *testing.T, d time.Duration) context.Context {
	ctx, cancel := context.WithTimeout(t.Context(), d)
	t.Cleanup(cancel)
	return ctx
}

// waitFor checks cond every 5 seconds until it's true, and fails the test after limit.
func waitFor(t *testing.T, ctx context.Context, what string, limit time.Duration, cond func() bool) {
	t.Helper()
	start := time.Now()
	for !cond() {
		if time.Since(start) > limit {
			t.Fatalf("gave up waiting for %s after %v", what, limit)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for %s: %v", what, ctx.Err())
		case <-time.After(5 * time.Second):
		}
	}
	t.Logf("waited %v for %s", time.Since(start).Round(time.Second), what)
}
