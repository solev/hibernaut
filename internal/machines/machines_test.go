package machines_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/machines"
	"github.com/solev/hibernaut/internal/room"
)

// guest plays machine-init in fake machines: each machine gets a host key on its first run,
// and keeps it.
type guest struct {
	mu       sync.Mutex
	hostKeys map[string]ssh.PublicKey
	keys     map[string]string
	runs     map[string]int
	// result, if set, replaces machine-init's.
	result func() (engine.ExecResult, error)
	delay  time.Duration
}

func (g *guest) exec(_ context.Context, m engine.Machine, _ string, req engine.ExecRequest) (engine.ExecResult, error) {
	if req.Cmd != images.MachineInit || req.User != "root" || len(req.Args) != 1 {
		return engine.ExecResult{ExitCode: 127, Stderr: []byte(fmt.Sprintf("unexpected %+v", req))}, nil
	}
	time.Sleep(g.delay)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.runs[m.ID]++
	if g.result != nil {
		return g.result()
	}
	keys, err := base64.StdEncoding.DecodeString(req.Args[0])
	if err != nil {
		return engine.ExecResult{ExitCode: 1, Stderr: []byte("base64: invalid input")}, nil
	}
	g.keys[m.ID] = string(keys)
	if g.hostKeys[m.ID] == nil {
		g.hostKeys[m.ID] = newKey()
	}
	return engine.ExecResult{Stdout: []byte(hibapi.FormatHostKey(g.hostKeys[m.ID]) + "\n")}, nil
}

func (g *guest) runCount(id string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.runs[id]
}

func newKey() ssh.PublicKey {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		panic(err)
	}
	return key
}

// roomy reads a host with room for three dev machines.
func roomy() (room.Host, error) {
	return room.Host{PageSize: 2 << 20, Pages: 3200, FreePages: 3125, DiskFree: 80 << 30}, nil
}

type env struct {
	m      *machines.Manager
	e      *fake.Engine
	g      *guest
	dir    string
	keys   string
	myKey  string
	dialed chan net.Conn
}

func newEnv(t *testing.T, opts ...fake.Option) *env {
	t.Helper()
	return newEnvAt(t, time.Now, opts...)
}

// newEnvAt is newEnv with the engine and the Manager on clock now.
func newEnvAt(t *testing.T, now func() time.Time, opts ...fake.Option) *env {
	t.Helper()
	g := &guest{hostKeys: map[string]ssh.PublicKey{}, keys: map[string]string{}, runs: map[string]int{}}
	dir := t.TempDir()
	keys := filepath.Join(t.TempDir(), "authorized_keys")
	myKey := string(ssh.MarshalAuthorizedKey(newKey()))
	if err := os.WriteFile(keys, []byte(myKey), 0o600); err != nil {
		t.Fatal(err)
	}
	dialed := make(chan net.Conn, 10)
	opts = append([]fake.Option{
		fake.WithTemplates(images.Template("dev")),
		fake.WithExec(g.exec),
		fake.WithDialer(func(_ context.Context, m engine.Machine, port int) (net.Conn, error) {
			if port != images.SSHPort {
				return nil, fmt.Errorf("dialed port %d", port)
			}
			a, b := net.Pipe()
			dialed <- b
			return a, nil
		}),
		fake.WithClock(now),
	}, opts...)
	e := fake.New(opts...)
	m := machines.New(machines.Config{Engine: e, StateDir: dir, AuthorizedKeys: keys, Now: now, ReadRoom: roomy})
	return &env{m: m, e: e, g: g, dir: dir, keys: keys, myKey: myKey, dialed: dialed}
}

func (v *env) create(t *testing.T, name string) hibapi.Machine {
	t.Helper()
	return v.createIdle(t, name, machines.DefaultIdle)
}

