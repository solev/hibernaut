package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/ca"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/hibd"
	"github.com/solev/hibernaut/internal/room"
)

const (
	testHost  = "azureuser@203.0.113.7"
	testHib   = "/opt/hib tools/hib"
	testGroup = "rg-test"
	testVM    = "vm-test"
)

type host struct {
	t              *testing.T
	engine         *fake.Engine
	env            map[string]string
	machineKey     ssh.PublicKey
	far            chan net.Conn
	sshVersion     string
	override       func(words []string, stdout, stderr io.Writer) (int, bool)
	lastStdin      io.Reader
	sshInput       func(ctx context.Context, args []string, stdin io.Reader)
	sudoApp        func(*hibd.App)
	sudoDisconnect <-chan struct{}

	mu       sync.Mutex
	calls    [][]string
	asleep   bool
	booting  int
	states   []string
	readRoom func() (room.Host, error)
	sudoRuns []*hostRun
}

type hostRun struct {
	done           chan struct{}
	code           int
	stdout, stderr bytes.Buffer
}

type hostOutput struct {
	mu sync.Mutex
	w  io.Writer
}

func (w *hostOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.w == nil {
		return len(p), nil
	}
	return w.w.Write(p)
}

func (w *hostOutput) disconnect() {
	w.mu.Lock()
	w.w = nil
	w.mu.Unlock()
}

func newHost(t *testing.T) *host {
	t.Helper()
	keys := filepath.Join(t.TempDir(), "authorized_keys")
	if err := os.WriteFile(keys, ssh.MarshalAuthorizedKey(newKey(t)), 0o600); err != nil {
		t.Fatal(err)
	}
	health := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(health.Close)
	state := t.TempDir()
	caCert := filepath.Join(state, "ca.crt")
	if err := os.WriteFile(caCert, testCAPEM(t), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &host{
		t:          t,
		machineKey: newKey(t),
		far:        make(chan net.Conn, 4),
		sshVersion: "OpenSSH_10.3p1, LibreSSL 3.3.6",
		states:     []string{"VM running"},
		readRoom: func() (room.Host, error) {
			return room.Host{PageSize: 2 << 20, Pages: 3200, FreePages: 3125, DiskFree: 80 << 30}, nil
		},
		env: map[string]string{
			"HIBD_CONFIG":          filepath.Join(state, "none.env"),
			"E2B_API_KEY":          "test",
			"E2B_HEALTH_URLS":      health.URL,
			"HIBD_STATE_DIR":       state,
			"HIBD_AUTHORIZED_KEYS": keys,
			"HIBD_CA_CERT":         caCert,
			"HIBD_QUIESCING":       filepath.Join(state, "quiescing"),
		},
	}
	h.engine = fake.New(
		fake.WithTemplates(images.Template("dev")),
		fake.WithExec(func(_ context.Context, _ engine.Machine, _ string, req engine.ExecRequest) (engine.ExecResult, error) {
			if req.Cmd != images.MachineInit {
				return engine.ExecResult{ExitCode: 127}, nil
			}
			return engine.ExecResult{Stdout: []byte(hibapi.FormatHostKey(h.machineKey) + "\n")}, nil
		}),
		fake.WithDialer(func(context.Context, engine.Machine, int) (net.Conn, error) {
			near, far := net.Pipe()
			h.far <- far
			return near, nil
		}),
	)
	return h
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

func (h *host) record(call []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, slices.Clone(call))
}

func (h *host) takeCalls() [][]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	calls := h.calls
	h.calls = nil
	return calls
}

