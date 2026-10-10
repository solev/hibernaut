package hibd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/egress/hook"
	"github.com/solev/hibernaut/internal/linkctl"
	"github.com/solev/hibernaut/internal/links"
)

type fakeGuard struct {
	installErr error
	installed  atomic.Bool
}

func (g *fakeGuard) Install() error {
	if g.installErr != nil {
		return g.installErr
	}
	g.installed.Store(true)
	return nil
}

func (g *fakeGuard) Installed() (bool, error)     { return g.installed.Load(), nil }
func (g *fakeGuard) Remove() error                { g.installed.Store(false); return nil }
func (g *fakeGuard) Open(links.Gate) error        { return nil }
func (g *fakeGuard) Extend([]links.Gate) error    { return nil }
func (g *fakeGuard) Drop(netip.Addr) error        { return nil }
func (g *fakeGuard) Has(links.Gate) (bool, error) { return true, nil }

type fakeTokens struct{ last atomic.Uint32 }

func (t *fakeTokens) Next() (uint32, error) { return t.last.Add(1), nil }

// listenLog records ProxyListen's calls from whichever goroutine makes them.
type listenLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *listenLog) add(call string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, call)
}

func (l *listenLog) get() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.calls)
}

// linksOn configures proxyApp's app with links on, over guard, the given checks, a recheck of
// 20 ms and a control socket in a short directory. ProxyListen hands the egress port egress and
// the link port whatever linkListen returns.
func linksOn(t *testing.T, a *App, env map[string]string, egress net.Listener, guard links.Guard, linkListen func() (net.Listener, error), checks ...linkCheck) (string, *listenLog) {
	t.Helper()
	dir, err := os.MkdirTemp(".", "s")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	state := env["HIBD_STATE_DIR"]
	env["HIBD_LINK_PORT"] = "5444"
	env["HIBD_CGROUPS"] = filepath.Join(state, "cgroups")
	env["HIBD_LINK_LOG"] = filepath.Join(state, "log", "links.jsonl")
	env["HIBD_QUIESCING"] = filepath.Join(state, "quiescing")
	socket := filepath.Join(dir, "l.sock")
	saved := proxyLinkHost
	t.Cleanup(func() { proxyLinkHost = saved })
	proxyLinkHost = linkHost{
		guard:   func() links.Guard { return guard },
		tokens:  func() links.Tokens { return new(fakeTokens) },
		socket:  socket,
		checks:  checks,
		recheck: 20 * time.Millisecond,
	}
	listens := &listenLog{}
	a.ProxyListen = func(network, address string) (net.Listener, error) {
		listens.add(network + " " + address)
		if address == ":5444" {
			return linkListen()
		}
		return egress, nil
	}
	return socket, listens
}

// closeHook is a listener that calls onClose the first time it is closed.
type closeHook struct {
	net.Listener
	once    sync.Once
	onClose func()
}

func (l *closeHook) Close() error {
	l.once.Do(l.onClose)
	return l.Listener.Close()
}

// breakingListener accepts nothing, and fails once broken.
type breakingListener struct {
	broken, closed chan struct{}
	once           sync.Once
}

func newBreakingListener() *breakingListener {
	return &breakingListener{broken: make(chan struct{}), closed: make(chan struct{})}
}

func (l *breakingListener) Accept() (net.Conn, error) {
	select {
	case <-l.broken:
		return nil, errors.New("the listener broke")
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *breakingListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *breakingListener) Addr() net.Addr { return &net.TCPAddr{} }

func passing(name string) linkCheck {
	return linkCheck{name: name, run: func(context.Context) error { return nil }}
}

// runProxyCmd runs hibd proxy until the returned stop is called, which returns its exit code,
// or -1 when it did not stop.
func runProxyCmd(t *testing.T, a *App) (stop func() int) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan int, 1)
	go func() { done <- a.Run(ctx, []string{"proxy"}) }()
	var once sync.Once
	code := -1
	stop = func() int {
		once.Do(func() {
			cancel()
			select {
			case code = <-done:
			case <-time.After(10 * time.Second):
				t.Error("proxy did not stop")
			}
		})
		return code
	}
	t.Cleanup(func() { stop() })
	return stop
}

func waitLogged(t *testing.T, a *App, line string) {
	t.Helper()
	logs := a.Stderr.(*syncBuffer)
	waitFor(t, logs, fmt.Sprintf("the proxy logs %q", line), func() bool { return strings.Contains(logs.String(), line) })
}

func socketMode(path string) (fs.FileMode, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	return fi.Mode(), nil
}

// The proxy serves its control socket where hibd's pause, rm and port commands dial it, in the
// directory the tmpfiles entry makes and hibd-proxy.service may write.
func TestProxyServesTheControlSocketWhereHibdDialsIt(t *testing.T) {
	ctl, err := linkctl.Dial("")
	if err != nil {
		t.Fatal(err)
	}
	if proxyLinkHost.socket != ctl.Path() {
		t.Errorf("hibd proxy serves its control socket at %s, but hibd dials %s", proxyLinkHost.socket, ctl.Path())
	}
	if dir := filepath.Dir(proxyLinkHost.socket); dir != links.RunDir {
		t.Errorf("the control socket is in %s, want %s", dir, links.RunDir)
	}
}

