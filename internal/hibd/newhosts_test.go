package hibd

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/hostsblock"
	"github.com/solev/hibernaut/internal/links"
)

// hostsEngine records each machine's /etc/hosts as hibd writes it, and how often hibd read or
// wrote one, and passes everything else on.
type hostsEngine struct {
	engine.Engine
	mu      sync.Mutex
	hosts   map[string]string
	touches int
}

func (e *hostsEngine) Exec(ctx context.Context, id string, req engine.ExecRequest) (engine.ExecResult, error) {
	read := req.Cmd == "cat" && slices.Equal(req.Args, []string{"/etc/hosts"})
	write := req.Cmd == "sh" && len(req.Args) == 5 && req.Args[2] == "hibd-hosts"
	if !read && !write {
		return e.Engine.Exec(ctx, id, req)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.touches++
	if read {
		return engine.ExecResult{Stdout: []byte(e.hosts[id])}, nil
	}
	next, err := base64.StdEncoding.DecodeString(req.Args[4])
	if err != nil {
		return engine.ExecResult{ExitCode: 64, Stderr: []byte(err.Error())}, nil
	}
	if e.hosts == nil {
		e.hosts = map[string]string{}
	}
	e.hosts[id] = string(next)
	return engine.ExecResult{}, nil
}

func (e *hostsEngine) hostsOf(id string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.hosts[id]
}

func (e *hostsEngine) hostsTouches() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.touches
}

// hostsApp is imageApp with a running machine web, made with links off, that publishes 8080 and
// 9090 at 198.18.0.1 in f, and an engine that records each /etc/hosts. Links are on when on is
// set. It returns web's ID.
func hostsApp(t *testing.T, on bool) (*App, *hostsEngine, *fakeLinks, *bytes.Buffer, *bytes.Buffer, string) {
	t.Helper()
	a, e, out, errOut := imageApp(t)
	code, r, err := runJSON(t, a, out, "new", "web", "-json")
	if code != 0 || err != nil || r.Machine == nil {
		t.Fatalf("new web = %d, %v; stderr %s", code, err, errOut)
	}
	id := r.Machine.ID
	f := &fakeLinks{doc: links.Doc{V: links.Version, Machines: map[string]*links.Machine{
		id: {Addr: netip.MustParseAddr("198.18.0.1"), Ports: []int{8080, 9090}},
	}}}
	withLinks(a, f, on)
	he := &hostsEngine{Engine: e}
	a.NewEngine = func() (engine.Engine, error) { return he, nil }
	out.Reset()
	errOut.Reset()
	return a, he, f, out, errOut, id
}

// madeBy is every machine a new or a fork reply carries.
func madeBy(t *testing.T, a *App, out, errOut *bytes.Buffer, args ...string) []string {
	t.Helper()
	code, r, err := runJSON(t, a, out, args...)
	if code != 0 || err != nil {
		t.Fatalf("%s = %d, %v; stderr %s", strings.Join(args, " "), code, err, errOut)
	}
	var ids []string
	if r.Machine != nil {
		ids = append(ids, r.Machine.ID)
	}
	for _, m := range r.Machines {
		ids = append(ids, m.ID)
	}
	if len(ids) == 0 {
		t.Fatalf("%s made nothing", strings.Join(args, " "))
	}
	return ids
}

var newAndFork = [][]string{{"new", "app", "-json"}, {"fork", "web", "-count", "2", "-json"}}

func TestNewAndForkStartWithTheHostsBlock(t *testing.T) {
	block, err := hostsblock.Render([]hostsblock.Host{{IP: "198.18.0.1", Name: "web.hib"}})
	if err != nil {
		t.Fatal(err)
	}
	want, err := hostsblock.Merge(nil, block)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range newAndFork {
		t.Run(args[0], func(t *testing.T) {
			a, he, _, out, errOut, _ := hostsApp(t, true)
			for _, id := range madeBy(t, a, out, errOut, args...) {
				if got := he.hostsOf(id); got != string(want) {
					t.Errorf("/etc/hosts of %s = %q, want %q", id, got, want)
				}
			}
		})
	}
}

func TestNewAndForkLeaveEtcHostsAloneWithLinksOff(t *testing.T) {
	for _, args := range newAndFork {
		t.Run(args[0], func(t *testing.T) {
			a, he, f, out, errOut, _ := hostsApp(t, false)
			madeBy(t, a, out, errOut, args...)
			if n := he.hostsTouches(); n != 0 {
				t.Errorf("hibd read or wrote /etc/hosts %d times", n)
			}
			if got := f.log(); len(got) != 0 {
				t.Errorf("links pieces used: %q", got)
			}
		})
	}
}

func TestNewAndForkGoAheadWithoutNamesWhenTheLinkStateIsUnreadable(t *testing.T) {
	for _, args := range newAndFork {
		t.Run(args[0], func(t *testing.T) {
			a, he, f, out, errOut, _ := hostsApp(t, true)
			f.readErr = errors.New("the link state is unreadable")
			madeBy(t, a, out, errOut, args...)
			who := map[string]string{"new": "app", "fork": "the copies of web"}[args[0]]
			if want := "warning: " + who + " will start without the other machines' names: the link state is unreadable\n"; !strings.Contains(errOut.String(), want) {
				t.Errorf("stderr = %q, want %q", errOut, want)
			}
			if n := he.hostsTouches(); n != 0 {
				t.Errorf("hibd read or wrote /etc/hosts %d times", n)
			}
		})
	}
}
