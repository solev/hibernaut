//go:build e2e

// Package e2e tests Hibernaut on the real VM (make e2e, make e2e-slow). The tests run on the
// laptop and reach E2B through an SSH tunnel.
// Machines are labeled hib-test=e2e or named e2e-*. Leftovers from crashed runs are deleted
// before the tests start.
package e2e

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/e2b"
	"github.com/solev/hibernaut/internal/termsafe"
)

const (
	// localDevKey is E2B's fixed local-development API key (vm/lib/e2b.sh). E2B is only
	// reachable through SSH.
	localDevKey = "e2b_53ae1fed82754c17ad8077fbc8bcdd90"
	template    = "base"
)

var labels = map[string]string{"hib-test": "e2e"}

var vm *host

var (
	hibBin string
	hibDir string

	imageOnce  sync.Once
	imageErr   error
	imageBuilt time.Duration
)

func TestMain(m *testing.M) {
	os.Exit(setUp(m))
}

func setUp(m *testing.M) int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var err error
	if vm, err = newHost(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		return 1
	}
	defer vm.closeTunnel()
	if err := setUpHib(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		return 1
	}
	if hibDir != "" {
		defer os.RemoveAll(hibDir)
	}
	if err := vm.deleteLeftovers(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", termsafe.String(err.Error()))
		return 1
	}
	if err := deleteLeftoverSecrets(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		return 1
	}
	if err := deleteLeftoverGitHub(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		return 1
	}
	return m.Run()
}

func setUpHib(ctx context.Context) error {
	dir, err := os.MkdirTemp("", "hib-e2e-")
	if err != nil {
		return err
	}
	hibDir = dir
	if hibBin, err = buildHib(ctx); err != nil {
		return err
	}
	group := cmp.Or(os.Getenv("RESOURCE_GROUP"), "rg-e2b-test")
	name := cmp.Or(os.Getenv("VM_NAME"), "e2b-test")
	stdout, stderr, code := runHib(ctx, "init", "-host", vm.target,
		"-ssh-config", filepath.Join(dir, "included.conf"),
		"-azure-group", group, "-azure-vm", name)
	if code != 0 {
		return fmt.Errorf("hib init exited %d: %s%s", code, stdout, stderr)
	}
	fmt.Fprint(os.Stderr, "e2e: hib init:\n", stdout)
	return nil
}

func buildHib(ctx context.Context) (string, error) {
	root := vm.root
	ver, err := output(exec.CommandContext(ctx, "git", "-C", root, "describe", "--tags", "--always", "--dirty"))
	if err != nil {
		return "", fmt.Errorf("finding the version: %w", err)
	}
	bin := filepath.Join(hibDir, "hib")
	cmd := exec.CommandContext(ctx, "go", "build",
		"-ldflags", "-X github.com/solev/hibernaut/internal/version.Version="+ver,
		"-o", bin, "./cmd/hib")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("building hib: %w: %s", err, out)
	}
	return bin, nil
}

func runHib(ctx context.Context, args ...string) (stdout, stderr string, code int) {
	cmd := exec.CommandContext(ctx, hibBin, append([]string{"-config", hibDir}, args...)...)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	switch err := cmd.Run(); {
	case err == nil:
	case errors.As(err, new(*exec.ExitError)):
		code = err.(*exec.ExitError).ExitCode()
	default:
		code, e = -1, *bytes.NewBufferString(err.Error())
	}
	return o.String(), e.String(), code
}

func hib(t *testing.T, ctx context.Context, args ...string) string {
	t.Helper()
	stdout, stderr, code := runHib(ctx, args...)
	if code != 0 {
		t.Fatalf("hib %s exited %d: %s%s", strings.Join(args, " "), code, stdout, stderr)
	}
	return stdout
}

func ensureImage(t *testing.T) {
	t.Helper()
	imageOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 65*time.Minute)
		defer cancel()
		start := time.Now()
		_, stderr, code := runHib(ctx, "image", "build", "dev")
		imageBuilt = time.Since(start)
		if code != 0 {
			imageErr = fmt.Errorf("hib image build dev exited %d: %s", code, stderr)
		}
	})
	if imageErr != nil {
		t.Fatal(imageErr)
	}
	t.Logf("dev image ready (built in %v)", imageBuilt.Round(time.Second))
}

