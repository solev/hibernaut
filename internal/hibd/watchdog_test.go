package hibd

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// listenNotify stands in for systemd's notification socket, and returns the env asking for a
// ping every 10 ms.
func listenNotify(t *testing.T) (*net.UnixConn, map[string]string) {
	t.Helper()
	dir, err := os.MkdirTemp(".", "w")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "n.sock")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn, map[string]string{"NOTIFY_SOCKET": path, "WATCHDOG_USEC": "30000", "WATCHDOG_PID": strconv.Itoa(os.Getpid())}
}

func readNotify(conn *net.UnixConn, wait time.Duration) (string, error) {
	_ = conn.SetReadDeadline(time.Now().Add(wait))
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	return string(buf[:n]), err
}

// assertPinged waits for a ping.
func assertPinged(t *testing.T, conn *net.UnixConn) {
	t.Helper()
	if got, err := readNotify(conn, 5*time.Second); err != nil || got != "WATCHDOG=1" {
		t.Fatalf("read %q, %v, want WATCHDOG=1", got, err)
	}
}

// assertQuiet reads what was sent already, and then wants nothing for ten pings' time.
func assertQuiet(t *testing.T, conn *net.UnixConn) {
	t.Helper()
	for range 1000 {
		if _, err := readNotify(conn, 100*time.Millisecond); err != nil {
			return
		}
	}
	t.Fatal("pings kept coming")
}

func TestWatchdogPingsOnlyWhenSystemdAsks(t *testing.T) {
	for _, tc := range []struct {
		name  string
		unset string
		set   map[string]string
		ended bool
	}{
		{name: "no socket", unset: "NOTIFY_SOCKET"},
		{name: "no interval", unset: "WATCHDOG_USEC"},
		{name: "a bad interval", set: map[string]string{"WATCHDOG_USEC": "soon"}},
		{name: "another process's", set: map[string]string{"WATCHDOG_PID": strconv.Itoa(os.Getpid() + 1)}},
		{name: "ended", ended: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, env := listenNotify(t)
			delete(env, tc.unset)
			for k, v := range tc.set {
				env[k] = v
			}
			ended := make(chan struct{})
			if tc.ended {
				close(ended)
			}
			a := &App{Getenv: func(k string) string { return env[k] }}
			done := make(chan struct{})
			go func() {
				defer close(done)
				a.watchdog(t.Context(), ended, nil, t.Logf)
			}()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("the watchdog went on")
			}
			assertQuiet(t, conn)
		})
	}
}

func TestProxyPingsTheWatchdogWithLinksOff(t *testing.T) {
	a, env, _, _ := proxyApp(t, newFakeHook())
	conn, notify := listenNotify(t)
	for k, v := range notify {
		env[k] = v
	}
	stop := runProxyCmd(t, a)
	assertPinged(t, conn)
	assertPinged(t, conn)
	if code := stop(); code != 0 {
		t.Fatalf("exit %d: %s", code, a.Stderr)
	}
	assertQuiet(t, conn)
}

func TestProxyPingsTheWatchdogWhileTheControlSocketAnswers(t *testing.T) {
	a, env, egress, _ := proxyApp(t, newFakeHook())
	conn, notify := listenNotify(t)
	for k, v := range notify {
		env[k] = v
	}
	socket, _ := linksOn(t, a, env, egress, &fakeGuard{}, func() (net.Listener, error) {
		return net.Listen("tcp", "127.0.0.1:0")
	})
	stop := runProxyCmd(t, a)
	waitLogged(t, a, "serving machine links on port 5444")
	assertPinged(t, conn)

	if err := os.Rename(socket, socket+".away"); err != nil {
		t.Fatal(err)
	}
	waitLogged(t, a, "not pinging systemd's watchdog: the link control socket: ")
	assertQuiet(t, conn)
	if err := os.Rename(socket+".away", socket); err != nil {
		t.Fatal(err)
	}
	waitLogged(t, a, "pinging systemd's watchdog again")
	assertPinged(t, conn)

	if code := stop(); code != 0 {
		t.Fatalf("exit %d: %s", code, a.Stderr)
	}
	if logs := a.Stderr.(*syncBuffer).String(); strings.Count(logs, "not pinging") != 1 {
		t.Errorf("want one line saying why the pings stopped: %s", logs)
	}
}