func (h *host) ssh(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	h.record(args)
	if slices.Equal(args, []string{"-V"}) {
		fmt.Fprintln(stderr, h.sshVersion)
		return 0, nil
	}
	i := slices.Index(args, "--")
	if i < 0 || i+2 >= len(args) {
		h.t.Errorf("ssh %q: want options, --, the host and a command", args)
		return 255, nil
	}
	if args[i+1] != testHost {
		fmt.Fprintf(stderr, "ssh: Could not resolve hostname %s\n", args[i+1])
		return 255, nil
	}
	h.lastStdin = stdin
	if h.sshInput != nil {
		h.sshInput(ctx, args, stdin)
	}
	h.mu.Lock()
	down := h.asleep || h.booting > 0
	if !h.asleep && h.booting > 0 {
		h.booting--
	}
	h.mu.Unlock()
	if down {
		fmt.Fprintln(stderr, "ssh: connect to host 203.0.113.7 port 22: Operation timed out")
		return 255, nil
	}
	// The host's shell splits the command into words.
	words := shellFields(strings.Join(args[i+2:], " "))
	if h.override != nil {
		overrideWords := words
		if len(words) > 0 && words[0] == "hibd" {
			overrideWords = words[1:]
		}
		if code, ok := h.override(overrideWords, stdout, stderr); ok {
			return code, nil
		}
	}
	sudo := h.sudoApp != nil && len(words) >= 3 && slices.Equal(words[:3], []string{"sudo", "-n", "hibd"})
	if sudo {
		words = words[2:]
	}
	if words[0] != "hibd" {
		fmt.Fprintf(stderr, "bash: %s: command not found\n", words[0])
		return 127, nil
	}
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	app := &hibd.App{
		Stdin:     stdin,
		Stdout:    stdout,
		Stderr:    stderr,
		Getenv:    func(k string) string { return h.env[k] },
		NewEngine: func() (engine.Engine, error) { return h.engine, nil },
		ReadRoom:  h.readRoom,
	}
	if sudo {
		app.EUID = func() int { return 0 }
		h.sudoApp(app)
		run := &hostRun{done: make(chan struct{})}
		h.mu.Lock()
		h.sudoRuns = append(h.sudoRuns, run)
		h.mu.Unlock()
		out, diagnostics := &hostOutput{w: stdout}, &hostOutput{w: stderr}
		app.Stdout = io.MultiWriter(&run.stdout, out)
		app.Stderr = io.MultiWriter(&run.stderr, diagnostics)
		defer out.disconnect()
		defer diagnostics.disconnect()
		go func() {
			run.code = app.Run(h.t.Context(), words[1:])
			close(run.done)
		}()
		select {
		case <-ctx.Done():
			return -1, ctx.Err()
		case <-h.sudoDisconnect:
			return 255, nil
		case <-run.done:
			if err := ctx.Err(); err != nil {
				return -1, err
			}
			return run.code, nil
		}
	}
	return app.Run(ctx, words[1:]), nil
}

func (h *host) lastHibd() *hostRun {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.sudoRuns) == 0 {
		return nil
	}
	return h.sudoRuns[len(h.sudoRuns)-1]
}

func (h *host) hibdReturned() bool {
	run := h.lastHibd()
	if run == nil {
		return false
	}
	select {
	case <-run.done:
		return true
	default:
		return false
	}
}

func (h *host) waitHibd() int {
	h.t.Helper()
	run := h.lastHibd()
	if run == nil {
		h.t.Fatal("sudo hibd wasn't started")
	}
	select {
	case <-run.done:
		return run.code
	case <-time.After(10 * time.Second):
		h.t.Fatal("in-process hibd didn't return")
		return -1
	}
}

func shellFields(s string) []string {
	var out []string
	var b strings.Builder
	quoted := false
	inWord := false
	for _, r := range s {
		switch {
		case r == '\'':
			quoted = !quoted
			inWord = true
		case (r == ' ' || r == '\t' || r == '\n') && !quoted:
			if inWord {
				out = append(out, b.String())
				b.Reset()
				inWord = false
			}
		default:
			b.WriteRune(r)
			inWord = true
		}
	}
	if inWord {
		out = append(out, b.String())
	}
	return out
}

func (h *host) az(_ context.Context, args ...string) ([]byte, error) {
	h.record(append([]string{"az"}, args...))
	h.mu.Lock()
	defer h.mu.Unlock()
	if !slices.Contains(args, testGroup) || !slices.Contains(args, testVM) {
		return nil, fmt.Errorf("az %q: ResourceNotFound", args)
	}
	switch args[1] {
	case "get-instance-view":
		state := h.states[0]
		if len(h.states) > 1 {
			h.states = h.states[1:]
		}
		return []byte(state + "\n"), nil
	case "start":
		h.asleep = false
		h.states = []string{"VM running"}
		return nil, nil
	}
	return nil, fmt.Errorf("az %q: unexpected", args)
}

