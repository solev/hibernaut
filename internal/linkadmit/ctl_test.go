package linkadmit_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/linkadmit"
	"github.com/solev/hibernaut/internal/linkctl"
	"github.com/solev/hibernaut/internal/links"
)

// socketPath returns a socket path short enough for macOS's 104 bytes, in a directory the test
// removes.
func socketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(".", "s")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "l.sock")
}

// call sends one request line and returns the reply line.
func call(path, line string) (string, error) {
	c, err := net.Dial("unix", path)
	if err != nil {
		return "", err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := fmt.Fprintln(c, line); err != nil {
		return "", err
	}
	reply, err := bufio.NewReader(c).ReadString('\n')
	return strings.TrimSpace(reply), err
}

// revoke cancels a pending admission waiting on a wake, and leaves one to another port of the
// same target waiting on the same wake.
func TestRevokeCancelsALinkMidWake(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		v := newEnv(t)
		v.source("src")
		v.target("web", engine.Paused, 8080, 5432)
		release := v.e.holdResumes()
		web := v.admitLater("src", "web", 8080)
		synctest.Wait()
		db := v.admitLater("src", "web", 5432)
		synctest.Wait()

		resp := linkadmit.Handle(v.a, context.Background(), linkctl.Request{Op: "revoke", ID: "web", Ports: []int{8080}})
		if resp.Err != "" {
			t.Fatalf("revoke: %s", resp.Err)
		}
		if r := <-web; !errors.Is(r.err, links.ErrRevoked) {
			t.Errorf("the revoked admission = %+v, %v; want %v", r.ad, r.err, links.ErrRevoked)
		}
		close(release)
		r := <-db
		if r.err != nil {
			t.Fatalf("the other port's admission = %v", r.err)
		}
		v.a.Release(r.ad)
		if got := v.closer.ports["web"]; !slices.Equal(got, []int{8080}) {
			t.Errorf("the closer was asked to abort web's ports %v, want [8080]", got)
		}
	})
}

// revoke cancels an open link to the port and leaves the link to the target's other port.
func TestRevokeCancelsAnOpenLinkOnThatPortOnly(t *testing.T) {
	v := newEnv(t)
	v.source("src")
	v.target("web", engine.Running, 8080, 5432)
	web, err := v.admit("src", "web", 8080)
	if err != nil {
		t.Fatal(err)
	}
	defer v.a.Release(web)
	db, err := v.admit("src", "web", 5432)
	if err != nil {
		t.Fatal(err)
	}
	defer v.a.Release(db)
	v.closer.n = 1

	resp := linkadmit.Handle(v.a, t.Context(), linkctl.Request{Op: "revoke", ID: "web", Ports: []int{8080}})
	if resp.Err != "" || resp.Closed != 1 {
		t.Fatalf("revoke = %+v, want the closer's 1", resp)
	}
	if cause := context.Cause(web.Ctx); !errors.Is(cause, links.ErrRevoked) {
		t.Errorf("the revoked link's cause is %v, want %v", cause, links.ErrRevoked)
	}
	if db.Ctx.Err() != nil {
		t.Errorf("revoking 8080 closed the link to 5432: %v", context.Cause(db.Ctx))
	}
}

// A listener that does not acknowledge a close within LinkCloseGrace is overruled: the admitter
// drops the machine's gates in the kernel, which stops every packet of its links whatever the
// listener is doing.
func TestAnUnacknowledgedCloseReachesTheDropper(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		v := newEnv(t)
		v.source("src")
		v.target("web", engine.Running, 8080)
		addr := netip.MustParseAddr("10.11.0.7")
		v.guard.addrs["web"] = addr
		v.closer.block = true
		ad, err := v.admit("src", "web", 8080)
		if err != nil {
			t.Fatal(err)
		}
		defer v.a.Release(ad)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		start := time.Now()
		resp := linkadmit.Handle(v.a, ctx, linkctl.Request{Op: "close", ID: "web"})
		if waited := time.Since(start); waited != links.LinkCloseGrace {
			t.Errorf("the fallback ran after %v, want %v", waited, links.LinkCloseGrace)
		}
		if resp.Err != "" || resp.Closed != 1 {
			t.Errorf("close = %+v, want the one link the admitter held", resp)
		}
		if !slices.Equal(v.guard.dropped, []netip.Addr{addr}) {
			t.Errorf("dropped %v, want [%v]", v.guard.dropped, addr)
		}
		if cause := context.Cause(ad.Ctx); !errors.Is(cause, links.ErrFenced) {
			t.Errorf("the closed link's cause is %v, want %v", cause, links.ErrFenced)
		}

		v.guard.err = errors.New("operation not permitted")
		if resp := linkadmit.Handle(v.a, ctx, linkctl.Request{Op: "close", ID: "web"}); resp.Err == "" {
			t.Errorf("close with a failing drop = %+v, want an error", resp)
		}
	})
}