func (v *env) createIdle(t *testing.T, name string, idle time.Duration) hibapi.Machine {
	t.Helper()
	m, err := v.m.Create(t.Context(), name, "dev", idle)
	if err != nil {
		t.Fatalf("Create(%q, idle %v): %v", name, idle, err)
	}
	return m
}

func (v *env) get(t *testing.T, id string) engine.Machine {
	t.Helper()
	em, err := v.e.Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return em
}

func (v *env) pin(t *testing.T, id string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(v.dir, "host-keys", id))
	if err != nil {
		t.Fatalf("pin of %s: %v", id, err)
	}
	return string(data)
}

func (v *env) machines(t *testing.T) []engine.Machine {
	t.Helper()
	ms, err := v.e.List(t.Context(), engine.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

func TestCreate(t *testing.T) {
	v := newEnv(t)
	m := v.createIdle(t, "app", 90*time.Minute)
	if m.Name != "app" || m.Image != "dev" || m.State != "running" || m.ID == "" {
		t.Errorf("Create = %+v", m)
	}
	if m.IdleSeconds == nil || *m.IdleSeconds != 90*60 {
		t.Errorf("IdleSeconds = %v, want 5400", m.IdleSeconds)
	}

	em := v.get(t, m.ID)
	if em.Template != "hib-dev" || em.Metadata["hib.name"] != "app" || em.Metadata["hib.image"] != "dev" || em.Metadata["hib.idle"] != "1h30m0s" {
		t.Errorf("engine machine = %+v, want template hib-dev and hib.* metadata", em)
	}
	if got := em.EndAt.Sub(em.StartedAt); got != 90*time.Minute {
		t.Errorf("timeout = %v, want the idle time, 90m", got)
	}
	if got := v.g.keys[m.ID]; got != v.myKey {
		t.Errorf("machine-init got keys %q, want %q", got, v.myKey)
	}
	if want := hibapi.FormatHostKey(v.g.hostKeys[m.ID]) + "\n"; v.pin(t, m.ID) != want {
		t.Errorf("pin = %q, want %q", v.pin(t, m.ID), want)
	}
	fi, err := os.Stat(filepath.Join(v.dir, "host-keys", m.ID))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("pin mode = %v, %v; want 0600", fi.Mode(), err)
	}
	if fi, err := os.Stat(filepath.Join(v.dir, "host-keys")); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("host-keys mode = %v, %v; want 0700", fi.Mode(), err)
	}
	if left, _ := filepath.Glob(filepath.Join(v.dir, "host-keys", ".pin-*")); len(left) > 0 {
		t.Errorf("temp files left: %v", left)
	}
}

func TestCreateNeverPauses(t *testing.T) {
	v := newEnv(t)
	m := v.createIdle(t, "app", 0)
	if m.IdleSeconds == nil || *m.IdleSeconds != 0 {
		t.Errorf("IdleSeconds = %v, want 0 (never)", m.IdleSeconds)
	}
	em := v.get(t, m.ID)
	if em.Metadata["hib.idle"] != "0s" {
		t.Errorf("hib.idle = %q, want 0s", em.Metadata["hib.idle"])
	}
	if got := em.EndAt.Sub(em.StartedAt); got != machines.MaxRun {
		t.Errorf("timeout = %v, want the longest E2B allows, %v", got, machines.MaxRun)
	}
}

func TestIdle(t *testing.T) {
	named := func(idle string) engine.Machine {
		md := map[string]string{"hib.name": "app"}
		if idle != "" {
			md["hib.idle"] = idle
		}
		return engine.Machine{ID: "x", Metadata: md}
	}
	for _, tc := range []struct {
		name string
		m    engine.Machine
		idle time.Duration
		ok   bool
	}{
		{"set", named("5m0s"), 5 * time.Minute, true},
		{"never", named("0s"), 0, true},
		{"made before M2", named(""), machines.DefaultIdle, true},
		{"garbage", named("soon"), machines.DefaultIdle, true},
		{"negative", named("-5m"), machines.DefaultIdle, true},
		{"too long", named("1000h"), machines.DefaultIdle, true},
		{"not hibd's", engine.Machine{ID: "x", Metadata: map[string]string{"hib.idle": "5m0s"}}, 0, false},
		{"bad name", engine.Machine{ID: "x", Metadata: map[string]string{"hib.name": "A B"}}, 0, false},
	} {
		idle, ok := machines.Idle(tc.m)
		if idle != tc.idle || ok != tc.ok {
			t.Errorf("%s: Idle = %v, %v; want %v, %v", tc.name, idle, ok, tc.idle, tc.ok)
		}
	}
}