func (h *host) sleep(states ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.asleep = true
	h.booting = 2
	h.states = states
}

type client struct {
	*app
	t           *testing.T
	out, errOut *bytes.Buffer
	dir         string
	sshConfig   string
	execs       [][]string
}

func newClient(t *testing.T, h *host) *client {
	t.Helper()
	var out, errOut bytes.Buffer
	env := map[string]string{"HOME": t.TempDir()}
	c := &client{t: t, out: &out, errOut: &errOut, dir: filepath.Join(t.TempDir(), "hib")}
	c.app = &app{
		stdin:      strings.NewReader(""),
		stdout:     &out,
		stderr:     &errOut,
		getenv:     func(k string) string { return env[k] },
		ssh:        h.ssh,
		execSSH:    func(args []string) error { c.execs = append(c.execs, args); return nil },
		az:         h.az,
		executable: func() (string, error) { return testHib, nil },
		wakePoll:   time.Millisecond,
		now:        time.Now,
	}
	c.sshConfig = filepath.Join(env["HOME"], ".ssh", "config")
	return c
}

func initClient(t *testing.T, h *host, noAzure ...bool) *client {
	t.Helper()
	c := newClient(t, h)
	args := []string{"init", "-host", testHost}
	if len(noAzure) == 0 {
		args = append(args, "-azure-group", testGroup, "-azure-vm", testVM)
	}
	if code := c.hib(args...); code != 0 {
		t.Fatalf("hib init exit %d: %s", code, c.errOut)
	}
	h.takeCalls()
	return c
}

func (c *client) hib(args ...string) int {
	return c.hibContext(c.t.Context(), args...)
}

func (c *client) hibContext(ctx context.Context, args ...string) int {
	c.out.Reset()
	c.errOut.Reset()
	return c.run(ctx, append([]string{"-config", c.dir}, args...))
}

func tableLines(out string) []string {
	table, _, _ := strings.Cut(out, "\n\n")
	return strings.Split(strings.TrimSpace(table), "\n")
}

func remoteCommands(calls [][]string) []string {
	var cmds []string
	for _, call := range calls {
		if i := slices.Index(call, "--"); i >= 0 && call[0] != "az" {
			cmds = append(cmds, strings.Join(call[i+2:], " "))
		}
	}
	return cmds
}

func machineID(t *testing.T, h *host, name string) string {
	t.Helper()
	ms, err := h.engine.List(t.Context(), engine.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.Metadata["hib.name"] == name {
			return m.ID
		}
	}
	t.Fatalf("no machine %q in %v", name, ms)
	return ""
}

func TestMachineCommands(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)

	if code := c.hib("new", "app"); code != 0 {
		t.Fatalf("new exit %d: %s", code, c.errOut)
	}
	if want := "made app; connect with: ssh app.hib\n"; c.out.String() != want {
		t.Errorf("new printed %q, want %q", c.out, want)
	}
	if code := c.hib("new", "-image", "dev", "web"); code != 0 {
		t.Fatalf("new web exit %d: %s", code, c.errOut)
	}
	if got, want := remoteCommands(h.takeCalls()), []string{"hibd new app --image dev --json", "hibd new web --image dev --json"}; !slices.Equal(got, want) {
		t.Errorf("remote commands = %q, want %q", got, want)
	}

	if code := c.hib("ls"); code != 0 {
		t.Fatalf("ls exit %d: %s", code, c.errOut)
	}
	lines := tableLines(c.out.String())
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "NAME") || !strings.HasPrefix(lines[1], "app ") ||
		!strings.Contains(lines[1], " running ") || !strings.Contains(lines[1], " dev ") || !strings.HasPrefix(lines[2], "web ") {
		t.Errorf("ls =\n%s", c.out)
	}

	if code := c.hib("pause", "app"); code != 0 || c.out.String() != "paused app\n" {
		t.Errorf("pause exit %d, printed %q: %s", code, c.out, c.errOut)
	}
	if m, _ := h.engine.Get(t.Context(), machineID(t, h, "app")); m.State != engine.Paused {
		t.Errorf("app is %s after pause", m.State)
	}

	if code := c.hib("rm", "web", "--yes"); code != 0 || c.out.String() != "removed web\n" {
		t.Errorf("rm exit %d, printed %q: %s", code, c.out, c.errOut)
	}
	if code := c.hib("rm", "web", "--yes"); code != 1 || !strings.Contains(c.errOut.String(), `hib: no machine called "web"`) {
		t.Errorf("rm again: exit %d, stderr %q", code, c.errOut)
	}
}