func newHibMachine(t *testing.T, ctx context.Context, flags ...string) string {
	t.Helper()
	ensureImage(t)
	name := fmt.Sprintf("e2e-%08x", rand.Uint32())
	hib(t, ctx, append([]string{"new", name}, flags...)...)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		runHib(ctx, "rm", name)

		if ms, err := vm.engine.List(ctx, engine.ListOptions{Metadata: map[string]string{"hib.name": name}}); err != nil || len(ms) > 0 {
			t.Logf("leaving machine %s for the next run to delete", name)
		}
	})
	return name
}

func sshMachineCmd(ctx context.Context, extra []string, name, command string) *exec.Cmd {
	args := append([]string{"-F", filepath.Join(hibDir, "ssh_config"), "-o", "BatchMode=yes"}, extra...)
	args = append(args, name+".hib")
	if command != "" {
		args = append(args, command)
	}
	return exec.CommandContext(ctx, "ssh", args...)
}

func sshMachine(t *testing.T, ctx context.Context, name, command string) string {
	t.Helper()
	out, err := output(sshMachineCmd(ctx, nil, name, command))
	if err != nil {
		t.Fatalf("ssh %s.hib %q: %v", name, command, err)
	}
	return out
}

type host struct {
	root   string
	target string
	engine *e2b.Engine

	tunnel *sshTunnel
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

func (h *host) openTunnel(ctx context.Context) error {
	h.closeTunnel()
	tunnel, err := startSSHTunnel(ctx, h.target, sshOptions, exec.Command)
	if err != nil {
		return err
	}
	h.tunnel = tunnel
	h.engine, err = e2b.New(e2b.Config{
		APIURL:     tunnel.apiURL,
		SandboxURL: tunnel.proxyURL,
		APIKey:     cmp.Or(os.Getenv("HIB_E2E_API_KEY"), localDevKey),
	})
	if err != nil {
		h.closeTunnel()
	}
	return err
}

func (h *host) closeTunnel() {
	h.engine = nil
	h.tunnel.close()
	h.tunnel = nil
}

func (h *host) ssh(ctx context.Context, command string) (string, error) {
	out, err := output(exec.CommandContext(ctx, "ssh", append(slices.Clone(sshOptions), h.target, command)...))
	if err != nil {
		err = fmt.Errorf("ssh %s %q: %w", h.target, command, err)
	}
	return out, err
}

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
	ms, err := h.engine.List(ctx, engine.ListOptions{})
	if err != nil {
		return err
	}
	for _, m := range ms {
		if !hasLabels(m.Metadata, labels) && !strings.HasPrefix(m.Metadata["hib.name"], "e2e-") {
			continue
		}
		fmt.Fprintf(os.Stderr, "e2e: deleting %s machine %s, left by an earlier run\n", m.State, m.ID)
		if err := h.engine.Kill(ctx, m.ID); err != nil && !errors.Is(err, engine.ErrNotFound) {
			return err
		}
	}
	saved, err := h.engine.SavedTemplates(ctx)
	if err != nil {
		return err
	}
	for _, tmpl := range saved {
		if !strings.HasPrefix(tmpl.Alias, "hib-contract-") && !strings.HasPrefix(tmpl.Alias, "hibimg-e2e-") {
			continue
		}
		fmt.Fprintf(os.Stderr, "e2e: deleting saved template %s, left by an earlier run\n", termsafe.String(tmpl.Alias))
		if err := h.engine.DeleteTemplate(ctx, tmpl.ID); err != nil && !errors.Is(err, engine.ErrNoTemplate) {
			return err
		}
	}
	return nil
}

func hasLabels(metadata, want map[string]string) bool {
	for k, v := range want {
		if metadata[k] != v {
			return false
		}
	}
	return true
}

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
