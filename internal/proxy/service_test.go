package proxy_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/egress"
	"github.com/solev/hibernaut/internal/egress/hook"
	"github.com/solev/hibernaut/internal/proxy"
)

type testRule struct {
	started chan struct{}
	removed chan struct{}
	fail    error
}

func (r *testRule) Ensure(context.Context) (bool, error) { return true, r.fail }
func (r *testRule) Keep(ctx context.Context, _ time.Duration, _ func(string, ...any)) error {
	close(r.started)
	<-ctx.Done()
	return nil
}
func (r *testRule) Remove(ctx context.Context) error {
	if ctx.Err() != nil {
		return errors.New("unhook used a cancelled context")
	}
	close(r.removed)
	return nil
}

type orderedListener struct {
	net.Listener
	removed <-chan struct{}
	t       *testing.T
	once    sync.Once
}

func (l *orderedListener) Close() error {
	l.once.Do(func() {
		select {
		case <-l.removed:
		default:
			l.t.Error("listener closed before the rule was removed")
		}
	})
	return l.Listener.Close()
}

type closeAudit struct {
	listener *orderedListener
	closed   bool
	t        *testing.T
}

func (a *closeAudit) Close() error {
	a.closed = true
	if conn, err := net.DialTimeout("tcp", a.listener.Addr().String(), time.Second); err == nil {
		conn.Close()
		a.t.Error("audit closed before serving stopped")
	}
	return nil
}

func TestServiceUnhooksBeforeStoppingAndClosesAudit(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "ensure failure"}[fail], func(t *testing.T) {
			rule := &testRule{started: make(chan struct{}), removed: make(chan struct{})}
			if fail {
				rule.fail = errors.New("ensure failed after insertion")
			}
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			listener := &orderedListener{Listener: l, removed: rule.removed, t: t}
			audit := &closeAudit{listener: listener, t: t}
			service := &proxy.Service{Server: &egress.Server{}, Rule: rule, Audit: audit}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- service.Serve(ctx, listener) }()
			if !fail {
				select {
				case <-rule.started:
				case <-time.After(time.Second):
					t.Fatal("keeper did not start")
				}
				cancel()
			}
			select {
			case err := <-done:
				if fail && !errors.Is(err, rule.fail) || !fail && err != nil {
					t.Fatalf("Serve returned %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("service did not stop")
			}
			if !audit.closed {
				t.Fatal("audit log was not closed")
			}
		})
	}
}

func TestServiceLogsAStrayRuleAndKeepsServing(t *testing.T) {
	rule := &testRule{started: make(chan struct{}), removed: make(chan struct{})}
	rule.fail = fmt.Errorf("%w: left a rule in place", hook.ErrStrayRule)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	listener := &orderedListener{Listener: l, removed: rule.removed, t: t}
	var mu sync.Mutex
	var logs []string
	service := &proxy.Service{Server: &egress.Server{}, Rule: rule, Audit: &closeAudit{listener: listener, t: t}, Logf: func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		logs = append(logs, fmt.Sprintf(format, args...))
	}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- service.Serve(ctx, listener) }()
	select {
	case <-rule.started:
	case err := <-done:
		t.Fatalf("Serve returned %v on a stray rule", err)
	case <-time.After(time.Second):
		t.Fatal("keeper did not start")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Serve returned %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(logs) != 1 || !strings.Contains(logs[0], rule.fail.Error()) {
		t.Fatalf("logs = %q, want the stray rule warning", logs)
	}
}