func TestNewIdle(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	for _, args := range [][]string{{"new", "web", "--idle", "90m"}, {"new", "-idle", "0", "forever"}} {
		if code := c.hib(args...); code != 0 {
			t.Fatalf("hib %q: exit %d: %s", args, code, c.errOut)
		}
	}
	if got, want := remoteCommands(h.takeCalls()), []string{
		"hibd new web --image dev --idle 1h30m0s --json",
		"hibd new forever --image dev --idle 0s --json",
	}; !slices.Equal(got, want) {
		t.Errorf("remote commands = %q, want %q", got, want)
	}
	for name, want := range map[string]string{"web": "1h30m0s", "forever": "0s"} {
		if m, _ := h.engine.Get(t.Context(), machineID(t, h, name)); m.Metadata["hib.idle"] != want {
			t.Errorf("%s has idle time %q, want %q", name, m.Metadata["hib.idle"], want)
		}
	}

	if code := c.hib("new", "quick", "--idle", "30s"); code != 1 || !strings.Contains(c.errOut.String(), "bad idle time 30s") {
		t.Errorf("new --idle 30s: exit %d, stderr %q", code, c.errOut)
	}
	if code := c.hib("new", "x", "--idle", "soon"); code != 2 {
		t.Errorf("new --idle soon: exit %d, want 2", code)
	}
	if got, want := remoteCommands(h.takeCalls()), []string{"hibd new quick --image dev --idle 30s --json"}; !slices.Equal(got, want) {
		t.Errorf("remote commands = %q, want %q", got, want)
	}
}

func TestLsShowsWhenMachinesPause(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	for _, m := range []struct {
		name  string
		state engine.State
		idle  string
		left  time.Duration
	}{
		{"tick", engine.Running, "1m0s", 45*time.Second + 600*time.Millisecond},
		{"soon", engine.Running, "15m0s", 12*time.Minute + 30*time.Second},
		{"long", engine.Running, "2h0m0s", time.Hour + 5*time.Minute + 59*time.Second},
		{"late", engine.Running, "15m0s", -time.Second},
		{"never", engine.Running, "0s", 720 * time.Hour},
		{"nap", engine.Paused, "15m0s", -time.Hour},
		{"old", engine.Running, "", 5 * time.Minute},
	} {
		md := map[string]string{"hib.name": m.name, "hib.image": "dev"}
		if m.idle != "" {
			md["hib.idle"] = m.idle
		}
		h.engine.Add(engine.Machine{ID: fake.NewID(), State: m.state, StartedAt: now.Add(-time.Hour), EndAt: now.Add(m.left), Metadata: md})
	}

	h.engine.Add(engine.Machine{ID: fake.NewID(), State: engine.Running, StartedAt: now.Add(-time.Hour), EndAt: now.Add(time.Hour)})

	if code := c.hib("ls"); code != 0 {
		t.Fatalf("ls exit %d: %s", code, c.errOut)
	}
	lines := tableLines(c.out.String())
	if got, want := strings.Fields(lines[0]), []string{"NAME", "STATE", "IMAGE", "SIZE", "IDLE", "PAUSES", "IN", "STARTED", "ID", "FROM"}; !slices.Equal(got, want) {
		t.Errorf("ls header = %q, want %q", got, want)
	}
	want := map[string][2]string{
		"tick":  {"1m", "45s"},
		"soon":  {"15m", "12m"},
		"long":  {"2h", "1h05m"},
		"late":  {"15m", "now"},
		"never": {"never", "never"},
		"nap":   {"15m", "-"},
		"old":   {"15m", "5m"},
		"-":     {"-", "-"},
	}
	for _, line := range lines[1:] {
		f := strings.Fields(line)
		w, ok := want[f[0]]
		if !ok || len(f) != 10 {
			t.Errorf("unexpected ls line %q", line)
			continue
		}
		if f[4] != w[0] || f[5] != w[1] {
			t.Errorf("%s: IDLE %q, PAUSES IN %q; want %q, %q", f[0], f[4], f[5], w[0], w[1])
		}
		delete(want, f[0])
	}
	if len(want) != 0 {
		t.Errorf("ls is missing %v:\n%s", want, c.out)
	}
}

