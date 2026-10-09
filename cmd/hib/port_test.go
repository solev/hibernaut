package main

import (
	"context"
	"io"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/hibapi"
)

// listening is what the machines report in /proc/net/tcp.
type listening struct {
	mu  sync.Mutex
	tcp string
}

func (l *listening) set(tcp string) {
	l.mu.Lock()
	l.tcp = tcp
	l.mu.Unlock()
}

// portHost is a host with links on, whose hibd has the port command, and the machines api and
// web.
func portHost(t *testing.T) (*host, *client, *listening) {
	t.Helper()
	h := newHost(t)
	h.env["HIBD_LINK_PORT"] = "5444"
	l := &listening{}
	h.engine = fake.New(
		fake.WithTemplates(images.Template("dev")),
		fake.WithExec(func(_ context.Context, _ engine.Machine, _ string, req engine.ExecRequest) (engine.ExecResult, error) {
			switch {
			case req.Cmd == images.MachineInit:
				return engine.ExecResult{Stdout: []byte(hibapi.FormatHostKey(h.machineKey) + "\n")}, nil
			case req.Cmd == "cat" && slices.Equal(req.Args, []string{"/proc/net/tcp", "/proc/net/tcp6"}):
				l.mu.Lock()
				defer l.mu.Unlock()
				return engine.ExecResult{Stdout: []byte(l.tcp)}, nil
			case req.Cmd == "cat":
				return engine.ExecResult{}, nil
			}
			return engine.ExecResult{ExitCode: 127}, nil
		}),
		fake.WithDialer(func(context.Context, engine.Machine, int) (net.Conn, error) {
			near, far := net.Pipe()
			h.far <- far
			return near, nil
		}),
	)
	hibdVersion(h, `{"v":1,"version":"test","features":["build-events","disconnect","ports"]}`)
	c := initClient(t, h)
	for _, name := range []string{"api", "web"} {
		if code := c.hib("new", name); code != 0 {
			t.Fatalf("hib new %s = %d: %s", name, code, c.errOut)
		}
	}
	h.takeCalls()
	return h, c, l
}

// hibdVersion makes the host's hibd answer version --json with reply, and leaves every other
// command to the real one.
func hibdVersion(h *host, reply string) {
	h.override = func(words []string, stdout, _ io.Writer) (int, bool) {
		if !slices.Equal(words, []string{"version", "--json"}) {
			return 0, false
		}
		io.WriteString(stdout, reply+"\n")
		return 0, true
	}
}

func TestPortAddAndLs(t *testing.T) {
	h, c, _ := portHost(t)
	if code := c.hib("port", "add", "api", "8080"); code != 0 {
		t.Fatalf("exit %d: %s", code, c.errOut)
	}
	if got, want := c.out.String(), "api:8080 is published to your machines as api.hib:8080\n"; got != want {
		t.Errorf("stdout %q, want %q", got, want)
	}
	if got, want := remoteCommands(h.takeCalls()), []string{"hibd version --json", "hibd port add api 8080 --json"}; !slices.Equal(got, want) {
		t.Errorf("remote commands %q, want %q, as the login user", got, want)
	}
	if code := c.hib("port", "add", "api", "5432", "6379", "5432", "--json"); code != 0 {
		t.Fatalf("exit %d: %s", code, c.errOut)
	}
	want := `{"v":1,"ports":[{"machine":"api","addr":"198.18.0.1","published":[{"port":5432,"open":0},{"port":6379,"open":0},{"port":8080,"open":0}]}]}` + "\n"
	if c.out.String() != want {
		t.Errorf("stdout %s, want %s", c.out, want)
	}
	if code := c.hib("port", "add", "web", "3000", "3001", "3002"); code != 0 {
		t.Fatalf("exit %d: %s", code, c.errOut)
	}
	if got, want := c.out.String(), "web:3000, web:3001 and web:3002 are published to your machines as web.hib:3000, web.hib:3001 and web.hib:3002\n"; got != want {
		t.Errorf("stdout %q, want %q", got, want)
	}

	if code := c.hib("port", "ls"); code != 0 {
		t.Fatalf("exit %d: %s", code, c.errOut)
	}
	want = "" +
		"NAME  HOST     IP          PORTS           OPEN\n" +
		"api   api.hib  198.18.0.1  5432,6379,8080  0\n" +
		"web   web.hib  198.18.0.2  3000,3001,3002  0\n"
	if c.out.String() != want {
		t.Errorf("stdout\n%s\nwant\n%s", c.out, want)
	}
	if got := c.errOut.String(); !strings.Contains(got, "warning: hibd proxy didn't say how many links are open") {
		t.Errorf("stderr %q, want hibd's warning that OPEN is unknown", got)
	}
	if code := c.hib("port", "ls", "--json"); code != 0 {
		t.Fatalf("exit %d: %s", code, c.errOut)
	}
	r, err := hibapi.DecodeResponse(c.out.Bytes())
	if err != nil || len(r.Ports) != 2 || r.Ports[1].Machine != "web" || len(r.Ports[1].Published) != 3 {
		t.Errorf("reply %+v, %v, want api and web", r, err)
	}
}

