package hibd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/links"
)

// portGuest is a machine that answers the setup push and reports its listening sockets.
type portGuest struct {
	hostKey ssh.PublicKey
	mu      sync.Mutex
	tcp     string
	checks  int
}

func (g *portGuest) exec(_ context.Context, _ engine.Machine, _ string, req engine.ExecRequest) (engine.ExecResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case req.Cmd == images.MachineInit && req.User == "root":
		return engine.ExecResult{Stdout: []byte(hibapi.FormatHostKey(g.hostKey) + "\n")}, nil
	case req.Cmd == "cat" && slices.Equal(req.Args, []string{"/proc/net/tcp", "/proc/net/tcp6"}):
		g.checks++
		return engine.ExecResult{Stdout: []byte(g.tcp)}, nil
	case req.Cmd == "cat":
		return engine.ExecResult{}, nil
	}
	return engine.ExecResult{ExitCode: 127}, nil
}

func (g *portGuest) listening(tcp string) {
	g.mu.Lock()
	g.tcp = tcp
	g.checks = 0
	g.mu.Unlock()
}

type fakeController struct {
	mu        sync.Mutex
	revokeErr error
	revoked   []string
	stats     links.Stats
	statErr   error
}

func (c *fakeController) Revoke(_ context.Context, id string, ports []int) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.revoked = append(c.revoked, fmt.Sprintf("%s %v", id, ports))
	return len(ports), c.revokeErr
}

func (c *fakeController) Close(context.Context, string) (int, error) { return 0, nil }

func (c *fakeController) Stat(context.Context) (links.Stats, error) {
	return c.stats, c.statErr
}

// portApp is a host with links on, the machines api and web, and a control socket that answers.
// Tests that use it must not be parallel: it replaces newController.
func portApp(t *testing.T) (*fake.Engine, *App, *portGuest, *fakeController, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	g := &portGuest{hostKey: newKey(t)}
	e := newEngine(g.hostKey, fake.WithExec(g.exec))
	a, out, errOut := newApp(t, e)
	get := a.Getenv
	a.Getenv = func(k string) string {
		if k == "HIBD_LINK_PORT" {
			return "5444"
		}
		return get(k)
	}
	for _, name := range []string{"api", "web"} {
		if code := a.Run(t.Context(), []string{"new", name}); code != 0 {
			t.Fatalf("new %s: %s", name, errOut)
		}
	}
	ctl := &fakeController{}
	old := newController
	newController = func() (links.Controller, error) { return ctl, nil }
	t.Cleanup(func() { newController = old })
	out.Reset()
	errOut.Reset()
	return e, a, g, ctl, out, errOut
}

func run(t *testing.T, a *App, out, errOut *bytes.Buffer, args ...string) int {
	t.Helper()
	out.Reset()
	errOut.Reset()
	return a.Run(t.Context(), args)
}