func TestCreateNameTaken(t *testing.T) {
	v := newEnv(t)
	v.create(t, "app")
	_, err := v.m.Create(t.Context(), "app", "dev", machines.DefaultIdle)
	if !errors.Is(err, machines.ErrExists) {
		t.Errorf("second Create = %v, want ErrExists", err)
	}
	if n := len(v.machines(t)); n != 1 {
		t.Errorf("%d machines, want 1", n)
	}
	v.create(t, "other")
}

func TestCreateRaceKeepsNamesUnique(t *testing.T) {
	v := newEnv(t)
	v.g.delay = 20 * time.Millisecond
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = v.m.Create(t.Context(), "app", "dev", machines.DefaultIdle)
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, machines.ErrExists):
			t.Errorf("Create: %v", err)
		}
	}
	if ok != 1 || len(v.machines(t)) != 1 {
		t.Errorf("%d Creates succeeded and %d machines exist, want 1 and 1", ok, len(v.machines(t)))
	}
}

func TestCreateRejects(t *testing.T) {
	v := newEnv(t)
	for _, tc := range []struct{ name, image, want string }{
		{"App", "dev", "bad name"},
		{"../x", "dev", "bad name"},
		{"", "dev", "bad name"},
		{"app", "nope", `no image "nope"`},
		{"app", "", `no image ""`},
	} {
		_, err := v.m.Create(t.Context(), tc.name, tc.image, machines.DefaultIdle)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Create(%q, %q) = %v, want an error containing %q", tc.name, tc.image, err, tc.want)
		}
	}
	for _, idle := range []time.Duration{-time.Minute, machines.MaxRun + time.Second} {
		if _, err := v.m.Create(t.Context(), "app", "dev", idle); err == nil || !strings.Contains(err.Error(), "idle") {
			t.Errorf("Create(idle %v) = %v, want an error about the idle time", idle, err)
		}
	}
	if n := len(v.machines(t)); n != 0 {
		t.Errorf("%d machines, want none", n)
	}
}

func TestCreateImageNotBuilt(t *testing.T) {
	v := newEnv(t, fake.WithTemplates())
	_, err := v.m.Create(t.Context(), "app", "dev", machines.DefaultIdle)
	if err == nil || !strings.Contains(err.Error(), "hib image build dev") {
		t.Errorf("Create = %v, want a hint to build the image", err)
	}
}

func TestCreateKillsMachineThatFailsSetup(t *testing.T) {
	for name, result := range map[string]func() (engine.ExecResult, error){
		"exit": func() (engine.ExecResult, error) {
			return engine.ExecResult{ExitCode: 1, Stderr: []byte("sshd: \x1b]52;c;evil\x07 bad config")}, nil
		},
		"exec error": func() (engine.ExecResult, error) { return engine.ExecResult{}, errors.New("envd is down") },
		"no key":     func() (engine.ExecResult, error) { return engine.ExecResult{Stdout: []byte("hello\n")}, nil },
		"two keys": func() (engine.ExecResult, error) {
			return engine.ExecResult{Stdout: []byte(hibapi.FormatHostKey(newKey()) + "\n" + hibapi.FormatHostKey(newKey()) + "\n")}, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			v := newEnv(t)
			v.g.result = result
			_, err := v.m.Create(t.Context(), "app", "dev", machines.DefaultIdle)
			if err == nil {
				t.Fatal("Create = nil, want an error")
			}
			if strings.ContainsRune(err.Error(), 0x1b) {
				t.Errorf("error has a raw escape from the machine: %q", err)
			}
			if n := len(v.machines(t)); n != 0 {
				t.Errorf("%d machines left, want the failed one killed", n)
			}
			if pins, _ := os.ReadDir(filepath.Join(v.dir, "host-keys")); len(pins) != 0 {
				t.Errorf("pins left: %v", pins)
			}
			v.g.result = nil
			v.create(t, "app")
		})
	}
}

