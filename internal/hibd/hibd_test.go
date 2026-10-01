package hibd

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/version"
)

// newApp returns an app with its own state directory and authorized_keys file, and no config
// file.
func newApp(t *testing.T, e engine.Engine) (*App, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	state := t.TempDir()
	keys := filepath.Join(t.TempDir(), "authorized_keys")
	if err := os.WriteFile(keys, ssh.MarshalAuthorizedKey(newKey(t)), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"HIBD_CONFIG":          filepath.Join(state, "none.env"),
		"E2B_API_KEY":          "test",
		"HIBD_STATE_DIR":       state,
		"HIBD_AUTHORIZED_KEYS": keys,
		"HIBD_QUIESCING":       filepath.Join(t.TempDir(), "quiescing"),
	}
	var out, errOut bytes.Buffer
	return &App{
		Stdin:     strings.NewReader(""),
		Stdout:    &out,
		Stderr:    &errOut,
		Getenv:    func(k string) string { return env[k] },
		NewEngine: func() (engine.Engine, error) { return e, nil },
	}, &out, &errOut
}

func newKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// newEngine returns a fake engine whose machines answer machine-init with hostKey.
func newEngine(hostKey ssh.PublicKey, opts ...fake.Option) *fake.Engine {
	return fake.New(append([]fake.Option{
		fake.WithTemplates(images.Template("dev")),
		fake.WithExec(func(_ context.Context, _ engine.Machine, _ string, req engine.ExecRequest) (engine.ExecResult, error) {
			if req.Cmd != images.MachineInit || req.User != "root" {
				return engine.ExecResult{ExitCode: 127}, nil
			}
			return engine.ExecResult{Stdout: []byte(hibapi.FormatHostKey(hostKey) + "\n")}, nil
		}),
	}, opts...)...)
}

// runJSON runs a command that prints JSON and decodes what it printed.
func runJSON(t *testing.T, a *App, out *bytes.Buffer, args ...string) (int, hibapi.Response, error) {
	t.Helper()
	out.Reset()
	code := a.Run(t.Context(), args)
	r, err := hibapi.DecodeResponse(out.Bytes())
	return code, r, err
}

