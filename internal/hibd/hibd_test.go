package hibd

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/ca"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/room"
	"github.com/solev/hibernaut/internal/version"
)

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
		"HIBD_CA_CERT":         filepath.Join(state, "ca.crt"),
	}
	if err := os.WriteFile(env["HIBD_CA_CERT"], testCAPEM(t), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	return &App{
		Stdin:     strings.NewReader(""),
		Stdout:    &out,
		Stderr:    &errOut,
		Getenv:    func(k string) string { return env[k] },
		NewEngine: func() (engine.Engine, error) { return e, nil },
		ReadRoom:  func() (room.Host, error) { return roomy, nil },
		Clean: func(context.Context, engine.Engine, time.Time, bool, func(string, ...any)) error {
			return nil
		},
	}, &out, &errOut
}

var (
	roomy = room.Host{PageSize: 2 << 20, Pages: 3200, FreePages: 3125, DiskFree: 80 << 30}
	full  = room.Host{PageSize: 2 << 20, Pages: 3200, FreePages: 53, DiskFree: 80 << 30}
)

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
	e.Add(engine.Machine{ID: "app1", State: engine.Running, StartedAt: now.Add(-2 * time.Hour), EndAt: now,
		Metadata: map[string]string{"hib.name": "app", "hib.image": "dev", "hib.idle": "1h30m0s"}})
	e.Add(engine.Machine{ID: "old1", State: engine.Paused, StartedAt: now.Add(-3 * time.Hour), EndAt: now,
		Metadata: map[string]string{"hib.name": "old", "hib.image": "dev"}})
	e.Add(engine.Machine{ID: "inf1", State: engine.Running, StartedAt: now.Add(-4 * time.Hour), EndAt: now,
		Metadata: map[string]string{"hib.name": "forever", "hib.image": "dev", "hib.idle": "0s"}})

	a, out, _ := newApp(t, e)
	if code := a.Run(t.Context(), []string{"ls", "-state", "running", "-q"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if want := "run1\napp1\ninf1\n"; out.String() != want {
		t.Errorf("ls -state running -q = %q, want %q", out.String(), want)
	}

	out.Reset()
	if code := a.Run(t.Context(), []string{"ls"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 6 || !strings.HasPrefix(lines[0], "NAME") || !strings.Contains(lines[0], " IDLE ") ||
		!strings.HasPrefix(lines[1], "-        run1") || !strings.Contains(lines[1], "2026-09-30T13:00:00Z") ||
		!strings.Contains(lines[2], "zzz1") {
		t.Errorf("ls =\n%s", out.String())
	}
	idle := map[string]string{}
	for _, l := range lines[1:] {
		f := strings.Fields(l)
		idle[f[1]] = f[5]
	}
	for id, want := range map[string]string{"run1": "-", "app1": "1h30m", "old1": "15m", "inf1": "never"} {
		if idle[id] != want {
			t.Errorf("IDLE of %s = %q, want %q; ls =\n%s", id, idle[id], want, out)
		}
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
	content := "# E2B\nE2B_API_URL=http://10.0.0.1:3000\n\nE2B_API_KEY=\"from-file\"\nHIBD_CA_CERT=/from-file/ca.crt\n"
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
	if cfg.caCert != "/from-file/ca.crt" {
		t.Errorf("caCert = %q, want the file's CA path", cfg.caCert)
	}
	env["HOME"] = "/home/me"
	env["E2B_HEALTH_URLS"] = " http://a/health  http://b/health "
	env["HIBD_CA_CERT"] = "/tmp/ca.crt"
	if cfg, err := loadConfig(func(k string) string { return env[k] }); err != nil || cfg.authorizedKeys != "/home/me/.ssh/authorized_keys" ||
		strings.Join(cfg.healthURLs, ",") != "http://a/health,http://b/health" || cfg.caCert != "/tmp/ca.crt" {
		t.Errorf("config = %+v, %v; want keys from $HOME, the two health URLs and the CA path", cfg, err)
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

func TestLoadConfigReadsTheLinkPortOneWay(t *testing.T) {
	for _, tc := range []struct {
		value  string
		wantOn bool
		bad    bool
	}{
		{"", false, false},
		{"0", false, false},
		{"5444", true, false},
		{" 5444 ", true, false},
		{"54x4", false, true},
		{"-1", false, true},
		{"65536", false, true},
	} {
		env := map[string]string{"HIBD_CONFIG": filepath.Join(t.TempDir(), "missing.env"), "E2B_API_KEY": "k", "HIBD_LINK_PORT": tc.value}
		cfg, err := loadConfig(func(k string) string { return env[k] })
		switch {
		case tc.bad && (err == nil || !strings.Contains(err.Error(), "HIBD_LINK_PORT")):
			t.Errorf("HIBD_LINK_PORT=%q: err = %v, want one naming HIBD_LINK_PORT", tc.value, err)
		case !tc.bad && err != nil:
			t.Errorf("HIBD_LINK_PORT=%q: %v", tc.value, err)
		case !tc.bad && cfg.linksConfiguredOn() != tc.wantOn:
			t.Errorf("HIBD_LINK_PORT=%q: links on = %t, want %t", tc.value, cfg.linksConfiguredOn(), tc.wantOn)
		}
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

func TestNewIdle(t *testing.T) {
	e := newEngine(newKey(t))
	a, out, errOut := newApp(t, e)
	for _, tc := range []struct {
		args []string
		want int64
	}{
		{[]string{"new", "app", "-json"}, 15 * 60},
		{[]string{"new", "web", "-idle", "90m", "-json"}, 90 * 60},
		{[]string{"new", "forever", "-idle", "0", "-json"}, 0},
		{[]string{"new", "quick", "-idle", "1m", "-json"}, 60},
	} {
		code, r, err := runJSON(t, a, out, tc.args...)
		if code != 0 || err != nil || r.Machine == nil || r.Machine.IdleSeconds == nil || *r.Machine.IdleSeconds != tc.want {
			t.Errorf("%q = %d, %+v, %v, want idle %ds; stderr %s", tc.args, code, r.Machine, err, tc.want, errOut)
		}
	}
	m, err := e.Get(t.Context(), mustID(t, e, "web"))
	if err != nil || m.EndAt.Sub(m.StartedAt) != 90*time.Minute {
		t.Errorf("web = %+v, %v; want it to pause after 90m", m, err)
	}
	for _, idle := range []string{"30s", "-5m", "1000h"} {
		code, _, err := runJSON(t, a, out, "new", "bad", "-idle", idle, "-json")
		if code != 1 || err == nil || !strings.Contains(err.Error(), "idle") {
			t.Errorf("new -idle %s = %d, %v; want an error about the idle time", idle, code, err)
		}
	}
	if code := a.Run(t.Context(), []string{"new", "bad", "-idle", "soon"}); code != 2 {
		t.Errorf("new -idle soon = %d, want 2", code)
	}
	if ms, _ := e.List(t.Context(), engine.ListOptions{}); len(ms) != 4 {
		t.Errorf("%d machines, want the 4 good ones", len(ms))
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

func webAndOld(t *testing.T) (*fake.Engine, *App, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	e := newEngine(newKey(t), fake.WithDialer(func(context.Context, engine.Machine, int) (net.Conn, error) {
		c, _ := net.Pipe()
		return c, nil
	}))
	a, out, errOut := newApp(t, e)
	for _, name := range []string{"web", "old"} {
		if code := a.Run(t.Context(), []string{"new", name}); code != 0 {
			t.Fatalf("new %s: %s", name, errOut)
		}
	}
	if err := e.Pause(t.Context(), mustID(t, e, "old")); err != nil {
		t.Fatal(err)
	}
	return e, a, out, errOut
}

func wantNoRoom(t *testing.T, what string, code int, msg, name string) {
	t.Helper()
	if code != 1 || !strings.Contains(msg, `no room on the host for "`+name+`"`) || !strings.Contains(msg, "Running now: web.") ||
		!strings.Contains(msg, "hib pause NAME") {
		t.Errorf("%s = exit %d, %q; want the message for a full host", what, code, msg)
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func wantUntouched(t *testing.T, e *fake.Engine) {
	t.Helper()
	ms, err := e.List(t.Context(), engine.ListOptions{})
	if err != nil || len(ms) != 2 {
		t.Fatalf("machines = %+v, %v; want only web and old", ms, err)
	}
	for name, want := range map[string]engine.State{"web": engine.Running, "old": engine.Paused} {
		if m, _ := e.Get(t.Context(), mustID(t, e, name)); m.State != want {
			t.Errorf("%s is %s, want %s", name, m.State, want)
		}
	}
}

func TestHostFull(t *testing.T) {
	e, a, out, errOut := webAndOld(t)
	a.ReadRoom = func() (room.Host, error) { return full, nil }

	code, _, err := runJSON(t, a, out, "new", "app", "-json")
	wantNoRoom(t, "new app", code, errText(err), "app")
	errOut.Reset()
	code = a.Run(t.Context(), []string{"bridge", "old"})
	wantNoRoom(t, "bridge old", code, errOut.String(), "old")
	wantUntouched(t, e)

	errOut.Reset()
	if code := a.Run(t.Context(), []string{"bridge", "web"}); code != 0 {
		t.Errorf("bridge web = exit %d, %s; want it to connect", code, errOut)
	}
}

type refusing struct{ *fake.Engine }

const placeErr = "E2B returned 500: Failed to place sandbox, please retry"

func (refusing) Create(context.Context, engine.CreateOptions) (engine.Machine, error) {
	return engine.Machine{}, errors.New("create hib-dev: " + placeErr)
}

func (refusing) DialWebSocket(context.Context, string, int, time.Duration) (net.Conn, error) {
	return nil, errors.New("dial: resume: " + placeErr)
}

func TestHostFullAfterTheCheck(t *testing.T) {
	e, a, out, errOut := webAndOld(t)
	a.NewEngine = func() (engine.Engine, error) { return refusing{e}, nil }

	a.ReadRoom = readings(roomy, full)
	code, _, err := runJSON(t, a, out, "new", "app", "-json")
	wantNoRoom(t, "new app", code, errText(err), "app")
	a.ReadRoom = readings(roomy, full)
	errOut.Reset()
	code = a.Run(t.Context(), []string{"bridge", "old"})
	wantNoRoom(t, "bridge old", code, errOut.String(), "old")

	a.ReadRoom = readings(roomy)
	if code, _, err := runJSON(t, a, out, "new", "app", "-json"); code != 1 || err == nil || !strings.Contains(err.Error(), placeErr) {
		t.Errorf("new app with room = %d, %v; want E2B's error", code, err)
	}
	wantUntouched(t, e)
}

type timingOut struct {
	*fake.Engine
	id string
}

func (e timingOut) List(ctx context.Context, opts engine.ListOptions) ([]engine.Machine, error) {
	ms, err := e.Engine.List(ctx, opts)
	e.Engine.Pause(ctx, e.id)
	return ms, err
}

func TestHostFullAsAMachinePauses(t *testing.T) {
	e, a, _, errOut := webAndOld(t)
	web := mustID(t, e, "web")
	if err := e.SetTimeout(t.Context(), web, 0); err != nil {
		t.Fatal(err)
	}
	a.NewEngine = func() (engine.Engine, error) { return timingOut{e, web}, nil }
	a.ReadRoom = func() (room.Host, error) { return full, nil }

	code := a.Run(t.Context(), []string{"bridge", "web"})
	if code != 1 || !strings.Contains(errOut.String(), `no room on the host for "web"`) {
		t.Errorf("bridge web = exit %d, %q; want the message for a full host", code, errOut)
	}
	if m, _ := e.Get(t.Context(), web); m.State != engine.Paused {
		t.Errorf("web is %s, want it paused", m.State)
	}
}

func readings(hs ...room.Host) func() (room.Host, error) {
	return func() (room.Host, error) {
		h := hs[0]
		if len(hs) > 1 {
			hs = hs[1:]
		}
		return h, nil
	}
}

type unreachable struct{ *fake.Engine }

func (u unreachable) DialWebSocket(ctx context.Context, id string, port int, timeout time.Duration) (net.Conn, error) {
	c, err := u.Engine.DialWebSocket(ctx, id, port, timeout)
	if err != nil {
		return nil, err
	}
	c.Close()
	return nil, errors.New("dial: connection refused")
}

func TestResumedButUnreachable(t *testing.T) {
	e, a, _, errOut := webAndOld(t)
	a.NewEngine = func() (engine.Engine, error) { return unreachable{e}, nil }
	a.ReadRoom = readings(roomy, full)

	code := a.Run(t.Context(), []string{"bridge", "old"})
	if msg := errOut.String(); code != 1 || !strings.Contains(msg, "connection refused") || strings.Contains(msg, "no room") {
		t.Errorf("bridge old = exit %d, %q; want why it couldn't connect", code, msg)
	}
}

func TestHostUnreadable(t *testing.T) {
	e, a, out, errOut := webAndOld(t)
	a.ReadRoom = func() (room.Host, error) { return room.Host{}, errors.New("no /proc/meminfo") }

	if code, _, err := runJSON(t, a, out, "new", "app", "-json"); code != 1 || err == nil || !strings.Contains(err.Error(), "no /proc/meminfo") {
		t.Errorf("new app = %d, %v; want the reading's error", code, err)
	}
	errOut.Reset()
	if code := a.Run(t.Context(), []string{"bridge", "old"}); code != 1 || !strings.Contains(errOut.String(), "no /proc/meminfo") {
		t.Errorf("bridge old = %d, %s; want the reading's error", code, errOut)
	}
	wantUntouched(t, e)
	errOut.Reset()
	if code := a.Run(t.Context(), []string{"bridge", "web"}); code != 0 {
		t.Errorf("bridge web = exit %d, %s; want it to connect", code, errOut)
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
	caPEM, err := os.ReadFile(a.Getenv("HIBD_CA_CERT"))
	if err != nil {
		t.Fatal(err)
	}
	code, r, err := runJSON(t, a, out, "image", "build", "dev", "-json")
	if code != 0 || err != nil || r.Template != "tmpl-hib-dev" {
		t.Fatalf("image build dev -json = %d, %+v, %v; stderr %s", code, r, err, errOut)
	}
	if !strings.Contains(errOut.String(), "fake: built hib-dev ") || !strings.Contains(errOut.String(), "built dev (medium) in") {
		t.Errorf("stderr = %q, want the build log", errOut)
	}
	if code, _, err := runJSON(t, a, out, "image", "build", "-force", "dev", "-json"); code != 0 || err != nil {
		t.Errorf("image build -force = %d, %v", code, err)
	}
	built := e.Built()
	if len(built) != 6 || built[0].Name != "hib-dev" || built[0].Force || !built[3].Force {
		t.Errorf("built %v, want every size, then every size forced", builtNames(e))
	}
	withCA := 0
	for _, step := range built[0].Steps {
		if strings.Contains(step.Cmd, base64.StdEncoding.EncodeToString(caPEM)) {
			withCA++
		}
	}
	if withCA != 1 {
		t.Errorf("%d of the built spec's steps include the configured CA, want 1", withCA)
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

func TestImageBuildRequiresCA(t *testing.T) {
	a, out, _ := newApp(t, fake.New())
	missing := filepath.Join(t.TempDir(), "missing-ca.crt")
	getenv := a.Getenv
	a.Getenv = func(k string) string {
		if k == "HIBD_CA_CERT" {
			return missing
		}
		return getenv(k)
	}
	code, _, err := runJSON(t, a, out, "image", "build", "dev", "-json")
	if code != 1 || err == nil || !strings.Contains(err.Error(), "no Hibernaut CA at "+missing) ||
		!strings.Contains(err.Error(), "sudo hibd ca init") {
		t.Fatalf("image build without CA = %d, %v", code, err)
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
	if features, ok := raw["features"].([]any); !ok || !slices.Contains(features, any(hibapi.FeatureBuildEvents)) {
		t.Errorf("version -json = %s, want build-events", out)
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

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func testCAPEM(t *testing.T) []byte {
	t.Helper()
	dir := t.TempDir()
	certPath := filepath.Join(dir, "ca.crt")
	keyPath := filepath.Join(dir, "ca.key")
	if _, err := ca.Init(certPath, keyPath, "Hibernaut Test CA"); err != nil {
		t.Fatal(err)
	}
	caPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	return caPEM
}

func startDaemon(t *testing.T, a *App) (logs *syncBuffer, stop func()) {
	t.Helper()
	logs = &syncBuffer{}
	a.Stderr = logs
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan int, 1)
	go func() { done <- a.Run(ctx, []string{"daemon", "-every", "10ms"}) }()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			select {
			case code := <-done:
				if code != 0 {
					t.Errorf("daemon exit %d, want 0; log:\n%s", code, logs)
				}
			case <-time.After(5 * time.Second):
				t.Error("daemon didn't stop")
			}
		})
	}
	t.Cleanup(stop)
	return logs, stop
}

func waitFor(t *testing.T, logs *syncBuffer, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !ok(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s; log:\n%s", what, logs)
		}
	}
}

func busyApp(t *testing.T) (*bounded, *App) {
	t.Helper()
	e := &bounded{Engine: fake.New(fake.WithExec(func(context.Context, engine.Machine, string, engine.ExecRequest) (engine.ExecResult, error) {
		return engine.ExecResult{}, nil
	}))}
	now := time.Now()
	e.Add(engine.Machine{ID: "app1", State: engine.Running, StartedAt: now, EndAt: now.Add(time.Minute),
		Metadata: map[string]string{"hib.name": "app", "hib.image": "dev", "hib.idle": "10m0s"}})
	a, _, _ := newApp(t, e)
	return e, a
}

func TestDaemon(t *testing.T) {
	var agentRunning atomic.Bool
	agentRunning.Store(true)
	var pgreps atomic.Int32
	e := fake.New(fake.WithExec(func(_ context.Context, _ engine.Machine, _ string, req engine.ExecRequest) (engine.ExecResult, error) {
		if req.Cmd != "pgrep" || !strings.Contains(strings.Join(req.Args, " "), "aider") {
			return engine.ExecResult{ExitCode: 2}, nil
		}
		pgreps.Add(1)
		if agentRunning.Load() {
			return engine.ExecResult{}, nil
		}
		return engine.ExecResult{ExitCode: 1}, nil
	}))
	now := time.Now()
	e.Add(engine.Machine{ID: "app1", State: engine.Running, StartedAt: now, EndAt: now.Add(time.Minute),
		Metadata: map[string]string{"hib.name": "app", "hib.image": "dev", "hib.idle": "10m0s"}})
	a, _, _ := newApp(t, e)
	cgroups := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cgroups, "sbx-app1-run1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cgroups, "sbx-app1-run1", "cpu.stat"), []byte("usage_usec 100\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	getenv := a.Getenv
	a.Getenv = func(k string) string {
		switch k {
		case "HIBD_AGENTS":
			return "aider  codex"
		case "HIBD_CGROUPS":
			return cgroups
		}
		return getenv(k)
	}
	logs, stop := startDaemon(t, a)
	endAt := func() time.Time {
		m, err := e.Get(t.Context(), "app1")
		if err != nil {
			t.Fatal(err)
		}
		return m.EndAt
	}
	waitFor(t, logs, "the busy machine runs for its idle time", func() bool { return endAt().After(now.Add(9 * time.Minute)) })

	if err := os.WriteFile(a.Getenv("HIBD_QUIESCING"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, logs, "the daemon notices", func() bool { return strings.Contains(logs.String(), "going to sleep") })
	n := pgreps.Load()
	time.Sleep(50 * time.Millisecond)
	if got := pgreps.Load(); got != n {
		t.Errorf("looked for agents %d times while the host was going to sleep, want none", got-n)
	}
	if err := os.Remove(a.Getenv("HIBD_QUIESCING")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, logs, "the daemon looks again", func() bool { return pgreps.Load() > n })
	agentRunning.Store(false)
	waitFor(t, logs, "the daemon sees the machine idle", func() bool { return strings.Contains(logs.String(), "app is idle") })
	stop()
	for _, want := range []string{"app is busy: a coding agent is running", "app is idle"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log doesn't say %q:\n%s", want, logs)
		}
	}
}

func TestCleanup(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		e := fake.New()
		a, out, _ := newApp(t, e)
		var dryRuns []bool
		before := time.Now()
		a.Clean = func(_ context.Context, ce engine.Engine, now time.Time, n bool, logf func(string, ...any)) error {
			if ce != e || now.Before(before) || now.After(time.Now()) {
				t.Errorf("cleaned up with engine %v at %v, want the app's engine, now", ce, now)
			}
			dryRuns = append(dryRuns, n)
			logf("would free %s", "1.5 GiB")
			return nil
		}
		args := []string{"cleanup"}
		if dryRun {
			args = append(args, "-n")
		}
		if code := a.Run(t.Context(), args); code != 0 {
			t.Errorf("%q: exit %d, want 0", args, code)
		}
		if want := []bool{dryRun}; !slices.Equal(dryRuns, want) {
			t.Errorf("%q: cleaned up with dry runs %v, want %v", args, dryRuns, want)
		}
		if got := out.String(); got != "would free 1.5 GiB\n" {
			t.Errorf("%q: stdout = %q, want cleanup's log", args, got)
		}
	}
}

func TestCleanupFails(t *testing.T) {
	a, _, errOut := newApp(t, fake.New())
	a.Clean = func(context.Context, engine.Engine, time.Time, bool, func(string, ...any)) error {
		return errors.New("no database")
	}
	if code := a.Run(t.Context(), []string{"cleanup"}); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if got := errOut.String(); got != "hibd cleanup: no database\n" {
		t.Errorf("stderr = %q", got)
	}
	if code := a.Run(t.Context(), []string{"cleanup", "now"}); code != 2 {
		t.Errorf("with an argument: exit %d, want 2", code)
	}
}

func TestDaemonCleansUp(t *testing.T) {
	e := fake.New()
	for _, id := range []string{"app1", "web1"} {
		e.Add(engine.Machine{ID: id, State: engine.Paused, StartedAt: time.Now(),
			Metadata: map[string]string{"hib.name": strings.TrimSuffix(id, "1"), "hib.image": "dev"}})
	}
	a, _, _ := newApp(t, e)
	var cleanups atomic.Int32
	a.Clean = func(_ context.Context, _ engine.Engine, _ time.Time, dryRun bool, logf func(string, ...any)) error {
		if dryRun {
			t.Error("the daemon only said what it would delete")
		}
		if cleanups.Add(1) <= 2 {
			return errors.New("no database")
		}
		logf("freed 1.5 GiB in 3 build directories")
		return nil
	}
	logs, _ := startDaemon(t, a)
	waitFor(t, logs, "the daemon cleans up as it starts", func() bool { return cleanups.Load() == 1 })
	time.Sleep(50 * time.Millisecond)
	if n := cleanups.Load(); n != 1 {
		t.Errorf("cleaned up %d times with nothing removed, want once", n)
	}
	for i, id := range []string{"app1", "web1"} {
		if err := e.Kill(t.Context(), id); err != nil {
			t.Fatal(err)
		}
		waitFor(t, logs, "the daemon cleans up after "+id, func() bool { return cleanups.Load() == int32(i+2) })
	}
	waitFor(t, logs, "the daemon logs it", func() bool { return strings.Contains(logs.String(), "cleanup: freed 1.5 GiB") })
	if n := strings.Count(logs.String(), "cleanup: no database; trying again in 1h"); n != 1 {
		t.Errorf("logged the same failed cleanup %d times, want once:\n%s", n, logs)
	}
}

func TestDaemonKeepsBusyMachinesRunningWhileItCleansUp(t *testing.T) {
	e, a := busyApp(t)
	var cleaning atomic.Bool
	a.Clean = func(ctx context.Context, _ engine.Engine, _ time.Time, _ bool, _ func(string, ...any)) error {
		cleaning.Store(true)
		defer cleaning.Store(false)
		<-ctx.Done()
		time.Sleep(20 * time.Millisecond)
		return ctx.Err()
	}
	logs, stop := startDaemon(t, a)
	waitFor(t, logs, "the daemon cleans up", cleaning.Load)
	n := e.count("SetTimeout")
	waitFor(t, logs, "the daemon keeps the busy machine running as it cleans up", func() bool { return e.count("SetTimeout") >= n+2 })
	stop()
	if cleaning.Load() {
		t.Error("the daemon stopped before its cleanup did")
	}
}

func TestDaemonBoundsCallsToE2B(t *testing.T) {
	e, a := busyApp(t)
	logs, stop := startDaemon(t, a)
	waitFor(t, logs, "the daemon keeps the busy machine running and looks for removed ones", func() bool {
		return e.count("SetTimeout") > 0 && e.count("List all") > 0
	})
	stop()
	if got := e.unboundedCalls(); len(got) > 0 {
		t.Errorf("the daemon called %v without a deadline", got)
	}
}

type bounded struct {
	*fake.Engine
	mu        sync.Mutex
	calls     map[string]int
	unbounded map[string]bool
}

func (b *bounded) record(ctx context.Context, call string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.calls == nil {
		b.calls, b.unbounded = map[string]int{}, map[string]bool{}
	}
	b.calls[call]++
	if _, ok := ctx.Deadline(); !ok {
		b.unbounded[call] = true
	}
}

func (b *bounded) count(call string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls[call]
}

func (b *bounded) unboundedCalls() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Sorted(maps.Keys(b.unbounded))
}

func (b *bounded) List(ctx context.Context, opts engine.ListOptions) ([]engine.Machine, error) {
	call := "List all"
	if len(opts.States) > 0 {
		call = "List running"
	}
	b.record(ctx, call)
	return b.Engine.List(ctx, opts)
}

func (b *bounded) SetTimeout(ctx context.Context, id string, timeout time.Duration) error {
	b.record(ctx, "SetTimeout")
	return b.Engine.SetTimeout(ctx, id, timeout)
}

func (b *bounded) Exec(ctx context.Context, id string, req engine.ExecRequest) (engine.ExecResult, error) {
	b.record(ctx, "Exec")
	return b.Engine.Exec(ctx, id, req)
}

func TestDaemonFlags(t *testing.T) {
	a, _, _ := newApp(t, fake.New())
	for _, args := range [][]string{{"daemon", "x"}, {"daemon", "-every", "0s"}, {"daemon", "-every", "soon"}} {
		if code := a.Run(t.Context(), args); code != 2 {
			t.Errorf("%q = exit %d, want 2", args, code)
		}
	}
	getenv := a.Getenv
	a.Getenv = func(k string) string {
		if k == "HIBD_AGENTS" {
			return "codex x|y"
		}
		return getenv(k)
	}
	if code := a.Run(t.Context(), []string{"daemon"}); code != 1 {
		t.Errorf("daemon with a bad agent name = exit %d, want 1", code)
	}
}