func TestLsShowsRoomOnTheHost(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	if code := c.hib("new", "web"); code != 0 {
		t.Fatalf("new web: exit %d: %s", code, c.errOut)
	}

	reading := func(freePages int64) func() (room.Host, error) {
		return func() (room.Host, error) {
			return room.Host{PageSize: 2 << 20, Pages: 3200, FreePages: freePages, DiskFree: 79 << 30}, nil
		}
	}
	for _, tc := range []struct {
		name     string
		readRoom func() (room.Host, error)
		want     string
	}{
		{"room", reading(2101), "Room for more machines: 4 small, 2 medium or 1 large. 4.1 GiB of 6.2 GiB memory free, 79 GiB disk free."},
		{"no large", reading(1077), "Room for more machines: 2 small or 1 medium. 2.1 GiB of 6.2 GiB memory free, 79 GiB disk free."},
		{"small only", reading(600), "Room for more machines: 1 small. 1.1 GiB of 6.2 GiB memory free, 79 GiB disk free."},
		{"full", reading(53), "No room for another machine: 106 MiB of 6.2 GiB memory free, 79 GiB disk free. " +
			"hib pause NAME frees memory, hib rm NAME --yes frees disk."},
		{"unreadable", func() (room.Host, error) { return room.Host{}, errors.New("no /proc/meminfo") }, ""},
	} {
		h.readRoom = tc.readRoom
		if code := c.hib("ls"); code != 0 {
			t.Fatalf("%s: ls exit %d: %s", tc.name, code, c.errOut)
		}
		table, line, _ := strings.Cut(c.out.String(), "\n\n")
		if !strings.Contains(table, "web") || strings.TrimSpace(line) != tc.want {
			t.Errorf("%s: ls =\n%s\nwant the machines, then %q", tc.name, c.out, tc.want)
		}
	}
}

func TestBadNamesNeverReachTheHost(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	for _, args := range [][]string{
		{"new", "App"},
		{"new", "a;b"},
		{"new", "app", "-image", "dev;reboot"},
		{"new", "app", "-image", "dev --force"},
		{"new"},
		{"new", "a", "b"},
		{"rm", "-oProxyCommand=x"},
		{"rm", "a b"},
		{"pause", "$(id)"},
		{"exec", "a.b", "--", "id"},
		{"exec", "app", "id"},
		{"exec", "app", "--"},
		{"image", "build", "../dev"},
		{"image", "rm", "../dev"},
		{"bridge", "app"},
		{"bridge", "App.hib"},
		{"bridge", "../x.hib"},
		{"bridge", "a.b.hib"},
		{"ls", "extra"},
	} {
		if code := c.hib(args...); code != 2 {
			t.Errorf("hib %q: exit %d, want 2", args, code)
		}
	}
	if calls := h.takeCalls(); len(calls) != 0 {
		t.Errorf("bad names reached the host: %q", calls)
	}
	if len(c.execs) != 0 {
		t.Errorf("bad names ran ssh: %q", c.execs)
	}
}

func TestImageBuild(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	if code := c.hib("image", "build", "dev"); code != 0 {
		t.Fatalf("exit %d: %s", code, c.errOut)
	}
	if c.out.String() != "built image dev in sizes medium, small and large\n" {
		t.Errorf("printed %q", c.out)
	}
	if !strings.Contains(c.errOut.String(), "built dev (medium) in") || !strings.Contains(c.errOut.String(), "built dev (large) in") {
		t.Errorf("stderr = %q, want hibd's build log", c.errOut)
	}
	if code := c.hib("image", "build", "-force", "dev"); code != 0 {
		t.Fatalf("-force exit %d: %s", code, c.errOut)
	}
	if got, want := remoteCommands(h.takeCalls()), []string{"hibd image build dev --json", "hibd image build dev --json --force"}; !slices.Equal(got, want) {
		t.Errorf("remote commands = %q, want %q", got, want)
	}
	built := h.engine.Built()
	if len(built) != 6 || built[0].Force || built[2].Force || !built[3].Force || !built[5].Force {
		t.Errorf("built %d templates, want every size and then every size forced", len(built))
	}
}

