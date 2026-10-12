package hibd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/github"
	"github.com/solev/hibernaut/internal/hostsblock"
	"github.com/solev/hibernaut/internal/links"
	"github.com/solev/hibernaut/internal/machines"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func (g *secretGuest) change(edit func(g *secretGuest)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	edit(g)
}

func newTestDelivery(t *testing.T, a *App, e engine.Engine) *setupDelivery {
	t.Helper()
	cfg, err := loadConfig(a.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	return newSetupDelivery(e, cfg, a.codexStore(cfg, nil), func(format string, args ...any) {
		t.Errorf("setup: "+format, args...)
	})
}

func TestSetupDeliveryWritesTheSetupAgainAfterAResume(t *testing.T) {
	ctx := t.Context()
	clock := &testClock{now: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)}
	guest := &secretGuest{env: []byte("PATH=/usr/bin\n")}
	e := fake.New(fake.WithClock(clock.Now), fake.WithExec(guest.exec))
	e.Add(engine.Machine{ID: "app1", State: engine.Running, StartedAt: clock.Now(), EndAt: clock.Now().Add(time.Hour),
		Metadata: map[string]string{"hib.name": "app", "hib.image": "dev", "hib.idle": "10m0s"}})
	a, _, _ := newApp(t, e)
	withSecretConfig(t, a)
	secret := storeTestSecret(t, a, "github", "GH_TOKEN", "github.com", "gho_realSecretValue1234567890")
	saveTestProfile(t, a, "The Octocat")
	d := newTestDelivery(t, a, e)
	d.look(ctx)
	env, id := guest.envString(), guest.identity()
	if !strings.Contains(env, secret.Placeholder) || id == nil {
		t.Fatalf("the first look wrote /etc/environment %q and git's identity %+v, want the setup", env, id)
	}

	guest.change(func(g *secretGuest) {
		g.env = []byte("PATH=/usr/bin\n")
		g.git = nil
	})
	requests := len(guest.allRequests())
	d.look(ctx)

	clock.add(time.Minute)
	if err := e.SetTimeout(ctx, "app1", time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Resume(ctx, "app1", 2*time.Hour); err != nil {
		t.Fatal(err)
	}
	d.look(ctx)
	if n := len(guest.allRequests()) - requests; n != 0 {
		t.Errorf("looks without a resume ran %d commands in the machine, want none", n)
	}

	clock.add(time.Minute)
	if err := e.Pause(ctx, "app1"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Resume(ctx, "app1", time.Hour); err != nil {
		t.Fatal(err)
	}
	d.look(ctx)
	if got := guest.envString(); got != env {
		t.Errorf("/etc/environment after a resume = %q, want %q", got, env)
	}
	if got := guest.identity(); got == nil || *got != *id {
		t.Errorf("git's identity after a resume = %+v, want %+v", got, id)
	}
	requests = len(guest.allRequests())
	d.look(ctx)
	if n := len(guest.allRequests()) - requests; n != 0 {
		t.Errorf("the look after the one that caught up with the resume ran %d commands in the machine, want none", n)
	}
}

func TestCodexLoginFileComesBackAfterLogoutAndAResume(t *testing.T) {
	ctx := t.Context()
	clock := &testClock{now: time.Now()}
	guest := &secretGuest{env: []byte("PATH=/usr/bin\n"), hostKey: newKey(t)}
	e := fake.New(fake.WithClock(clock.Now), fake.WithTemplates(images.Template("dev")), fake.WithExec(guest.exec),
		fake.WithDialer(func(context.Context, engine.Machine, int) (net.Conn, error) {
			c, _ := net.Pipe()
			return c, nil
		}))
	a, _, errOut := newApp(t, e)
	withSecretConfig(t, a)
	connectTestCodex(t, a, testCodexTokens())
	run := func(args ...string) {
		t.Helper()
		if code := a.Run(ctx, args); code != 0 {
			t.Fatalf("hibd %s exit %d: %s", strings.Join(args, " "), code, errOut)
		}
	}
	run("new", "app")
	d := newTestDelivery(t, a, e)
	d.look(ctx)
	want := codexLoginFile(t, a)
	if got := guest.codexAuthFile(); !bytes.Equal(got, want) {
		t.Fatalf("auth.json = %q, want the placeholder login file", got)
	}

	// codex logout in the machine deletes the file.
	guest.change(func(g *secretGuest) { g.codexAuth = nil })
	d.look(ctx)
	if got := guest.codexAuthFile(); got != nil {
		t.Fatal("the daemon wrote auth.json again without a resume")
	}

	clock.add(time.Minute)
	run("pause", "app")
	run("bridge", "app")
	d.look(ctx)
	if got := guest.codexAuthFile(); !bytes.Equal(got, want) {
		t.Errorf("auth.json after a pause and a resume = %q, want the placeholder login file", got)
	}
}

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

const namedUserHosts = "127.0.0.1 localhost\n127.0.1.1 e2b.local\n"

// namedGuest is a machine with an /etc/hosts. Everything else goes to its secretGuest.
type namedGuest struct {
	*secretGuest
	mu         sync.Mutex
	hosts      []byte
	hostsExecs int
}

func newNamedGuest(hosts string) *namedGuest {
	return &namedGuest{secretGuest: &secretGuest{env: []byte("PATH=/usr/bin\n")}, hosts: []byte(hosts)}
}

func (g *namedGuest) exec(ctx context.Context, m engine.Machine, token string, req engine.ExecRequest) (engine.ExecResult, error) {
	read := req.Cmd == "cat" && slices.Equal(req.Args, []string{"/etc/hosts"})
	write := req.Cmd == "sh" && len(req.Args) == 5 && req.Args[2] == "hibd-hosts"
	if !read && !write {
		return g.secretGuest.exec(ctx, m, token, req)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.hostsExecs++
	if read {
		return engine.ExecResult{Stdout: bytes.Clone(g.hosts)}, nil
	}
	if sum := sha256.Sum256(g.hosts); req.Args[3] != hexHash(sum[:]) {
		return engine.ExecResult{ExitCode: 75}, nil
	}
	next, err := base64.StdEncoding.DecodeString(req.Args[4])
	if err != nil {
		return engine.ExecResult{ExitCode: 64, Stderr: []byte(err.Error())}, nil
	}
	g.hosts = next
	return engine.ExecResult{}, nil
}

func (g *namedGuest) hostsFile() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return string(g.hosts)
}

func (g *namedGuest) hostsTouches() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.hostsExecs
}

func (g *namedGuest) touches() int {
	return g.hostsTouches() + len(g.allRequests())
}

// hostsLinks is links.json as the delivery reads it.
type hostsLinks struct {
	mu  sync.Mutex
	doc links.Doc
	err error
}

func (l *hostsLinks) Read() (links.Doc, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.doc, l.err
}

func (l *hostsLinks) Update(context.Context, func(*links.Doc) error) error {
	return errors.New("test: the delivery only reads links.json")
}

func (l *hostsLinks) Fence(context.Context, string, string) (func(), error) {
	return func() {}, errors.New("test: the delivery only reads links.json")
}

func (l *hostsLinks) set(doc links.Doc, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.doc, l.err = doc, err
}

func published(ports map[string][]int) links.Doc {
	doc := links.Doc{V: links.Version, Machines: map[string]*links.Machine{}}
	for i, id := range []string{"api1", "web1", "cli1", "dup1", "dup2", "raw1", "gone1", "app1", "bad1"} {
		doc.Machines[id] = &links.Machine{Addr: netip.AddrFrom4([4]byte{198, 18, 0, byte(i + 1)}), Ports: ports[id]}
	}
	return doc
}

func withHostsLinkPort(a *App) {
	old := a.Getenv
	a.Getenv = func(k string) string {
		if k == "HIBD_LINK_PORT" {
			return "5444"
		}
		return old(k)
	}
}

func namedGuests(t *testing.T, guests map[string]*namedGuest) *fake.Engine {
	t.Helper()
	return fake.New(fake.WithExec(func(ctx context.Context, m engine.Machine, token string, req engine.ExecRequest) (engine.ExecResult, error) {
		g, ok := guests[m.ID]
		if !ok {
			t.Errorf("setup ran %+v in %s", req, m.ID)
			return engine.ExecResult{ExitCode: 127}, nil
		}
		return g.exec(ctx, m, token, req)
	}))
}

func TestSetupDeliveryNamesPublishedMachinesInEtcHosts(t *testing.T) {
	ctx := t.Context()
	guests := map[string]*namedGuest{}
	for _, id := range []string{"api1", "web1", "cli1", "dup1", "dup2"} {
		guests[id] = newNamedGuest(namedUserHosts)
	}
	e := namedGuests(t, guests)
	now := time.Now()
	for id, m := range map[string]struct {
		name  string
		state engine.State
	}{"api1": {"api", engine.Running}, "web1": {"web", engine.Paused}, "cli1": {"cli", engine.Running},
		"dup1": {"dup", engine.Running}, "dup2": {"dup", engine.Paused}} {
		e.Add(engine.Machine{ID: id, State: m.state, StartedAt: now, Metadata: map[string]string{"hib.name": m.name}})
	}
	e.Add(engine.Machine{ID: "raw1", State: engine.Paused, StartedAt: now})
	a, _, _ := newApp(t, e)
	withSecretConfig(t, a)
	withHostsLinkPort(a)
	// cli1 has an address but nothing published, raw1 isn't hibd's, gone1 is gone, and the
	// two dups share a name.
	store := &hostsLinks{doc: published(map[string][]int{"api1": {8080}, "web1": {3000, 3001}, "dup1": {80}, "dup2": {80},
		"raw1": {80}, "gone1": {80}})}
	d := newTestDelivery(t, a, e)
	d.links = store
	d.look(ctx)
	want := namedUserHosts +
		"# BEGIN hibernaut machines: hibd manages this block and replaces edits\n" +
		"198.18.0.1 api.hib\n" +
		"198.18.0.2 web.hib\n" +
		"# END hibernaut machines\n"
	for _, id := range []string{"api1", "cli1", "dup1"} {
		if got := guests[id].hostsFile(); got != want {
			t.Errorf("%s's /etc/hosts = %q, want %q", id, got, want)
		}
	}
	for _, id := range []string{"web1", "dup2"} {
		if n := guests[id].touches(); n != 0 {
			t.Errorf("setup ran %d commands in paused %s", n, id)
		}
	}

	store.set(published(map[string][]int{"api1": {8080}, "web1": {3000, 3001}, "cli1": {9000}}), nil)
	d.look(ctx)
	want = namedUserHosts +
		"# BEGIN hibernaut machines: hibd manages this block and replaces edits\n" +
		"198.18.0.1 api.hib\n" +
		"198.18.0.3 cli.hib\n" +
		"198.18.0.2 web.hib\n" +
		"# END hibernaut machines\n"
	for _, id := range []string{"api1", "cli1", "dup1"} {
		if got := guests[id].hostsFile(); got != want {
			t.Errorf("after cli published its first port, %s's /etc/hosts = %q, want %q", id, got, want)
		}
	}

	touches := func() int {
		n := 0
		for _, g := range guests {
			n += g.touches()
		}
		return n
	}
	before := touches()
	store.set(published(map[string][]int{"api1": {8080, 8081}, "web1": {3000, 3001}, "cli1": {9000}}), nil)
	d.look(ctx)
	if n := touches() - before; n != 0 {
		t.Errorf("a port change that leaves the names alone ran %d commands", n)
	}

	store.set(published(nil), nil)
	d.look(ctx)
	for _, id := range []string{"api1", "cli1", "dup1"} {
		if got := guests[id].hostsFile(); got != namedUserHosts {
			t.Errorf("after the last port went, %s's /etc/hosts = %q, want %q", id, got, namedUserHosts)
		}
	}
}

func TestSetupDeliveryLeavesEtcHostsAloneWithLinksOff(t *testing.T) {
	stale := namedUserHosts + "# BEGIN hibernaut machines: hibd manages this block and replaces edits\n" +
		"198.18.0.8 app.hib\n# END hibernaut machines\n"
	for _, off := range []string{"port 0", "store off"} {
		t.Run(off, func(t *testing.T) {
			guest := newNamedGuest(stale)
			e := namedGuests(t, map[string]*namedGuest{"app1": guest})
			e.Add(engine.Machine{ID: "app1", State: engine.Running, StartedAt: time.Now(), Metadata: map[string]string{"hib.name": "app"}})
			a, _, _ := newApp(t, e)
			withSecretConfig(t, a)
			saveTestProfile(t, a, "The Octocat")
			store := &hostsLinks{doc: published(nil)}
			if off == "store off" {
				withHostsLinkPort(a)
				store.err = links.ErrLinksOff
			}
			d := newTestDelivery(t, a, e)
			d.links = store
			d.look(t.Context())
			if guest.identity() == nil {
				t.Fatal("the rest of the setup was not delivered")
			}
			if guest.hostsTouches() != 0 || guest.hostsFile() != stale {
				t.Errorf("with links off, setup touched /etc/hosts: %q", guest.hostsFile())
			}
		})
	}
}

func TestSetupDeliveryWarnsWhenItCantNameMachines(t *testing.T) {
	ctx := t.Context()
	guests := map[string]*namedGuest{
		"app1": newNamedGuest(namedUserHosts),
		"bad1": newNamedGuest(namedUserHosts + "# END hibernaut machines\n"),
	}
	e := namedGuests(t, guests)
	for id, name := range map[string]string{"app1": "app", "bad1": "bad"} {
		e.Add(engine.Machine{ID: id, State: engine.Running, StartedAt: time.Now(), Metadata: map[string]string{"hib.name": name}})
	}
	a, _, _ := newApp(t, e)
	withSecretConfig(t, a)
	withHostsLinkPort(a)
	secret := storeTestSecret(t, a, "github", "GH_TOKEN", "github.com", "gho_realSecretValue1234567890")
	saveTestProfile(t, a, "The Octocat")
	cfg, err := loadConfig(a.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	var logs []string
	d := newSetupDelivery(e, cfg, a.codexStore(cfg, nil), func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	})
	store := &hostsLinks{err: errors.New("test: links.json unreadable")}
	d.links = store
	d.look(ctx)
	d.look(ctx)
	if want := []string{"building the hosts block: test: links.json unreadable"}; !slices.Equal(logs, want) {
		t.Errorf("logs = %q, want %q once", logs, want)
	}
	for id, g := range guests {
		if !strings.Contains(g.envString(), secret.Placeholder) || g.identity() == nil {
			t.Errorf("%s didn't get the rest of the setup", id)
		}
		if g.hostsTouches() != 0 {
			t.Errorf("%s's /etc/hosts was touched without links.json", id)
		}
	}

	logs = nil
	store.set(published(map[string][]int{"app1": {8080}}), nil)
	d.look(ctx)
	d.look(ctx)
	if got := guests["app1"].hostsFile(); !strings.HasSuffix(got, "198.18.0.8 app.hib\n# END hibernaut machines\n") {
		t.Errorf("app's /etc/hosts = %q, want the block, though bad's doesn't take it", got)
	}
	if len(logs) != 1 || !strings.HasPrefix(logs[0], "updating bad: ") || !strings.Contains(logs[0], hostsblock.ErrUnterminated.Error()) {
		t.Errorf("logs = %q, want bad's unterminated block once", logs)
	}

	logs = nil
	store.set(links.Doc{}, errors.New("test: links.json unreadable"))
	d.look(ctx)
	if len(logs) != 1 || !strings.HasPrefix(logs[0], "building the hosts block: ") {
		t.Errorf("logs = %q, want the failure again once it came back", logs)
	}
}