func linkDoc(t *testing.T, a *App) links.Doc {
	t.Helper()
	d, err := links.NewStore(a.Getenv("HIBD_STATE_DIR")).Read()
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestPortAdd(t *testing.T) {
	e, a, _, _, out, errOut := portApp(t)
	if code := run(t, a, out, errOut, "port", "add", "api", "8080"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if got, want := out.String(), "api:8080 is published to your machines as api.hib:8080\n"; got != want {
		t.Errorf("stdout %q, want %q", got, want)
	}
	if code := run(t, a, out, errOut, "port", "add", "api", "5432", "6379", "5432"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if got, want := out.String(), "api:5432 and api:6379 are published to your machines as api.hib:5432 and api.hib:6379\n"; got != want {
		t.Errorf("stdout %q, want %q", got, want)
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr %q, want nothing", errOut)
	}
	m := linkDoc(t, a).Machines[mustID(t, e, "api")]
	if m == nil || m.Addr != links.First || !slices.Equal(m.Ports, []int{5432, 6379, 8080}) {
		t.Fatalf("links.json has %+v, want 5432, 6379 and 8080 at %v", m, links.First)
	}

	if code := run(t, a, out, errOut, "port", "add", "web", "3000", "-json"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	want := `{"v":1,"ports":[{"machine":"web","addr":"198.18.0.2","published":[{"port":3000,"open":null}]}]}` + "\n"
	if out.String() != want {
		t.Errorf("stdout %s, want %s", out, want)
	}
}

func TestPortAddWhatIsAlreadyPublished(t *testing.T) {
	_, a, _, _, out, errOut := portApp(t)
	if code := run(t, a, out, errOut, "port", "add", "api", "8080"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if code := run(t, a, out, errOut, "port", "add", "api", "8080", "9090"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if got, want := errOut.String(), "api:8080 was already published\n"; got != want {
		t.Errorf("stderr %q, want %q", got, want)
	}
	if code := run(t, a, out, errOut, "port", "add", "api", "8080", "9090"); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if got, want := errOut.String(), "hibd port add: api:8080 and api:9090 are already published\n"; got != want {
		t.Errorf("stderr %q, want %q", got, want)
	}
}

func TestPortRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		code int
		want string
	}{
		{"ssh bridge", []string{"add", "api", "80", "8081"}, 1, "hibd port add: port 8081 is Hibernaut's SSH bridge and can't be published\n"},
		{"envd", []string{"add", "api", "49983"}, 1, "hibd port add: port 49983 is E2B's agent and can't be published\n"},
		{"rm reserved", []string{"rm", "api", "8081"}, 1, "hibd port rm: port 8081 is Hibernaut's SSH bridge and can't be published\n"},
		{"too big", []string{"add", "api", "8080000"}, 2, "hibd port add: 8080000 isn't a port. Ports are 1 to 65535\n"},
		{"zero", []string{"rm", "api", "0"}, 2, "hibd port rm: 0 isn't a port. Ports are 1 to 65535\n"},
		{"signed", []string{"add", "api", "+80"}, 2, "hibd port add: +80 isn't a port. Ports are 1 to 65535\n"},
		{"no ports", []string{"add", "api"}, 2, "usage: hibd port add NAME PORT [PORT...] [-json]\n"},
		{"no name", []string{"rm"}, 2, "usage: hibd port rm NAME PORT [PORT...] [-json]\n"},
		{"ls args", []string{"ls", "api"}, 2, "usage: hibd port ls [-json]\n"},
		{"no machine", []string{"add", "nope", "80"}, 1, "hibd port add: no machine called \"nope\"\n"},
		{"unknown", []string{"open"}, 2, "hibd port: unknown command \"open\"\n"},
		{"nothing", nil, 2, "usage: hibd port add NAME PORT [PORT...] [-json] | port rm NAME PORT [PORT...] [-json] | port ls [-json]\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, a, _, _, out, errOut := portApp(t)
			if code := run(t, a, out, errOut, append([]string{"port"}, tc.args...)...); code != tc.code {
				t.Errorf("exit %d, want %d", code, tc.code)
			}
			if got := errOut.String(); !strings.HasPrefix(got, tc.want) {
				t.Errorf("stderr %q, want %q", got, tc.want)
			}
			if out.Len() != 0 {
				t.Errorf("stdout %q, want nothing", out)
			}
			if _, err := os.Stat(filepath.Join(a.Getenv("HIBD_STATE_DIR"), links.StateName)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("links.json: %v, want none written", err)
			}
		})
	}
}

func TestPortCaps(t *testing.T) {
	ports := func(from, n int) []string {
		var s []string
		for p := from; p < from+n; p++ {
			s = append(s, fmt.Sprint(p))
		}
		return s
	}
	_, a, _, _, out, errOut := portApp(t)
	if code := run(t, a, out, errOut, append([]string{"port", "add", "api"}, ports(1000, 10)...)...); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if code := run(t, a, out, errOut, append([]string{"port", "add", "api", "1000"}, ports(2000, 7)...)...); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if got, want := errOut.String(), "hibd port add: that would make 17 ports on api, and 16 is the most there is room for\n"; got != want {
		t.Errorf("stderr %q, want %q", got, want)
	}
	if code := run(t, a, out, errOut, append([]string{"port", "add", "api"}, ports(2000, 6)...)...); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if code := run(t, a, out, errOut, "port", "add", "api", "3000", "-json"); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	r, err := hibapi.DecodeResponse(out.Bytes())
	if want := "api already publishes 16 ports, the most there is room for"; r.Error != want || err == nil {
		t.Errorf("reply %+v, %v, want the error %q", r, err, want)
	}
}

func TestPortAddressCaps(t *testing.T) {
	for _, tc := range []struct {
		name string
		fill func(*links.Doc)
		want string
	}{
		{"machines", func(d *links.Doc) {
			for i := range links.MaxMachines {
				_, _ = d.Allocate(fmt.Sprintf("gone%d", i))
			}
		}, "hibd port add: 256 machines already publish ports, the most there is room for\n"},
		{"addresses", func(d *links.Doc) {
			d.Next = netip.MustParseAddr("198.20.0.0")
		}, "hibd port add: no virtual addresses left. addresses are never reused, so this needs a new host\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, a, _, _, out, errOut := portApp(t)
			store := links.NewStore(a.Getenv("HIBD_STATE_DIR"))
			if err := store.Update(t.Context(), func(d *links.Doc) error { tc.fill(d); return nil }); err != nil {
				t.Fatal(err)
			}
			if code := run(t, a, out, errOut, "port", "add", "api", "8080"); code != 1 {
				t.Errorf("exit %d, want 1", code)
			}
			if got := errOut.String(); got != tc.want {
				t.Errorf("stderr %q, want %q", got, tc.want)
			}
			if m := linkDoc(t, a).Machines[mustID(t, e, "api")]; m != nil {
				t.Errorf("api has %+v, want no entry", m)
			}
		})
	}
}

func TestPortStateItCannotRead(t *testing.T) {
	for _, tc := range []struct {
		name, state, want string
	}{
		{"corrupt", "{", "hibd can't read its published ports: links.json: "},
		{"newer", `{"v":2,"future":true}`, "hibd can't read its published ports: they were written by a newer hibd. Upgrade it"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, a, _, ctl, out, errOut := portApp(t)
			path := filepath.Join(a.Getenv("HIBD_STATE_DIR"), links.StateName)
			if err := os.WriteFile(path, []byte(tc.state), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{{"add", "api", "8080"}, {"rm", "api", "8080"}, {"ls"}} {
				if code := run(t, a, out, errOut, append(append([]string{"port"}, args...), "-json")...); code != 1 {
					t.Errorf("port %s: exit %d, want 1", args[0], code)
				}
				r, _ := hibapi.DecodeResponse(out.Bytes())
				if !strings.HasPrefix(r.Error, tc.want) {
					t.Errorf("port %s: error %q, want %q", args[0], r.Error, tc.want)
				}
				if tc.name == "corrupt" && !strings.HasSuffix(r.Error, ". No machine can reach another until it's fixed") {
					t.Errorf("port %s: error %q, want it to say what breaks", args[0], r.Error)
				}
			}
			if data, _ := os.ReadFile(path); string(data) != tc.state {
				t.Errorf("links.json is %q, want it left as %q", data, tc.state)
			}
			if len(ctl.revoked) != 0 {
				t.Errorf("revoked %q, want nothing", ctl.revoked)
			}
		})
	}
}

func TestPortsNeedLinks(t *testing.T) {
	e := newEngine(newKey(t))
	a, out, errOut := newApp(t, e)
	for _, args := range [][]string{{"add", "api", "8080"}, {"rm", "api", "8080"}, {"ls"}} {
		if code := run(t, a, out, errOut, append([]string{"port"}, args...)...); code != 1 {
			t.Errorf("port %s: exit %d, want 1", args[0], code)
		}
		if got, want := errOut.String(), "hibd port "+args[0]+": links are off on this host\n"; got != want {
			t.Errorf("stderr %q, want %q", got, want)
		}
	}
}

func TestPortRm(t *testing.T) {
	e, a, _, ctl, out, errOut := portApp(t)
	if code := run(t, a, out, errOut, "port", "add", "api", "8080", "5432", "6379"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	id := mustID(t, e, "api")
	if code := run(t, a, out, errOut, "port", "rm", "api", "6379"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if got, want := out.String(), "api:6379 is no longer reachable from your machines\n"; got != want {
		t.Errorf("stdout %q, want %q", got, want)
	}
	if code := run(t, a, out, errOut, "port", "rm", "api", "5432", "9090", "-json"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if got, want := errOut.String(), "api:9090 wasn't published\n"; got != want {
		t.Errorf("stderr %q, want %q", got, want)
	}
	want := `{"v":1,"ports":[{"machine":"api","addr":"198.18.0.1","published":[{"port":8080,"open":null}]}]}` + "\n"
	if out.String() != want {
		t.Errorf("stdout %s, want %s", out, want)
	}
	if code := run(t, a, out, errOut, "port", "rm", "api", "5432", "9090"); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if got, want := errOut.String(), "hibd port rm: api:5432 and api:9090 weren't published\n"; got != want {
		t.Errorf("stderr %q, want %q", got, want)
	}
	if code := run(t, a, out, errOut, "port", "rm", "api", "8080", "-json"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	want = `{"v":1,"ports":[{"machine":"api","addr":"198.18.0.1","published":[]}]}` + "\n"
	if out.String() != want {
		t.Errorf("stdout %s, want %s", out, want)
	}
	revoked := []string{id + " [6379]", id + " [5432 9090]", id + " [5432 9090]", id + " [8080]"}
	if !slices.Equal(ctl.revoked, revoked) {
		t.Errorf("revoked %q, want %q", ctl.revoked, revoked)
	}
	if m := linkDoc(t, a).Machines[id]; m == nil || len(m.Ports) != 0 || m.Addr != links.First {
		t.Errorf("api has %+v, want no ports and its address kept", m)
	}
}

func TestPortRmUnconfirmed(t *testing.T) {
	e, a, _, ctl, out, errOut := portApp(t)
	if code := run(t, a, out, errOut, "port", "add", "api", "8080", "5432"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	ctl.revokeErr = errors.New("connection refused")
	if code := run(t, a, out, errOut, "port", "rm", "api", "8080"); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if got, want := errOut.String(), "hibd port rm: api:8080 is unpublished, but hibd proxy didn't confirm it closed open links: check hib port ls\n"; got != want {
		t.Errorf("stderr %q, want %q", got, want)
	}
	if code := run(t, a, out, errOut, "port", "rm", "api", "5432", "-json"); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	r, err := hibapi.DecodeResponse(out.Bytes())
	if want := "api:5432 is unpublished, but hibd proxy didn't confirm it closed open links: check hib port ls"; err == nil || r.Error != want {
		t.Errorf("reply %+v, %v, want the error %q", r, err, want)
	}
	if len(r.Ports) != 1 || r.Ports[0].Machine != "api" || len(r.Ports[0].Published) != 0 {
		t.Errorf("reply ports %+v, want api with nothing published", r.Ports)
	}
	if m := linkDoc(t, a).Machines[mustID(t, e, "api")]; m == nil || len(m.Ports) != 0 {
		t.Errorf("api has %+v, want both ports unpublished anyway", m)
	}
}

func TestPortLs(t *testing.T) {
	e, a, _, ctl, out, errOut := portApp(t)
	if code := run(t, a, out, errOut, "port", "ls", "-json"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if got, want := out.String(), `{"v":1,"ports":[]}`+"\n"; got != want {
		t.Errorf("stdout %q, want %q", got, want)
	}
	if code := run(t, a, out, errOut, "port", "ls"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if got, want := out.String(), "NAME  HOST  IP  PORTS  OPEN\n"; got != want {
		t.Errorf("stdout %q, want %q", got, want)
	}
	if got, want := errOut.String(), "no published ports. hib port add NAME PORT publishes one\n"; got != want {
		t.Errorf("stderr %q, want %q", got, want)
	}

	for _, args := range [][]string{{"web", "3000"}, {"api", "8080", "5432"}} {
		if code := run(t, a, out, errOut, append([]string{"port", "add"}, args...)...); code != 0 {
			t.Fatalf("exit %d: %s", code, errOut)
		}
	}
	api := mustID(t, e, "api")
	ctl.stats = links.Stats{Ports: map[links.Open]int{{Target: api, Port: 8080}: 2, {Target: api, Port: 5432}: 1}}
	if code := run(t, a, out, errOut, "port", "ls"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	want := "" +
		"NAME  HOST     IP          PORTS      OPEN\n" +
		"api   api.hib  198.18.0.2  5432,8080  3\n" +
		"web   web.hib  198.18.0.1  3000       0\n"
	if out.String() != want {
		t.Errorf("stdout\n%s\nwant\n%s", out, want)
	}
	if code := run(t, a, out, errOut, "port", "ls", "-json"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	wantJSON := `{"v":1,"ports":[` +
		`{"machine":"api","addr":"198.18.0.2","published":[{"port":5432,"open":1},{"port":8080,"open":2}]},` +
		`{"machine":"web","addr":"198.18.0.1","published":[{"port":3000,"open":0}]}]}` + "\n"
	if out.String() != wantJSON {
		t.Errorf("stdout %s, want %s", out, wantJSON)
	}

	ctl.statErr = errors.New("connection refused")
	if code := run(t, a, out, errOut, "port", "ls"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if lines := strings.Split(out.String(), "\n"); !strings.HasSuffix(lines[1], " -") || !strings.HasSuffix(lines[2], " -") {
		t.Errorf("stdout %q, want OPEN to be - when the proxy doesn't answer", out)
	}
	if got, want := errOut.String(), "warning: hibd proxy didn't say how many links are open: connection refused\n"; got != want {
		t.Errorf("stderr %q, want %q", got, want)
	}
	if code := run(t, a, out, errOut, "port", "ls", "-json"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	wantJSON = `{"v":1,"ports":[` +
		`{"machine":"api","addr":"198.18.0.2","published":[{"port":5432,"open":null},{"port":8080,"open":null}]},` +
		`{"machine":"web","addr":"198.18.0.1","published":[{"port":3000,"open":null}]}]}` + "\n"
	if out.String() != wantJSON {
		t.Errorf("stdout %s, want %s", out, wantJSON)
	}

	if err := e.Kill(t.Context(), mustID(t, e, "web")); err != nil {
		t.Fatal(err)
	}
	if code := run(t, a, out, errOut, "port", "ls", "-json"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if r, _ := hibapi.DecodeResponse(out.Bytes()); len(r.Ports) != 1 || r.Ports[0].Machine != "api" {
		t.Errorf("ports %+v, want only api once web is gone", r.Ports)
	}
}

func TestPortAddWarnsAboutLocalhost(t *testing.T) {
	const header = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
	const header6 = "  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
	listen := func(addr string) string {
		return fmt.Sprintf("   0: %s:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 1 1 0 100 0 0 10 0\n", addr)
	}
	connected := "   1: 0100007F:1F90 0100007F:D431 01 00000000:00000000 00:00000000 00000000  1000        0 2 1 0 20 4 30 10 -1\n"
	for _, tc := range []struct {
		name, tcp, want string
	}{
		{"ipv4 loopback", header + listen("0100007F") + connected, "api listens on 127.0.0.1:8080, so its clients will look local to it\n"},
		{"ipv6 loopback", header + header6 + listen("00000000000000000000000001000000"), "api listens on [::1]:8080, so its clients will look local to it\n"},
		{"mapped loopback", header6 + listen("0000000000000000FFFF00000100007F"), "api listens on 127.0.0.1:8080, so its clients will look local to it\n"},
		{"any address", header + listen("00000000") + connected, ""},
		{"loopback and any", header + listen("0100007F") + header6 + listen("00000000000000000000000000000000"), ""},
		{"another port", header + strings.Replace(listen("0100007F"), ":1F90", ":1F91", 1), ""},
		{"nothing listening", "", ""},
		{"garbage", "cat: /proc/net/tcp6: No such file\n0: zz:1F90 00 0A\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, a, g, _, out, errOut := portApp(t)
			g.listening(tc.tcp)
			if code := run(t, a, out, errOut, "port", "add", "api", "8080"); code != 0 {
				t.Fatalf("exit %d: %s", code, errOut)
			}
			if got := errOut.String(); got != tc.want {
				t.Errorf("stderr %q, want %q", got, tc.want)
			}
			if got, want := out.String(), "api:8080 is published to your machines as api.hib:8080\n"; got != want {
				t.Errorf("stdout %q, want %q", got, want)
			}
		})
	}
}

func TestPortAddSkipsTheCheckOnAPausedMachine(t *testing.T) {
	e, a, g, _, out, errOut := portApp(t)
	g.listening("   0: 0100007F:1F90 00000000:0000 0A 0\n")
	if err := e.Pause(t.Context(), mustID(t, e, "api")); err != nil {
		t.Fatal(err)
	}
	if code := run(t, a, out, errOut, "port", "add", "api", "8080"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if errOut.Len() != 0 || g.checks != 0 {
		t.Errorf("stderr %q after %d checks, want no check and no warning", errOut, g.checks)
	}
	if m := linkDoc(t, a).Machines[mustID(t, e, "api")]; m == nil || !slices.Equal(m.Ports, []int{8080}) {
		t.Errorf("api has %+v, want 8080 published", m)
	}
}