func TestProxyHookCarriesTheLinkRuleOnlyWithLinksOn(t *testing.T) {
	a := &App{}
	if got, want := a.proxyHook(5443, 5444), (proxyRule{hook.Rule{ProxyPort: 5443, LinkPort: 5444, LinkCIDR: links.CIDR}}); got != want {
		t.Errorf("links on: hook = %+v, want %+v", got, want)
	}
	if got, want := a.proxyHook(5443, 0), (proxyRule{hook.Rule{ProxyPort: 5443}}); got != want {
		t.Errorf("links off: hook = %+v, want %+v", got, want)
	}
}

func TestProxyServesLinksBesideEgress(t *testing.T) {
	rule := newFakeHook()
	a, env, egress, _ := proxyApp(t, rule)
	guard := &fakeGuard{}
	var socket string
	var link *closeHook
	var socketAtClose error
	socket, listens := linksOn(t, a, env, egress, guard, func() (net.Listener, error) {
		// Links come last: the guard is in and the control socket answers.
		if !guard.installed.Load() {
			t.Error("links listened before the guard was installed")
		}
		ctl, _ := linkctl.Dial(socket)
		if _, err := ctl.Stat(t.Context()); err != nil {
			t.Errorf("links listened before the control socket answered: %v", err)
		}
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		link = &closeHook{Listener: l, onClose: func() { _, socketAtClose = os.Lstat(socket) }}
		return link, nil
	}, passing("the sandbox firewall"), passing("the orchestrator's IPAddressDeny"))
	stop := runProxyCmd(t, a)

	waitLogged(t, a, "serving machine links on port 5444")
	assertServes(t, egress.Addr().String())
	assertServes(t, link.Addr().String())
	if mode, err := socketMode(socket); err != nil || mode.Type() != fs.ModeSocket || mode.Perm() != 0o600 {
		t.Fatalf("control socket: mode %v, %v, want a socket of mode 0600", mode, err)
	}

	if code := stop(); code != 0 {
		t.Fatalf("exit %d: %s", code, a.Stderr)
	}
	if socketAtClose != nil {
		t.Errorf("the control socket was gone before links closed: %v", socketAtClose)
	}
	if _, err := os.Lstat(socket); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the control socket is still there after shutdown: %v", err)
	}
	if want := []string{"tcp :5443", "tcp :5444"}; !slices.Equal(listens.get(), want) {
		t.Errorf("listened on %q, want %q", listens.get(), want)
	}
	if calls := rule.Calls(); !slices.Equal(calls, []string{"ensure", "keep", "remove"}) {
		t.Errorf("hook calls = %q", calls)
	}
	if _, err := os.Stat(env["HIBD_LINK_LOG"]); err != nil {
		t.Errorf("link log: %v", err)
	}
	for _, l := range []net.Listener{egress, link} {
		if c, err := net.DialTimeout("tcp", l.Addr().String(), time.Second); err == nil {
			c.Close()
			t.Errorf("%s still accepts after shutdown", l.Addr())
		}
	}
}

func TestProxyStopsEgressAndLinksTogether(t *testing.T) {
	for _, breaks := range []string{"egress", "links"} {
		t.Run(breaks, func(t *testing.T) {
			rule := newFakeHook()
			a, env, egress, _ := proxyApp(t, rule)
			broken := newBreakingListener()
			// other is the listener that did not break, which must close all the same.
			var egressListener, linkListener, other net.Listener = egress, broken, egress
			if breaks == "egress" {
				l, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { l.Close() })
				egressListener, linkListener, other = broken, l, l
			}
			socket, _ := linksOn(t, a, env, egressListener, &fakeGuard{}, func() (net.Listener, error) { return linkListener, nil })
			stop := runProxyCmd(t, a)
			waitLogged(t, a, "serving machine links on port 5444")
			close(broken.broken)
			waitFor(t, a.Stderr.(*syncBuffer), "the control socket is gone", func() bool {
				_, err := os.Lstat(socket)
				return errors.Is(err, fs.ErrNotExist)
			})
			want := "the listener broke"
			if breaks == "links" {
				want = "serving machine links: the listener broke"
			}
			if code := stop(); code != 1 || !strings.Contains(fmt.Sprint(a.Stderr), want) {
				t.Fatalf("exit %d, want 1 and %q: %s", code, want, a.Stderr)
			}
			if calls := rule.Calls(); !slices.Equal(calls, []string{"ensure", "keep", "remove"}) {
				t.Errorf("hook calls = %q", calls)
			}
			if c, err := net.DialTimeout("tcp", other.Addr().String(), time.Second); err == nil {
				c.Close()
				t.Errorf("the other listener still accepts after %s broke", breaks)
			}
		})
	}
}