// The control socket answers for the closer and nothing else. A closer that is not serving holds
// no sockets and says so, and a closer's error is an error, never a count of 0.
func TestTheControlSocketAnswersForTheCloser(t *testing.T) {
	v := newEnv(t, func(c *linkadmit.Config) { c.Closer = stubCloser{} })
	for _, req := range []linkctl.Request{
		{Op: "revoke", ID: "web", Ports: []int{8080}},
		{Op: "close", ID: "web"},
		{Op: "stat"},
	} {
		resp := linkadmit.Handle(v.a, t.Context(), req)
		if b, _ := json.Marshal(resp); string(b) != `{"closed":0}` {
			t.Errorf("%s with a closer not serving = %s, want 0 and empty counts", req.Op, b)
		}
	}

	v = newEnv(t)
	v.closer.n, v.closer.err = 3, errors.New("listener wedged")
	for _, req := range []linkctl.Request{
		{Op: "revoke", ID: "web", Ports: []int{8080}},
		{Op: "close", ID: "web"},
	} {
		if resp := linkadmit.Handle(v.a, t.Context(), req); resp.Closed != 0 || !strings.Contains(resp.Err, "listener wedged") {
			t.Errorf("%s with a failing closer = %+v, want its error", req.Op, resp)
		}
	}
	for _, req := range []linkctl.Request{
		{Op: "reboot"},
		{Op: "close", ID: "../web"},
		{Op: "revoke", ID: "web"},
		{Op: "revoke", ID: "web", Ports: []int{70000}},
	} {
		if resp := linkadmit.Handle(v.a, t.Context(), req); resp.Err == "" {
			t.Errorf("%+v = %+v, want an error", req, resp)
		}
	}
}

// hibd's other invocations reach the admitter over the socket: one JSON line in, one out. The
// socket replaces one a killed proxy left behind, is the owner's alone, refuses to start beside
// a live one and is gone once Serve returns.
func TestTheControlSocketAcrossProcesses(t *testing.T) {
	path := socketPath(t)
	v := newEnv(t, func(c *linkadmit.Config) { c.Socket = path })
	v.closer.n = 2
	v.closer.stats = links.Stats{
		Ports:    map[links.Open]int{{Target: "web", Port: 8080}: 2, {Target: "db", Port: 5432}: 1, {Target: "web", Port: 22}: 1},
		Machines: map[string]int{"web": 3, "db": 1, "src": 4},
	}
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	stale.Close()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- v.a.Serve(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := call(path, `{"op":"stat"}`); err == nil {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("the socket never answered: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if fi, err := os.Lstat(path); err != nil || fi.Mode().Type() != fs.ModeSocket || fi.Mode().Perm() != 0o600 {
		t.Errorf("socket = %v, %v; want a socket with mode 0600", fi.Mode(), err)
	}

	for _, tc := range []struct{ req, want string }{
		{`{"op":"revoke","id":"web","ports":[8080]}`, `{"closed":2}`},
		{`{"op":"close","id":"web"}`, `{"closed":2}`},
		{`{"op":"stat"}`, `{"closed":0,"ports":[{"target":"db","port":5432,"open":1},{"target":"web","port":22,"open":1},{"target":"web","port":8080,"open":2}],"machines":{"db":1,"src":4,"web":3}}`},
		{`not json`, `{"closed":0,"err":"bad request: invalid character 'o' in literal null (expecting 'u')"}`},
	} {
		got, err := call(path, tc.req)
		if err != nil || got != tc.want {
			t.Errorf("%s = %s, %v; want %s", tc.req, got, err, tc.want)
		}
	}

	second, err := linkadmit.New(linkadmit.Config{Closer: stubCloser{}, Socket: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Serve(ctx); err == nil || !strings.Contains(err.Error(), "another") {
		t.Errorf("a second proxy on the same socket = %v, want a refusal", err)
	}

	cancel()
	if err := <-served; err != nil {
		t.Errorf("serve = %v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the socket outlived Serve: %v", err)
	}
}

// Serve refuses to remove anything at its path that is not a socket.
func TestServeLeavesAFileThatIsNotASocket(t *testing.T) {
	path := socketPath(t)
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	v := newEnv(t, func(c *linkadmit.Config) { c.Socket = path })
	if err := v.a.Serve(t.Context()); err == nil {
		t.Errorf("serving over a regular file succeeded")
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "keep" {
		t.Errorf("the file is now %q, %v", b, err)
	}
}