func TestCreateCancelledStillKills(t *testing.T) {
	v := newEnv(t)
	ctx, cancel := context.WithCancel(t.Context())
	v.g.result = func() (engine.ExecResult, error) {
		cancel()
		return engine.ExecResult{}, context.Canceled
	}
	if _, err := v.m.Create(ctx, "app", "dev", machines.DefaultIdle); err == nil {
		t.Fatal("Create = nil, want an error")
	}
	if n := len(v.machines(t)); n != 0 {
		t.Errorf("%d machines left, want the failed one killed", n)
	}
}

func TestAuthorizedKeysFiltered(t *testing.T) {
	v := newEnv(t)
	ed := ssh.MarshalAuthorizedKey(newKey())
	ecPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecPub, err := ssh.NewPublicKey(&ecPriv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	ec := ssh.MarshalAuthorizedKey(ecPub)

	caSigner, err := ssh.NewSignerFromKey(ecPriv)
	if err != nil {
		t.Fatal(err)
	}
	cert := &ssh.Certificate{Key: newKey(), CertType: ssh.UserCert, ValidPrincipals: []string{"user"}, ValidBefore: ssh.CertTimeInfinity}
	if err := cert.SignCert(rand.Reader, caSigner); err != nil {
		t.Fatal(err)
	}
	edBlob := strings.Fields(string(ssh.MarshalAuthorizedKey(newKey())))[1]
	trim := func(b []byte) string { return strings.TrimSuffix(string(b), "\n") }

	file := strings.Join([]string{
		"# my keys",
		"",
		trim(ed) + " me@laptop",
		trim(ec) + " work\r",
		trim(ed),
		`command="/usr/bin/backup" ` + strings.Fields(string(ssh.MarshalAuthorizedKey(newKey())))[0] + " " + strings.Fields(string(ssh.MarshalAuthorizedKey(newKey())))[1],
		"restrict " + trim(ssh.MarshalAuthorizedKey(newKey())),
		`from="10.0.0.1" ` + trim(ssh.MarshalAuthorizedKey(newKey())),
		"cert-authority " + trim(ec),
		trim(ssh.MarshalAuthorizedKey(cert)),
		"ssh-rsa " + edBlob,
		"restrict " + edBlob,
		"garbage",
	}, "\n")
	if err := os.WriteFile(v.keys, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	m := v.create(t, "app")
	if got, want := v.g.keys[m.ID], string(ed)+string(ec); got != want {
		t.Errorf("machine-init got keys\n%s\nwant\n%s", got, want)
	}
}

func TestAuthorizedKeysUnusable(t *testing.T) {
	for name, content := range map[string]*string{
		"missing":      nil,
		"empty":        ptr(""),
		"only options": ptr("restrict " + string(ssh.MarshalAuthorizedKey(newKey()))),
		"too big":      ptr(string(ssh.MarshalAuthorizedKey(newKey())) + strings.Repeat("#", 64<<10)),
	} {
		t.Run(name, func(t *testing.T) {
			v := newEnv(t)
			os.Remove(v.keys)
			if content != nil {
				if err := os.WriteFile(v.keys, []byte(*content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := v.m.Create(t.Context(), "app", "dev", machines.DefaultIdle); err == nil {
				t.Error("Create = nil, want an error")
			}
			if n := len(v.machines(t)); n != 0 {
				t.Errorf("%d machines, want none", n)
			}
		})
	}
}

func TestAuthorizedKeysNotConfigured(t *testing.T) {
	v := newEnv(t)
	m := machines.New(machines.Config{Engine: v.e, StateDir: v.dir})
	if _, err := m.Create(t.Context(), "app", "dev", machines.DefaultIdle); err == nil || !strings.Contains(err.Error(), "no authorized_keys file") {
		t.Errorf("Create = %v, want an error saying there's no authorized_keys file", err)
	}
	if n := len(v.machines(t)); n != 0 {
		t.Errorf("%d machines, want none", n)
	}
}

func ptr[T any](v T) *T { return &v }

func TestStateDirChecked(t *testing.T) {
	for name, setup := range map[string]func(t *testing.T, dir string) string{
		"world-writable": func(t *testing.T, dir string) string {
			if err := os.Chmod(dir, 0o777); err != nil {
				t.Fatal(err)
			}
			return dir
		},
		"group-writable host-keys": func(t *testing.T, dir string) string {
			if err := os.Mkdir(filepath.Join(dir, "host-keys"), 0o770); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(filepath.Join(dir, "host-keys"), 0o770); err != nil {
				t.Fatal(err)
			}
			return dir
		},
		"symlink": func(t *testing.T, dir string) string {
			link := filepath.Join(t.TempDir(), "link")
			if err := os.Symlink(dir, link); err != nil {
				t.Fatal(err)
			}
			return link
		},
		"host-keys symlink": func(t *testing.T, dir string) string {
			if err := os.Symlink(t.TempDir(), filepath.Join(dir, "host-keys")); err != nil {
				t.Fatal(err)
			}
			return dir
		},
		"missing": func(t *testing.T, dir string) string { return filepath.Join(dir, "nope") },
		"file": func(t *testing.T, dir string) string {
			f := filepath.Join(dir, "file")
			if err := os.WriteFile(f, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			return f
		},
	} {
		t.Run(name, func(t *testing.T) {
			v := newEnv(t)
			dir := setup(t, v.dir)
			m := machines.New(machines.Config{Engine: v.e, StateDir: dir, AuthorizedKeys: v.keys})
			if _, err := m.Create(t.Context(), "app", "dev", machines.DefaultIdle); err == nil || !strings.Contains(err.Error(), "state directory") {
				t.Errorf("Create = %v, want a state directory error", err)
			}
			if _, err := m.Dial(t.Context(), "app"); err == nil {
				t.Error("Dial = nil, want an error")
			}
			if n := len(v.machines(t)); n != 0 {
				t.Errorf("%d machines, want none", n)
			}
		})
	}
}

func TestList(t *testing.T) {
	v := newEnv(t)
	app := v.create(t, "app")
	v.e.Add(engine.Machine{ID: "unnamed1", State: engine.Paused, Template: "base"})
	v.e.Add(engine.Machine{ID: "weird1", State: engine.Running, Metadata: map[string]string{"hib.name": "\x1b[31mevil", "hib.image": "a b"}})

	all, err := v.m.List(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]hibapi.Machine{}
	for _, m := range all {
		byID[m.ID] = m
	}
	if len(all) != 3 || byID[app.ID].Name != "app" || byID[app.ID].Image != "dev" {
		t.Errorf("List = %+v", all)
	}
	if m := byID["unnamed1"]; m.Name != "" || m.State != "paused" {
		t.Errorf("unnamed machine = %+v", m)
	}
	if m := byID["weird1"]; m.Name != "" || m.Image != "" {
		t.Errorf("machine with bad metadata = %+v, want no name or image", m)
	}

	paused, err := v.m.List(t.Context(), engine.Paused)
	if err != nil || len(paused) != 1 || paused[0].ID != "unnamed1" {
		t.Errorf("List(paused) = %+v, %v", paused, err)
	}
}

func TestRemove(t *testing.T) {
	v := newEnv(t)
	app := v.create(t, "app")
	keep := v.create(t, "keep")
	if err := v.m.Remove(t.Context(), "app"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := v.e.Get(t.Context(), app.ID); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("machine after Remove: %v, want ErrNotFound", err)
	}
	if _, err := os.Stat(filepath.Join(v.dir, "host-keys", app.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("pin after Remove: %v, want it gone", err)
	}
	v.pin(t, keep.ID)
	if err := v.m.Remove(t.Context(), "app"); !errors.Is(err, machines.ErrNoMachine) {
		t.Errorf("second Remove = %v, want ErrNoMachine", err)
	}
	v.create(t, "app")
}

func TestPause(t *testing.T) {
	v := newEnv(t)
	app := v.create(t, "app")
	for range 2 {
		if err := v.m.Pause(t.Context(), "app"); err != nil {
			t.Fatalf("Pause: %v", err)
		}
	}
	if em, _ := v.e.Get(t.Context(), app.ID); em.State != engine.Paused {
		t.Errorf("state = %s, want paused", em.State)
	}
	if err := v.m.Pause(t.Context(), "nope"); !errors.Is(err, machines.ErrNoMachine) {
		t.Errorf("Pause(nope) = %v, want ErrNoMachine", err)
	}
}

func TestLookupAmbiguous(t *testing.T) {
	v := newEnv(t)
	for _, id := range []string{"aaa", "bbb"} {
		v.e.Add(engine.Machine{ID: id, State: engine.Running, Metadata: map[string]string{"hib.name": "app"}})
	}
	for name, err := range map[string]error{
		"Pause":  v.m.Pause(t.Context(), "app"),
		"Remove": v.m.Remove(t.Context(), "app"),
	} {
		if err == nil || !strings.Contains(err.Error(), "aaa") || !strings.Contains(err.Error(), "bbb") {
			t.Errorf("%s = %v, want an error naming both machines", name, err)
		}
	}
	if _, err := v.m.Dial(t.Context(), "app"); err == nil {
		t.Error("Dial = nil, want an error")
	}
	if n := len(v.machines(t)); n != 2 {
		t.Errorf("%d machines, want both left alone", n)
	}
}

func TestDial(t *testing.T) {
	v := newEnv(t)
	app := v.create(t, "app")
	if err := v.m.Pause(t.Context(), "app"); err != nil {
		t.Fatal(err)
	}

	c, err := v.m.Dial(t.Context(), "app")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()
	if hibapi.FormatHostKey(c.HostKey)+"\n" != v.pin(t, app.ID) {
		t.Errorf("HostKey = %s, want the pinned one", hibapi.FormatHostKey(c.HostKey))
	}
	if em, _ := v.e.Get(t.Context(), app.ID); em.State != engine.Running {
		t.Errorf("state after Dial = %s, want running", em.State)
	}
	if n := v.g.runCount(app.ID); n != 1 {
		t.Errorf("machine-init ran %d times, want once (the pin is there)", n)
	}

	far := <-v.dialed
	go far.Write([]byte("SSH-2.0-test\r\n"))
	buf := make([]byte, 14)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "SSH-2.0-test\r\n" {
		t.Errorf("read %q, %v", buf, err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := c.Close(); err == nil {
		t.Log("second Close returned nil")
	}
}

func TestDialRepinsLostPin(t *testing.T) {
	v := newEnv(t)
	app := v.create(t, "app")
	want := v.pin(t, app.ID)
	if err := os.Remove(filepath.Join(v.dir, "host-keys", app.ID)); err != nil {
		t.Fatal(err)
	}
	if err := v.m.Pause(t.Context(), "app"); err != nil {
		t.Fatal(err)
	}
	newKeys := string(ssh.MarshalAuthorizedKey(newKey()))
	if err := os.WriteFile(v.keys, []byte(newKeys), 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := v.m.Dial(t.Context(), "app")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()
	if got := v.pin(t, app.ID); got != want || hibapi.FormatHostKey(c.HostKey)+"\n" != want {
		t.Errorf("pin = %q, HostKey = %s; want %q", got, hibapi.FormatHostKey(c.HostKey), want)
	}
	if n := v.g.runCount(app.ID); n != 2 {
		t.Errorf("machine-init ran %d times, want 2", n)
	}
	if v.g.keys[app.ID] != newKeys {
		t.Errorf("keys after repin = %q, want the current ones", v.g.keys[app.ID])
	}
}

func TestDialRefusesCorruptPin(t *testing.T) {
	v := newEnv(t)
	app := v.create(t, "app")
	if err := os.WriteFile(filepath.Join(v.dir, "host-keys", app.ID), []byte("ssh-ed25519 AAAA\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := v.m.Dial(t.Context(), "app"); err == nil {
		t.Error("Dial = nil, want an error")
	}
	if n := v.g.runCount(app.ID); n != 1 {
		t.Errorf("machine-init ran %d times, want once: a bad pin isn't replaced silently", n)
	}
}

func TestDialRejectsOddIDs(t *testing.T) {
	v := newEnv(t)
	v.e.Add(engine.Machine{ID: "../../x", State: engine.Running, Metadata: map[string]string{"hib.name": "app"}})
	if _, err := v.m.Dial(t.Context(), "app"); err == nil || !strings.Contains(err.Error(), "unexpected machine ID") {
		t.Errorf("Dial = %v, want an unexpected machine ID error", err)
	}
	if err := v.m.Remove(t.Context(), "app"); err == nil {
		t.Error("Remove = nil, want an error")
	}
}

func TestDialUnknown(t *testing.T) {
	v := newEnv(t)
	if _, err := v.m.Dial(t.Context(), "nope"); !errors.Is(err, machines.ErrNoMachine) {
		t.Errorf("Dial = %v, want ErrNoMachine", err)
	}
	if _, err := v.m.Dial(t.Context(), "NOPE"); err == nil || !strings.Contains(err.Error(), "bad name") {
		t.Errorf("Dial(NOPE) = %v, want a bad name error", err)
	}
}

// clock is a fake time that tests move.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestKeepalive(t *testing.T) {
	clk := &clock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	const timeout = 200 * time.Millisecond // so it ticks every 50ms
	v := newEnvAt(t, clk.Now)
	app := v.createIdle(t, "app", timeout)
	c, err := v.m.Dial(t.Context(), "app")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	endAt := func() time.Time {
		em, err := v.e.Get(t.Context(), app.ID)
		if err != nil {
			t.Fatal(err)
		}
		return em.EndAt
	}
	clk.Add(time.Hour)
	want := clk.Now().Add(timeout)
	deadline := time.Now().Add(5 * time.Second)
	for !endAt().Equal(want) {
		if time.Now().After(deadline) {
			t.Fatalf("EndAt = %v, want it kept at %v while connected", endAt(), want)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Paused on purpose: the keepalive must not start it again.
	if err := v.m.Pause(t.Context(), "app"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if em, _ := v.e.Get(t.Context(), app.ID); em.State != engine.Paused {
		t.Errorf("state = %s, want it left paused", em.State)
	}

	done := make(chan struct{})
	go func() { c.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close hangs")
	}
	if em, _ := v.e.Get(t.Context(), app.ID); em.State != engine.Paused {
		t.Errorf("state after Close = %s, want it left paused", em.State)
	}
}

func TestKeepaliveStopsOnClose(t *testing.T) {
	clk := &clock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	const timeout = 200 * time.Millisecond
	v := newEnvAt(t, clk.Now)
	app := v.createIdle(t, "app", timeout)
	c, err := v.m.Dial(t.Context(), "app")
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	before, _ := v.e.Get(t.Context(), app.ID)
	clk.Add(time.Hour)
	time.Sleep(200 * time.Millisecond)
	if after, _ := v.e.Get(t.Context(), app.ID); !after.EndAt.Equal(before.EndAt) {
		t.Errorf("EndAt moved from %v to %v after Close", before.EndAt, after.EndAt)
	}
}

func TestCloseStartsIdleClock(t *testing.T) {
	clk := &clock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	v := newEnvAt(t, clk.Now)
	app := v.createIdle(t, "app", time.Hour) // the keepalive won't tick during the test
	c, err := v.m.Dial(t.Context(), "app")
	if err != nil {
		t.Fatal(err)
	}
	clk.Add(10 * time.Minute)
	c.Close()
	if got, want := v.get(t, app.ID).EndAt, clk.Now().Add(time.Hour); !got.Equal(want) {
		t.Errorf("EndAt after Close = %v, want an hour after the disconnect, %v", got, want)
	}

	// Close never shortens a timeout.
	far := clk.Now().Add(5 * time.Hour)
	if err := v.e.SetTimeout(t.Context(), app.ID, 5*time.Hour); err != nil {
		t.Fatal(err)
	}
	c, err = v.m.Dial(t.Context(), "app")
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if got := v.get(t, app.ID).EndAt; !got.Equal(far) {
		t.Errorf("EndAt after Close = %v, want it left at %v", got, far)
	}
}

func TestDialResumesForIdleTime(t *testing.T) {
	clk := &clock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	v := newEnvAt(t, clk.Now)
	app := v.createIdle(t, "app", 20*time.Minute)
	if err := v.m.Pause(t.Context(), "app"); err != nil {
		t.Fatal(err)
	}
	clk.Add(time.Hour)
	c, err := v.m.Dial(t.Context(), "app")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	em := v.get(t, app.ID)
	if want := clk.Now().Add(20 * time.Minute); em.State != engine.Running || !em.EndAt.Equal(want) {
		t.Errorf("after Dial: %s until %v, want running until %v", em.State, em.EndAt, want)
	}
}

func TestDialNeverPausing(t *testing.T) {
	clk := &clock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	v := newEnvAt(t, clk.Now)
	app := v.createIdle(t, "app", 0)
	if err := v.m.Pause(t.Context(), "app"); err != nil {
		t.Fatal(err)
	}
	clk.Add(time.Hour)
	c, err := v.m.Dial(t.Context(), "app")
	if err != nil {
		t.Fatal(err)
	}
	want := clk.Now().Add(machines.MaxRun)
	if em := v.get(t, app.ID); em.State != engine.Running || !em.EndAt.Equal(want) {
		t.Errorf("after Dial: %s until %v, want running until %v", em.State, em.EndAt, want)
	}
	clk.Add(time.Minute)
	c.Close()
	if em := v.get(t, app.ID); !em.EndAt.Equal(want) {
		t.Errorf("EndAt after Close = %v, want it left at %v", em.EndAt, want)
	}
}

func TestListIdle(t *testing.T) {
	v := newEnv(t)
	v.createIdle(t, "app", 0)
	v.e.Add(engine.Machine{ID: "old1", State: engine.Paused, Metadata: map[string]string{"hib.name": "old"}})
	v.e.Add(engine.Machine{ID: "other1", State: engine.Paused})
	ms, err := v.m.List(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, m := range ms {
		got[m.ID] = "nil"
		if m.IdleSeconds != nil {
			got[m.ID] = fmt.Sprint(*m.IdleSeconds)
		}
	}
	for id, want := range map[string]string{"old1": "900", "other1": "nil"} {
		if got[id] != want {
			t.Errorf("IdleSeconds of %s = %s, want %s", id, got[id], want)
		}
	}
	if n := len(got); n != 3 {
		t.Errorf("%d machines, want 3", n)
	}
}