func TestProxyServesTheControlSocketWhileTheGuardIsDown(t *testing.T) {
	rule := newFakeHook()
	a, env, egress, _ := proxyApp(t, rule)
	guard := &fakeGuard{installErr: errors.New("no nftables here")}
	socket, listens := linksOn(t, a, env, egress, guard, func() (net.Listener, error) {
		t.Error("links listened with the guard down")
		return nil, errors.New("not in this test")
	}, passing("the sandbox firewall"))
	stop := runProxyCmd(t, a)

	waitLogged(t, a, "links are off until hibd proxy restarts: the link guard: linkguard: install the guard: no nftables here")
	assertServes(t, egress.Addr().String())
	ctl, _ := linkctl.Dial(socket)
	waitFor(t, a.Stderr.(*syncBuffer), "the control socket answers", func() bool {
		_, err := ctl.Stat(t.Context())
		return err == nil
	})
	stats, err := ctl.Stat(t.Context())
	if err != nil || len(stats.Ports) != 0 || len(stats.Machines) != 0 {
		t.Fatalf("stat = %+v, %v, want zero counts", stats, err)
	}
	if n, err := ctl.Close(t.Context(), "abc123"); n != 0 || err != nil {
		t.Fatalf("close = %d, %v, want nothing closed", n, err)
	}
	// Its Ready never closes, so checking again would only repeat the same line.
	time.Sleep(5 * proxyLinkHost.recheck)
	if logs := fmt.Sprint(a.Stderr); strings.Count(logs, "links are off") != 1 {
		t.Errorf("want one line saying why links are off: %s", logs)
	}

	if code := stop(); code != 0 {
		t.Fatalf("exit %d: %s", code, a.Stderr)
	}
	if want := []string{"tcp :5443"}; !slices.Equal(listens.get(), want) {
		t.Errorf("listened on %q, want %q", listens.get(), want)
	}
	if _, err := os.Lstat(socket); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the control socket is still there after shutdown: %v", err)
	}
}

func TestProxyStartsLinksOnceTheChecksPass(t *testing.T) {
	rule := newFakeHook()
	a, env, egress, _ := proxyApp(t, rule)
	var fixed atomic.Bool
	firewall := linkCheck{name: "the sandbox firewall", run: func(context.Context) error {
		if !fixed.Load() {
			return errors.New("table inet hibernaut-sandboxes does not drop udp from veth-* to 198.18.0.0/15")
		}
		return nil
	}}
	linkUp := make(chan net.Listener, 1)
	var binds atomic.Int32
	_, listens := linksOn(t, a, env, egress, &fakeGuard{}, func() (net.Listener, error) {
		if binds.Add(1) == 1 {
			return nil, errors.New("address already in use")
		}
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err == nil {
			linkUp <- l
		}
		return l, err
	}, firewall, passing("the orchestrator's IPAddressDeny"))
	stop := runProxyCmd(t, a)

	waitLogged(t, a, "links are off, checking again in 20ms: the sandbox firewall: table inet hibernaut-sandboxes does not drop udp")
	assertServes(t, egress.Addr().String())
	if logs := fmt.Sprint(a.Stderr); strings.Contains(logs, "IPAddressDeny") {
		t.Fatalf("logged a check that passed: %s", logs)
	}
	if slices.Contains(listens.get(), "tcp :5444") {
		t.Fatal("links listened while a check failed")
	}
	fixed.Store(true)
	waitLogged(t, a, "links are off, checking again in 20ms: listening on port 5444: address already in use")
	select {
	case l := <-linkUp:
		assertServes(t, l.Addr().String())
	case <-time.After(10 * time.Second):
		t.Fatalf("links did not start once the checks passed: %s", a.Stderr)
	}
	if code := stop(); code != 0 {
		t.Fatalf("exit %d: %s", code, a.Stderr)
	}
}

func TestProxyWithLinksOffHasNoLinksSide(t *testing.T) {
	rule := newFakeHook()
	a, _, listener, listen := proxyApp(t, rule)
	dir, err := os.MkdirTemp(".", "s")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	saved := proxyLinkHost
	t.Cleanup(func() { proxyLinkHost = saved })
	proxyLinkHost = linkHost{
		guard:  func() links.Guard { t.Error("links off built a guard"); return &fakeGuard{} },
		tokens: func() links.Tokens { return new(fakeTokens) },
		socket: filepath.Join(dir, "l.sock"),
	}
	stop := runProxyCmd(t, a)
	select {
	case <-rule.kept:
	case <-time.After(5 * time.Second):
		t.Fatal("proxy did not keep the hook")
	}
	assertServes(t, listener.Addr().String())
	if _, err := os.Lstat(proxyLinkHost.socket); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("links off made a control socket: %v", err)
	}
	if code := stop(); code != 0 {
		t.Fatalf("exit %d: %s", code, a.Stderr)
	}
	if want := []string{"tcp :5443"}; !slices.Equal(*listen, want) {
		t.Errorf("listened on %q, want %q", *listen, want)
	}
	if logs := fmt.Sprint(a.Stderr); strings.Contains(logs, "links") {
		t.Errorf("links off logged about links: %s", logs)
	}
}