func TestExec(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	if code := c.hib("exec", "app", "--", "ls", "-la", "--", "-x"); code != 0 {
		t.Fatalf("exit %d: %s", code, c.errOut)
	}
	if want := []string{"--", "app.hib", "ls", "-la", "--", "-x"}; len(c.execs) != 1 || !slices.Equal(c.execs[0], want) {
		t.Errorf("ran ssh %q, want %q", c.execs, want)
	}

	h2 := newHost(t)
	c2 := newClient(t, h2)
	own := filepath.Join(t.TempDir(), "my ssh config")
	if code := c2.hib("init", "-host", testHost, "-ssh-config", own); code != 0 {
		t.Fatalf("init exit %d: %s", code, c2.errOut)
	}
	if code := c2.hib("exec", "app", "--", "id"); code != 0 {
		t.Fatalf("exit %d: %s", code, c2.errOut)
	}
	if want := []string{"-F", own, "--", "app.hib", "id"}; len(c2.execs) != 1 || !slices.Equal(c2.execs[0], want) {
		t.Errorf("ran ssh %q, want %q", c2.execs, want)
	}

	c.execSSH = func([]string) error { return errors.New(`exec: "ssh": executable file not found in $PATH`) }
	if code := c.hib("exec", "app", "--", "id"); code != 1 || !strings.Contains(c.errOut.String(), "not found") {
		t.Errorf("no ssh: exit %d, stderr %q", code, c.errOut)
	}
}

func TestOutputFromTheHostIsTerminalSafe(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	h.override = func(words []string, stdout, stderr io.Writer) (int, bool) {
		fmt.Fprint(stderr, "log \x1b]0;pwned\x07 \x1b[2J\n")
		switch words[0] {
		case "rm":
			fmt.Fprintln(stdout, `{"v":1,"error":"bad \u001b[31mthing\u009b"}`)
			return 1, true
		case "ls":
			fmt.Fprintln(stdout, `{"v":1,"machines":[{"name":"x\u001b[2Jy","id":"i\u0007d","state":"running","image":"dev"}]}`)
			return 0, true
		}
		return 0, false
	}
	for _, args := range [][]string{{"rm", "app", "--yes"}, {"ls"}} {
		c.hib(args...)
		for _, s := range []string{c.out.String(), c.errOut.String()} {
			if strings.ContainsAny(s, "\x1b\x07\u009b") {
				t.Errorf("hib %q printed control characters: %q", args, s)
			}
		}
	}
	if !strings.Contains(c.out.String(), `x\x1b[2Jy`) {
		t.Errorf("ls = %q, want the escaped name", c.out)
	}
}

func TestRemoteFailures(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h, true)
	h.override = func(words []string, stdout, _ io.Writer) (int, bool) {
		switch words[0] {
		case "pause":
			return 127, true
		case "rm":
			fmt.Fprint(stdout, "not json")
			return 1, true
		case "ls":
			fmt.Fprint(stdout, `{"v":2}`)
			return 0, true
		case "new":
			return 3, true
		}
		return 0, false
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"pause", "app"}, "hibd isn't installed on " + testHost},
		{[]string{"rm", "app", "--yes"}, "unreadable reply from hibd"},
		{[]string{"ls"}, "different versions"},
		{[]string{"new", "app"}, "hibd on " + testHost + " exited 3"},
	} {
		if code := c.hib(tc.args...); code != 1 || !strings.Contains(c.errOut.String(), tc.want) {
			t.Errorf("hib %q: exit %d, stderr %q; want 1 and %q", tc.args, code, c.errOut, tc.want)
		}
	}

	h.sleep("VM deallocated")
	if code := c.hib("ls"); code != 1 || !strings.Contains(c.errOut.String(), "Operation timed out") ||
		!strings.Contains(c.errOut.String(), "couldn't reach "+testHost) {
		t.Errorf("unreachable without Azure: exit %d, stderr %q", code, c.errOut)
	}
	for _, call := range h.takeCalls() {
		if call[0] == "az" {
			t.Errorf("called az without Azure settings: %q", call)
		}
	}
}

