package linkctl

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/links"
)

func TestRevokeSendsOneLineAndReadsClosed(t *testing.T) {
	srv := serve(t, func(Request) Response { return Response{Closed: 2} })
	c := client(t, srv.path)

	closed, err := c.Revoke(context.Background(), "sbx1", []int{8080, 5432})
	if err != nil {
		t.Fatal(err)
	}
	if closed != 2 {
		t.Errorf("Revoke closed %d, want 2", closed)
	}
	if got, want := srv.lines(), []string{`{"op":"revoke","id":"sbx1","ports":[8080,5432]}`}; !reflect.DeepEqual(got, want) {
		t.Errorf("wire = %q, want %q", got, want)
	}
}

func TestCloseSendsTheMachine(t *testing.T) {
	srv := serve(t, func(Request) Response { return Response{Closed: 3} })
	c := client(t, srv.path)

	closed, err := c.Close(context.Background(), "sbx1")
	if err != nil {
		t.Fatal(err)
	}
	if closed != 3 {
		t.Errorf("Close closed %d, want 3", closed)
	}
	if got, want := srv.lines(), []string{`{"op":"close","id":"sbx1"}`}; !reflect.DeepEqual(got, want) {
		t.Errorf("wire = %q, want %q", got, want)
	}
}

func TestStatTurnsThePortListBackIntoAMap(t *testing.T) {
	srv := serve(t, func(Request) Response {
		return Response{
			Ports:    []OpenPort{{Target: "api", Port: 8080, Open: 2}, {Target: "db", Port: 5432, Open: 1}},
			Machines: map[string]int{"api": 2, "db": 1, "web": 3},
		}
	})
	c := client(t, srv.path)

	stats, err := c.Stat(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := links.Stats{
		Ports:    map[links.Open]int{{Target: "api", Port: 8080}: 2, {Target: "db", Port: 5432}: 1},
		Machines: map[string]int{"api": 2, "db": 1, "web": 3},
	}
	if !reflect.DeepEqual(stats, want) {
		t.Errorf("Stat = %+v, want %+v", stats, want)
	}
	if got, want := srv.lines(), []string{`{"op":"stat"}`}; !reflect.DeepEqual(got, want) {
		t.Errorf("wire = %q, want %q", got, want)
	}
}

func TestStatWithNothingOpenHasEmptyMaps(t *testing.T) {
	srv := serve(t, func(Request) Response { return Response{} })
	c := client(t, srv.path)

	stats, err := c.Stat(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Ports == nil || stats.Machines == nil || len(stats.Ports) != 0 || len(stats.Machines) != 0 {
		t.Errorf("Stat = %+v, want empty non-nil maps", stats)
	}
}

func TestReplyErrComesBackAsAnError(t *testing.T) {
	tests := []struct {
		reply string
		is    error
	}{
		{reply: "links are off", is: links.ErrLinksOff},
		{reply: "close sbx1: pause in progress", is: links.ErrFenced},
		{reply: "the closer is wedged"},
	}
	for _, tt := range tests {
		t.Run(tt.reply, func(t *testing.T) {
			srv := serve(t, func(Request) Response { return Response{Closed: 1, Err: tt.reply} })
			c := client(t, srv.path)

			closed, err := c.Close(context.Background(), "sbx1")
			if err == nil {
				t.Fatal("Close succeeded, want the reply's error")
			}
			if closed != 1 {
				t.Errorf("Close closed %d, want the reply's 1", closed)
			}
			if tt.is != nil && !errors.Is(err, tt.is) {
				t.Errorf("err = %v, want errors.Is %v", err, tt.is)
			}
			if !strings.Contains(err.Error(), tt.reply) {
				t.Errorf("err = %q, want it to carry %q", err, tt.reply)
			}
		})
	}
}

func TestNoReplyWithinTheTimeoutIsAnError(t *testing.T) {
	srv := serve(t, nil)
	c := client(t, srv.path)
	c.timeout = 50 * time.Millisecond

	start := time.Now()
	_, err := c.Revoke(context.Background(), "sbx1", []int{8080})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Revoke = %v, want a deadline error", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("Revoke took %s past a 50ms timeout", took)
	}

	ctx, cancel := context.WithCancel(context.Background())
	c.timeout = links.CtlTimeout
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	if _, err := c.Stat(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Stat = %v, want the caller's cancellation", err)
	}
}

func TestMissingSocketIsAnError(t *testing.T) {
	c := client(t, filepath.Join(shortDir(t), "none.sock"))
	if _, err := c.Close(context.Background(), "sbx1"); err == nil {
		t.Fatal("Close succeeded with no socket")
	}
}

func TestBadCallsAreRefusedBeforeDialling(t *testing.T) {
	srv := serve(t, func(Request) Response { return Response{} })
	c := client(t, srv.path)

	if _, err := c.Revoke(context.Background(), "sbx1", nil); err == nil {
		t.Error("Revoke with no ports succeeded")
	}
	if _, err := c.Revoke(context.Background(), "", []int{8080}); err == nil {
		t.Error("Revoke with no ID succeeded")
	}
	if _, err := c.Close(context.Background(), ""); err == nil {
		t.Error("Close with no ID succeeded")
	}
	if got := srv.lines(); len(got) != 0 {
		t.Errorf("server saw %q, want nothing", got)
	}
}

func TestDialDefaultsToTheControlSocket(t *testing.T) {
	c, err := Dial("")
	if err != nil {
		t.Fatal(err)
	}
	if c.Path() != links.CtlSocket {
		t.Errorf("Path = %q, want %q", c.Path(), links.CtlSocket)
	}
}

type fakeServer struct {
	path string

	mu   sync.Mutex
	seen []string
}

func (s *fakeServer) lines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

// serve answers each connection's one line with reply, or never answers when reply is nil.
func serve(t *testing.T, reply func(Request) Response) *fakeServer {
	t.Helper()
	s := &fakeServer{path: filepath.Join(shortDir(t), "ctl.sock")}
	l, err := net.Listen("unix", s.path)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	t.Cleanup(func() {
		_ = l.Close()
		wg.Wait()
	})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer conn.Close()
				line, err := bufio.NewReader(conn).ReadString('\n')
				if err != nil {
					return
				}
				s.mu.Lock()
				s.seen = append(s.seen, strings.TrimSuffix(line, "\n"))
				s.mu.Unlock()
				if reply == nil {
					_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
					_, _ = conn.Read(make([]byte, 1))
					return
				}
				var req Request
				if err := json.Unmarshal([]byte(line), &req); err != nil {
					return
				}
				_ = json.NewEncoder(conn).Encode(reply(req))
			}()
		}
	}()
	return s
}

func client(t *testing.T, path string) *Client {
	t.Helper()
	c, err := Dial(path)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// shortDir is a fresh directory with a path short enough for a unix socket, which macOS caps at
// 104 bytes and t.TempDir can exceed.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "linkctl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