func TestPortLsWithNothingPublished(t *testing.T) {
	_, c, _ := portHost(t)
	if code := c.hib("port", "ls"); code != 0 {
		t.Fatalf("exit %d: %s", code, c.errOut)
	}
	if got, want := c.out.String(), "NAME  HOST  IP  PORTS  OPEN\n"; got != want {
		t.Errorf("stdout %q, want %q", got, want)
	}
	if got, want := c.errOut.String(), "no published ports. hib port add NAME PORT publishes one\n"; got != want {
		t.Errorf("stderr %q, want %q", got, want)
	}
	if code := c.hib("port", "ls", "--json"); code != 0 {
		t.Fatalf("exit %d: %s", code, c.errOut)
	}
	if got, want := c.out.String(), `{"v":1,"ports":[]}`+"\n"; got != want {
		t.Errorf("stdout %q, want %q", got, want)
	}
}

func TestPortRm(t *testing.T) {
	h, c, _ := portHost(t)
	if code := c.hib("port", "add", "api", "8080", "6379"); code != 0 {
		t.Fatalf("exit %d: %s", code, c.errOut)
	}
	// This build's control socket is a stub that never answers, so hibd can't confirm.
	if code := c.hib("port", "rm", "api", "6379"); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if got, want := c.errOut.String(), "hib: api:6379 is unpublished, but hibd proxy didn't confirm it closed open links: check hib port ls\n"; got != want {
		t.Errorf("stderr %q, want %q", got, want)
	}
	if code := c.hib("port", "rm", "api", "8080", "--json"); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	want := `{"v":1,"error":"api:8080 is unpublished, but hibd proxy didn't confirm it closed open links: check hib port ls",` +
		`"ports":[{"machine":"api","addr":"198.18.0.1","published":[]}]}` + "\n"
	if c.out.String() != want {
		t.Errorf("stdout %s, want %s", c.out, want)
	}
	if code := c.hib("port", "rm", "api", "8080", "9090"); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if got, want := c.errOut.String(), "hib: api:8080 and api:9090 weren't published\n"; got != want {
		t.Errorf("stderr %q, want %q", got, want)
	}

	h.override = func(words []string, stdout, _ io.Writer) (int, bool) {
		switch {
		case slices.Equal(words, []string{"version", "--json"}):
			io.WriteString(stdout, `{"v":1,"version":"test","features":["ports"]}`+"\n")
		case slices.Equal(words, []string{"port", "rm", "api", "5432", "6379", "--json"}):
			io.WriteString(stdout, `{"v":1,"ports":[{"machine":"api","addr":"198.18.0.1","published":[]}]}`+"\n")
		default:
			return 0, false
		}
		return 0, true
	}
	if code := c.hib("port", "rm", "api", "5432", "6379"); code != 0 {
		t.Fatalf("exit %d: %s", code, c.errOut)
	}
	if got, want := c.out.String(), "api:5432 and api:6379 are no longer reachable from your machines\n"; got != want {
		t.Errorf("stdout %q, want %q", got, want)
	}
}