func TestBridge(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	if code := c.hib("new", "app"); code != 0 {
		t.Fatalf("new: %s", c.errOut)
	}
	if err := h.engine.Pause(t.Context(), machineID(t, h, "app")); err != nil {
		t.Fatal(err)
	}
	h.takeCalls()

	stdinR, stdinW := io.Pipe()
	defer stdinW.Close()
	outR, outW := io.Pipe()
	c.stdin, c.stdout = stdinR, outW
	done := make(chan int, 1)
	go func() {
		done <- c.run(t.Context(), []string{"-config", c.dir, "bridge", "app.hib"})
		outW.Close()
	}()

	var machine net.Conn
	select {
	case machine = <-h.far:
	case <-time.After(5 * time.Second):
		t.Fatal("hibd bridge didn't connect to the machine")
	}
	machine.SetDeadline(time.Now().Add(5 * time.Second))

	go machine.Write([]byte("SSH-2.0-machine\r\n"))
	got := make([]byte, 17)
	if _, err := io.ReadFull(outR, got); err != nil || string(got) != "SSH-2.0-machine\r\n" {
		t.Fatalf("laptop got %q, %v", got, err)
	}

	pin := filepath.Join(c.dir, "known_hosts", "app.hib")
	data, err := os.ReadFile(pin)
	if want := "app.hib " + hibapi.FormatHostKey(h.machineKey) + "\n"; err != nil || string(data) != want {
		t.Errorf("pin = %q, %v; want %q", data, err, want)
	}
	if fi, err := os.Stat(pin); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("pin mode: %v, %v", fi.Mode(), err)
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
			t.Errorf("bridge exit %d: %s", code, c.errOut)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bridge didn't end when the machine closed the connection")
	}
	if got := remoteCommands(h.takeCalls()); !slices.Equal(got, []string{"hibd bridge app"}) {
		t.Errorf("remote commands = %q", got)
	}

	c.stdout = c.out
	if code := c.hib("rm", "app", "--yes"); code != 0 {
		t.Fatalf("rm: %s", c.errOut)
	}
	if _, err := os.Stat(pin); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("rm left the pin: %v", err)
	}
}

func TestBridgeFailures(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	if code := c.hib("bridge", "nope.hib"); code != 1 {
		t.Errorf("unknown machine: exit %d", code)
	}
	if !strings.Contains(c.errOut.String(), `no machine called "nope"`) || strings.Contains(c.errOut.String(), "hib:") {
		t.Errorf("unknown machine: stderr %q, want only hibd's message", c.errOut)
	}
	if _, err := os.Stat(filepath.Join(c.dir, "known_hosts", "nope.hib")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("pinned a key for an unknown machine: %v", err)
	}

	h.override = func(words []string, stdout, _ io.Writer) (int, bool) {
		if words[0] == "bridge" {
			fmt.Fprintln(stdout, `{"v":1,"hostKey":"ssh-rsa AAAA"}`)
			return 0, true
		}
		return 0, false
	}
	if code := c.hib("bridge", "app.hib"); code != 1 || !strings.Contains(c.errOut.String(), "bad host key") {
		t.Errorf("bad header: exit %d, stderr %q", code, c.errOut)
	}
	h.override = func(words []string, _, _ io.Writer) (int, bool) { return 0, words[0] == "bridge" }
	if code := c.hib("bridge", "app.hib"); code != 1 || !strings.Contains(c.errOut.String(), "closed the bridge") {
		t.Errorf("no header: exit %d, stderr %q", code, c.errOut)
	}
	if entries, _ := os.ReadDir(filepath.Join(c.dir, "known_hosts")); len(entries) != 0 {
		t.Errorf("failed bridges left pins: %v", entries)
	}
}