func TestLs(t *testing.T) {
	e := fake.New()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	e.Add(engine.Machine{ID: "run1", Template: "base", State: engine.Running, StartedAt: now, EndAt: now.Add(time.Hour)})
	e.Add(engine.Machine{ID: "zzz1", Template: "base", State: engine.Paused, StartedAt: now.Add(-time.Hour)})

	a, out, _ := newApp(t, e)
	if code := a.Run(t.Context(), []string{"ls", "-state", "running", "-q"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if out.String() != "run1\n" {
		t.Errorf("ls -state running -q = %q, want %q", out.String(), "run1\n")
	}

	out.Reset()
	if code := a.Run(t.Context(), []string{"ls"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "NAME") || !strings.HasPrefix(lines[1], "-     run1") ||
		!strings.Contains(lines[1], "2026-09-30T13:00:00Z") || !strings.Contains(lines[2], "zzz1") {
		t.Errorf("ls =\n%s", out.String())
	}

	if code := a.Run(t.Context(), []string{"ls", "-state", "sleeping"}); code != 2 {
		t.Errorf("ls -state sleeping exit = %d, want 2", code)
	}
}

func TestPauseAll(t *testing.T) {
	e := fake.New()
	e.Add(engine.Machine{ID: "run1", State: engine.Running})
	a, _, errOut := newApp(t, e)
	if code := a.Run(t.Context(), []string{"pause-all"}); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if m, _ := e.Get(t.Context(), "run1"); m.State != engine.Paused {
		t.Errorf("run1 is %s, want paused", m.State)
	}
	if !strings.Contains(errOut.String(), "paused 1 machine(s)") {
		t.Errorf("log = %q", errOut)
	}
}

func TestPauseAllTimesOut(t *testing.T) {
	e := fake.New(fake.WithPauseHook(func(ctx context.Context, _ string) error {
		<-ctx.Done()
		return ctx.Err()
	}))
	e.Add(engine.Machine{ID: "stuck", State: engine.Running})
	a, _, errOut := newApp(t, e)
	if code := a.Run(t.Context(), []string{"pause-all", "-timeout", "10ms"}); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "stuck") {
		t.Errorf("log = %q, want it to name the stuck machine", errOut)
	}
}

func TestEngineErrorExitsOne(t *testing.T) {
	a, _, errOut := newApp(t, nil)
	a.NewEngine = func() (engine.Engine, error) { return nil, errors.New("E2B_API_KEY is not set") }
	if code := a.Run(t.Context(), []string{"ls"}); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "E2B_API_KEY") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestUnknownCommand(t *testing.T) {
	a, _, _ := newApp(t, fake.New())
	if code := a.Run(t.Context(), []string{"frobnicate"}); code != 2 {
		t.Errorf("exit %d, want 2", code)
	}
	if code := a.Run(t.Context(), nil); code != 2 {
		t.Errorf("no command: exit %d, want 2", code)
	}
}

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hibd.env")
	content := "# E2B\nE2B_API_URL=http://10.0.0.1:3000\n\nE2B_API_KEY=\"from-file\"\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"HIBD_CONFIG": path, "E2B_API_KEY": "from-env"}
	cfg, err := loadConfig(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.e2b.APIURL != "http://10.0.0.1:3000" || cfg.e2b.SandboxURL != "http://127.0.0.1:3002" || cfg.e2b.APIKey != "from-env" {
		t.Errorf("config = %+v, want the file's API URL, the default sandbox URL and the env's key", cfg)
	}
	if cfg.stateDir != "/var/lib/hibernaut" || cfg.authorizedKeys != "" || len(cfg.healthURLs) != 3 {
		t.Errorf("config = %+v, want the default state directory and health URLs, and no keys without $HOME", cfg)
	}
	env["HOME"] = "/home/me"
	env["E2B_HEALTH_URLS"] = " http://a/health  http://b/health "
	if cfg, err := loadConfig(func(k string) string { return env[k] }); err != nil || cfg.authorizedKeys != "/home/me/.ssh/authorized_keys" ||
		strings.Join(cfg.healthURLs, ",") != "http://a/health,http://b/health" {
		t.Errorf("config = %+v, %v; want keys from $HOME and the two health URLs", cfg, err)
	}

	delete(env, "E2B_API_KEY")
	if cfg, err := loadConfig(func(k string) string { return env[k] }); err != nil || cfg.e2b.APIKey != "from-file" {
		t.Errorf("config = %+v, %v; want the file's key without quotes", cfg, err)
	}

	env["HIBD_CONFIG"] = filepath.Join(dir, "missing.env")
	if _, err := loadConfig(func(k string) string { return env[k] }); err == nil || !strings.Contains(err.Error(), "E2B_API_KEY") {
		t.Errorf("no key anywhere: err = %v, want one naming E2B_API_KEY", err)
	}

	if err := os.WriteFile(path, []byte("not a pair\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env["HIBD_CONFIG"] = path
	if _, err := loadConfig(func(k string) string { return env[k] }); err == nil {
		t.Error("malformed file: no error")
	}
}

func TestMachineCommands(t *testing.T) {
	e := newEngine(newKey(t))
	a, out, errOut := newApp(t, e)

	code, r, err := runJSON(t, a, out, "new", "app", "-json")
	if code != 0 || err != nil || r.Machine == nil || r.Machine.Name != "app" || r.Machine.Image != "dev" || r.Machine.State != "running" {
		t.Fatalf("new app -json = %d, %+v, %v; stderr %s", code, r, err, errOut)
	}
	id := r.Machine.ID

	code, _, err = runJSON(t, a, out, "new", "-json", "app")
	var remote *hibapi.RemoteError
	if code != 1 || !errors.As(err, &remote) || !strings.Contains(remote.Msg, `"app"`) {
		t.Errorf("new app again = %d, %v; want exit 1 and an error naming app", code, err)
	}

	code, r, err = runJSON(t, a, out, "ls", "-json")
	if code != 0 || err != nil || len(r.Machines) != 1 || r.Machines[0].Name != "app" || r.Machines[0].ID != id {
		t.Errorf("ls -json = %d, %+v, %v", code, r, err)
	}

	if code, _, err := runJSON(t, a, out, "pause", "app", "-json"); code != 0 || err != nil {
		t.Errorf("pause app -json = %d, %v", code, err)
	}
	if m, _ := e.Get(t.Context(), id); m.State != engine.Paused {
		t.Errorf("state after pause = %s", m.State)
	}

	if code, _, err := runJSON(t, a, out, "rm", "-json", "app"); code != 0 || err != nil {
		t.Errorf("rm -json app = %d, %v", code, err)
	}
	code, _, err = runJSON(t, a, out, "rm", "app", "-json")
	if code != 1 || !errors.As(err, &remote) || !strings.Contains(remote.Msg, "no machine") {
		t.Errorf("rm app again = %d, %v; want no machine", code, err)
	}

	out.Reset()
	errOut.Reset()
	if code := a.Run(t.Context(), []string{"new", "web"}); code != 0 || !strings.HasPrefix(out.String(), "made web (") {
		t.Errorf("new web = %d, %q, %q", code, out, errOut)
	}
	errOut.Reset()
	if code := a.Run(t.Context(), []string{"rm", "nope"}); code != 1 || !strings.Contains(errOut.String(), `no machine called "nope"`) {
		t.Errorf("rm nope = %d, stderr %q", code, errOut)
	}
}

func TestNewErrors(t *testing.T) {
	a, out, errOut := newApp(t, newEngine(newKey(t), fake.WithTemplates()))
	code, _, err := runJSON(t, a, out, "new", "app", "-json")
	if code != 1 || err == nil || !strings.Contains(err.Error(), "hib image build dev") {
		t.Errorf("new without the image built = %d, %v", code, err)
	}
	code, _, err = runJSON(t, a, out, "new", "Bad_Name", "-json")
	if code != 1 || err == nil || !strings.Contains(err.Error(), "bad name") {
		t.Errorf("new Bad_Name = %d, %v", code, err)
	}
	code, _, err = runJSON(t, a, out, "new", "app", "-image", "nope", "-json")
	if code != 1 || err == nil || !strings.Contains(err.Error(), `no image "nope"`) {
		t.Errorf("new -image nope = %d, %v", code, err)
	}
	for _, args := range [][]string{{"new"}, {"new", "a", "b"}, {"new", "-bogus", "a"}, {"rm"}, {"pause", "a", "b"}, {"bridge"}, {"ls", "x"}} {
		errOut.Reset()
		if code := a.Run(t.Context(), args); code != 2 || errOut.Len() == 0 {
			t.Errorf("%q = exit %d, stderr %q; want 2 and usage", args, code, errOut)
		}
	}
}

func TestBridge(t *testing.T) {
	hostKey := newKey(t)
	far := make(chan net.Conn, 1)
	e := newEngine(hostKey, fake.WithDialer(func(_ context.Context, _ engine.Machine, port int) (net.Conn, error) {
		if port != images.SSHPort {
			t.Errorf("dialed port %d, want %d", port, images.SSHPort)
		}
		a, b := net.Pipe()
		far <- b
		return a, nil
	}))
	a, _, errOut := newApp(t, e)
	if code := a.Run(t.Context(), []string{"new", "app"}); code != 0 {
		t.Fatalf("new: %s", errOut)
	}
	if err := e.Pause(t.Context(), mustID(t, e, "app")); err != nil {
		t.Fatal(err)
	}

	stdinR, stdinW := io.Pipe()
	outR, outW := io.Pipe()
	defer stdinW.Close()
	a.Stdin, a.Stdout = stdinR, outW
	done := make(chan int, 1)
	go func() { done <- a.Run(t.Context(), []string{"bridge", "app"}); outW.Close() }()

	br := bufio.NewReaderSize(outR, hibapi.MaxHeader)
	key, err := hibapi.ReadBridgeHeader(br)
	if err != nil {
		t.Fatalf("header: %v (stderr %s)", err, errOut)
	}
	if !bytes.Equal(key.Marshal(), hostKey.Marshal()) {
		t.Errorf("header host key = %s, want the machine's", hibapi.FormatHostKey(key))
	}
	machine := <-far
	if m, _ := e.Get(t.Context(), mustID(t, e, "app")); m.State != engine.Running {
		t.Errorf("state = %s, want bridge to resume the machine", m.State)
	}

	go machine.Write([]byte("SSH-2.0-machine\r\n"))
	got := make([]byte, 17)
	if _, err := io.ReadFull(br, got); err != nil || string(got) != "SSH-2.0-machine\r\n" {
		t.Errorf("laptop got %q, %v", got, err)
	}
	go stdinW.Write([]byte("SSH-2.0-laptop\r\n"))
	got = make([]byte, 16)
	if _, err := io.ReadFull(machine, got); err != nil || string(got) != "SSH-2.0-laptop\r\n" {
		t.Errorf("machine got %q, %v", got, err)
	}

	machine.Close()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("bridge exit %d: %s", code, errOut)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bridge didn't end when the machine closed the connection")
	}
}

func TestBridgeUnknownMachine(t *testing.T) {
	a, out, errOut := newApp(t, newEngine(newKey(t)))
	if code := a.Run(t.Context(), []string{"bridge", "nope"}); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if out.Len() != 0 || !strings.Contains(errOut.String(), `no machine called "nope"`) {
		t.Errorf("stdout %q, stderr %q; want nothing on stdout and the reason on stderr", out, errOut)
	}
}

func mustID(t *testing.T, e engine.Engine, name string) string {
	t.Helper()
	ms, err := e.List(t.Context(), engine.ListOptions{Metadata: map[string]string{"hib.name": name}})
	if err != nil || len(ms) != 1 {
		t.Fatalf("machine %q: %v, %v", name, ms, err)
	}
	return ms[0].ID
}

func TestImageBuild(t *testing.T) {
	e := fake.New()
	a, out, errOut := newApp(t, e)
	code, r, err := runJSON(t, a, out, "image", "build", "dev", "-json")
	if code != 0 || err != nil || r.Template != "tmpl-hib-dev" {
		t.Fatalf("image build dev -json = %d, %+v, %v; stderr %s", code, r, err, errOut)
	}
	if !strings.Contains(errOut.String(), "fake: built hib-dev") || !strings.Contains(errOut.String(), "built dev in") {
		t.Errorf("stderr = %q, want the build log", errOut)
	}
	if code, _, err := runJSON(t, a, out, "image", "build", "-force", "dev", "-json"); code != 0 || err != nil {
		t.Errorf("image build -force = %d, %v", code, err)
	}
	built := e.Built()
	if len(built) != 2 || built[0].Name != "hib-dev" || built[0].Force || !built[1].Force {
		t.Errorf("built %+v, want hib-dev, then hib-dev forced", built)
	}
	code, _, err = runJSON(t, a, out, "image", "build", "nope", "-json")
	if code != 1 || err == nil || !strings.Contains(err.Error(), `no image "nope"`) {
		t.Errorf("image build nope = %d, %v", code, err)
	}
	for _, args := range [][]string{{"image"}, {"image", "frob"}, {"image", "build"}} {
		if code := a.Run(t.Context(), args); code != 2 {
			t.Errorf("%q = exit %d, want 2", args, code)
		}
	}
}

func TestReady(t *testing.T) {
	var calls atomic.Int32
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer slow.Close()
	up := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer up.Close()
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer down.Close()

	a, _, errOut := newApp(t, fake.New())
	getenv := a.Getenv
	urls := slow.URL + " " + up.URL
	a.Getenv = func(k string) string {
		if k == "E2B_HEALTH_URLS" {
			return urls
		}
		return getenv(k)
	}
	if code := a.Run(t.Context(), []string{"ready", "-timeout", "20s"}); code != 0 {
		t.Errorf("ready = %d: %s", code, errOut)
	}
	if n := calls.Load(); n != 3 {
		t.Errorf("slow service asked %d times, want 3", n)
	}

	urls = up.URL + " " + down.URL
	if code := a.Run(t.Context(), []string{"ready", "-timeout", "1500ms"}); code != 1 || !strings.Contains(errOut.String(), "502") {
		t.Errorf("ready with a service down = %d, stderr %q; want 1 and the status", code, errOut)
	}
}

func TestVersion(t *testing.T) {
	a, out, _ := newApp(t, fake.New())
	code, r, err := runJSON(t, a, out, "version", "-json")
	if code != 0 || err != nil || r.Version != version.Version {
		t.Errorf("version -json = %d, %+v, %v", code, r, err)
	}
	out.Reset()
	if code := a.Run(t.Context(), []string{"version"}); code != 0 || out.String() != "hibd "+version.Version+"\n" {
		t.Errorf("version = %d, %q", code, out)
	}
	var raw map[string]any
	out.Reset()
	a.Run(t.Context(), []string{"version", "-json"})
	if err := json.Unmarshal(out.Bytes(), &raw); err != nil || raw["v"] != float64(hibapi.Version) {
		t.Errorf("version -json = %s, want v %d", out, hibapi.Version)
	}
}

func TestQuiescing(t *testing.T) {
	e := newEngine(newKey(t))
	a, out, errOut := newApp(t, e)
	if code := a.Run(t.Context(), []string{"new", "app"}); code != 0 {
		t.Fatalf("new: %s", errOut)
	}
	marker := a.Getenv("HIBD_QUIESCING")
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"new", "web", "-json"}, {"image", "build", "dev", "-json"}} {
		code, _, err := runJSON(t, a, out, args...)
		if code != 1 || err == nil || !strings.Contains(err.Error(), "going to sleep") {
			t.Errorf("%q while quiescing: exit %d, %v", args, code, err)
		}
	}
	errOut.Reset()
	if code := a.Run(t.Context(), []string{"bridge", "app"}); code != 1 || !strings.Contains(errOut.String(), "going to sleep") {
		t.Errorf("bridge while quiescing: exit %d, stderr %q", code, errOut)
	}
	// Getting ready to sleep pauses machines, and anything else that doesn't start one works.
	for _, args := range [][]string{{"ls", "-json"}, {"pause", "app", "-json"}, {"rm", "app", "-json"}} {
		if code, _, err := runJSON(t, a, out, args...); code != 0 || err != nil {
			t.Errorf("%q while quiescing: exit %d, %v", args, code, err)
		}
	}
	if len(e.Built()) != 0 {
		t.Errorf("built an image while quiescing")
	}

	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if code, _, err := runJSON(t, a, out, "new", "web", "-json"); code != 0 || err != nil {
		t.Errorf("new after quiescing: exit %d, %v", code, err)
	}
}