func TestPortRefusalsComeFromTheHost(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"add", "api", "8081"}, "hib: port 8081 is Hibernaut's SSH bridge and can't be published\n"},
		{[]string{"add", "api", "49983"}, "hib: port 49983 is E2B's agent and can't be published\n"},
		{[]string{"add", "nope", "8080"}, `hib: no machine called "nope"`},
		{[]string{"rm", "api", "8080"}, "hib: api:8080 wasn't published\n"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			_, c, _ := portHost(t)
			if code := c.hib(append([]string{"port"}, tc.args...)...); code != 1 {
				t.Errorf("exit %d, want 1", code)
			}
			if got := c.errOut.String(); !strings.HasPrefix(got, tc.want) {
				t.Errorf("stderr %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPortMistakesNeverReachTheHost(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"add"}, "hib port add: missing NAME\n"},
		{[]string{"add", "api"}, "hib port add: missing PORT\n"},
		{[]string{"rm", "api"}, "hib port rm: missing PORT\n"},
		{[]string{"add", "api", "8080000"}, "hib port add: 8080000 isn't a port. Ports are 1 to 65535\n"},
		{[]string{"add", "api", "8080", "0"}, "hib port add: 0 isn't a port. Ports are 1 to 65535\n"},
		{[]string{"rm", "api", "http"}, "hib port rm: http isn't a port. Ports are 1 to 65535\n"},
		{[]string{"add", "API", "8080"}, "hib port add: "},
		{[]string{"add", "api", "8080", "--all"}, "hib port add: "},
		{[]string{"ls", "api"}, "hib port ls: unexpected argument api\n"},
		{[]string{"open"}, "hib port: unknown command open\n"},
	} {
		if code := c.hib(append([]string{"port"}, tc.args...)...); code != 2 {
			t.Errorf("hib port %q = %d, want 2", tc.args, code)
		}
		if got := c.errOut.String(); !strings.HasPrefix(got, tc.want) {
			t.Errorf("hib port %q: stderr %q, want %q", tc.args, got, tc.want)
		}
	}
	if calls := h.takeCalls(); len(calls) != 0 {
		t.Errorf("mistakes reached the host: %q", calls)
	}
}

func TestPortNeedsANewEnoughHibd(t *testing.T) {
	h := newHost(t)
	h.env["HIBD_LINK_PORT"] = "5444"
	hibdVersion(h, `{"v":1,"version":"old","features":["build-events","disconnect"]}`)
	c := initClient(t, h)
	for _, args := range [][]string{{"add", "api", "8080"}, {"rm", "api", "8080"}, {"ls"}} {
		if code := c.hib(append([]string{"port"}, args...)...); code != 1 {
			t.Errorf("hib port %s = %d, want 1", args[0], code)
		}
		if got, want := c.errOut.String(), "hib: this host's hibd is too old for hib port. Upgrade it\n"; got != want {
			t.Errorf("hib port %s: stderr %q, want %q", args[0], got, want)
		}
		if got, want := remoteCommands(h.takeCalls()), []string{"hibd version --json"}; !slices.Equal(got, want) {
			t.Errorf("hib port %s ran %q, want only %q", args[0], got, want)
		}
	}
}

func TestPortAddWarnsAboutALocalhostOnlyService(t *testing.T) {
	const row = "   0: %s:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 1 1 0 100 0 0 10 0\n"
	for _, tc := range []struct {
		name, addr string
		warn       bool
	}{
		{"localhost", "0100007F", true},
		{"every address", "00000000", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, c, l := portHost(t)
			l.set("  sl  local_address rem_address   st\n" + strings.Replace(row, "%s", tc.addr, 1))
			if code := c.hib("port", "add", "api", "8080"); code != 0 {
				t.Fatalf("exit %d: %s", code, c.errOut)
			}
			warning := "api listens on 127.0.0.1:8080, so its clients will look local to it\n"
			if got := c.errOut.String(); strings.Contains(got, warning) != tc.warn {
				t.Errorf("stderr %q, want the warning: %v", got, tc.warn)
			}
			if got, want := c.out.String(), "api:8080 is published to your machines as api.hib:8080\n"; got != want {
				t.Errorf("stdout %q, want %q: the warning never stops the publish", got, want)
			}
		})
	}
}

func TestREADMEWarnsThatPublishingTrustsEveryMachine(t *testing.T) {
	data, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Publishing a port trusts every one of your machines with it.",
		"A service that listens only on localhost is published too.",
		"ssh -L 8080:localhost:8080 api.hib",
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("README.md doesn't say %q", want)
		}
	}
}

func TestLogTakesLinks(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	if code := c.hib("log", "--links", "--n", "5"); code == 2 {
		t.Errorf("hib log --links is a usage mistake: %s", c.errOut)
	}
}