func TestWake(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	h.sleep("VM deallocating", "VM deallocating", "VM deallocated")
	if code := c.hib("new", "app"); code != 0 {
		t.Fatalf("exit %d: %s", code, c.errOut)
	}
	if !strings.Contains(c.errOut.String(), "the host is asleep (VM deallocating)") || !strings.Contains(c.errOut.String(), "the host is up after") {
		t.Errorf("stderr = %q", c.errOut)
	}
	var az, cmds []string
	for _, call := range h.takeCalls() {
		if call[0] == "az" {
			az = append(az, call[2])
		} else {
			cmds = append(cmds, remoteCommands([][]string{call})...)
		}
	}
	if want := []string{"get-instance-view", "get-instance-view", "get-instance-view", "start"}; !slices.Equal(az, want) {
		t.Errorf("az calls = %q, want %q: wait for the deallocation, then start", az, want)
	}
	ready := "hibd ready --timeout 5m"
	if want := []string{"hibd new app --image dev --json", ready, ready, ready, "hibd new app --image dev --json"}; !slices.Equal(cmds, want) {
		t.Errorf("remote commands = %q, want %q", cmds, want)
	}

	h.mu.Lock()
	h.asleep = true
	h.mu.Unlock()
	if code := c.hib("ls"); code != 1 || !strings.Contains(c.errOut.String(), "couldn't reach") {
		t.Errorf("running but unreachable: exit %d, stderr %q", code, c.errOut)
	}
	for _, call := range h.takeCalls() {
		if call[0] == "az" && call[2] == "start" {
			t.Errorf("started a running VM")
		}
	}
}

func TestWakeForBridge(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	if code := c.hib("new", "app"); code != 0 {
		t.Fatalf("new: %s", c.errOut)
	}
	h.sleep("VM deallocated")
	stdinR, stdinW := io.Pipe()
	defer stdinW.Close()
	c.stdin = stdinR
	outR, outW := io.Pipe()
	c.stdout = outW
	done := make(chan int, 1)
	go func() {
		done <- c.run(t.Context(), []string{"-config", c.dir, "bridge", "app.hib"})
		outW.Close()
	}()

	go stdinW.Write([]byte("SSH-2.0-laptop\r\n"))
	var machine net.Conn
	select {
	case machine = <-h.far:
	case <-time.After(5 * time.Second):
		t.Fatal("hibd bridge didn't connect to the machine")
	}
	machine.SetDeadline(time.Now().Add(5 * time.Second))
	go machine.Write([]byte("SSH-2.0-machine\r\n"))
	got := make([]byte, 17)
	if _, err := io.ReadFull(outR, got); err != nil || string(got) != "SSH-2.0-machine\r\n" {
		t.Fatalf("laptop got %q, %v", got, err)
	}
	got = make([]byte, 16)
	if _, err := io.ReadFull(machine, got); err != nil || string(got) != "SSH-2.0-laptop\r\n" {
		t.Errorf("machine got %q, %v", got, err)
	}
	machine.Close()
	if code := <-done; code != 0 {
		t.Errorf("exit %d: %s", code, c.errOut)
	}
	if !strings.Contains(c.errOut.String(), "the host is up after") {
		t.Errorf("stderr = %q", c.errOut)
	}
}

func TestBridgeKeepsBytesAfterTheHeader(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	h.override = func(words []string, stdout, _ io.Writer) (int, bool) {
		if words[0] != "bridge" {
			return 0, false
		}
		var b bytes.Buffer
		if err := hibapi.WriteBridgeHeader(&b, h.machineKey); err != nil {
			t.Error(err)
		}

		stdout.Write(append(b.Bytes(), "SSH-2.0-machine\r\n"...))
		return 0, true
	}
	if code := c.hib("bridge", "app.hib"); code != 0 {
		t.Fatalf("exit %d: %s", code, c.errOut)
	}
	if c.out.String() != "SSH-2.0-machine\r\n" {
		t.Errorf("laptop got %q", c.out)
	}
}

func TestWakeFailures(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	h.sleep("VM deallocated")
	h.override = func(words []string, _, _ io.Writer) (int, bool) { return 1, words[0] == "ready" }
	if code := c.hib("ls"); code != 1 || !strings.Contains(c.errOut.String(), "hibd ready exited 1") {
		t.Errorf("ready fails: exit %d, stderr %q", code, c.errOut)
	}

	h.sleep("VM deallocated")
	c.az = func(context.Context, ...string) ([]byte, error) { return nil, errors.New("az: please run az login") }
	if code := c.hib("ls"); code != 1 || !strings.Contains(c.errOut.String(), "az login") {
		t.Errorf("az fails: exit %d, stderr %q", code, c.errOut)
	}
}
