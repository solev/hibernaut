package machines

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/room"
)

type cutExecEngine struct {
	engine.Engine
	exec func(context.Context) (engine.ExecResult, error)
}

func (e *cutExecEngine) Exec(ctx context.Context, _ string, _ engine.ExecRequest) (engine.ExecResult, error) {
	return e.exec(ctx)
}

func TestCreateCancellationDuringCutBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, e := sharedStateManager(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		tries, waits := 0, 0
		m.e = &cutExecEngine{Engine: e, exec: func(context.Context) (engine.ExecResult, error) {
			tries++
			return engine.ExecResult{}, engine.ErrConnectionLost
		}}
		m.waitCut = func(ctx context.Context, delay time.Duration) error {
			waits++
			cancel()
			return waitCut(ctx, delay)
		}
		start := time.Now()
		_, err := m.Create(ctx, "web", "dev", DefaultIdle)
		if !errors.Is(err, context.Canceled) || !errors.Is(err, engine.ErrConnectionLost) || tries != 1 || waits != 1 {
			t.Errorf("Create error=%v tries=%d waits=%d, want cancellation after one try", err, tries, waits)
		}
		if time.Since(start) != 0 {
			t.Error("cancellation waited for the backoff timer")
		}
	})
}

func TestCreateCutTriesShareOneDeadline(t *testing.T) {
	for _, expire := range []bool{false, true} {
		t.Run(map[bool]string{false: "exhausted", true: "expired"}[expire], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				m, e := sharedStateManager(t)
				start := time.Now()
				wantDeadline := start.Add(2 * time.Minute)
				tries := 0
				var waits []time.Duration
				m.e = &cutExecEngine{Engine: e, exec: func(ctx context.Context) (engine.ExecResult, error) {
					tries++
					if deadline, ok := ctx.Deadline(); !ok || !deadline.Equal(wantDeadline) {
						t.Errorf("try %d received a different deadline", tries)
					}
					if expire && tries == 3 {
						<-ctx.Done()
						return engine.ExecResult{}, ctx.Err()
					}
					return engine.ExecResult{}, engine.ErrConnectionLost
				}}
				m.waitCut = func(ctx context.Context, delay time.Duration) error {
					waits = append(waits, delay)
					if deadline, ok := ctx.Deadline(); !ok || !deadline.Equal(wantDeadline) {
						t.Error("backoff received a different deadline")
					}
					return waitCut(ctx, delay)
				}
				_, err := m.Create(t.Context(), "web", "dev", DefaultIdle)
				wantErr, elapsed := engine.ErrConnectionLost, 3*time.Second
				if expire {
					wantErr, elapsed = context.DeadlineExceeded, 2*time.Minute
				}
				if !errors.Is(err, wantErr) || tries != 3 || !slices.Equal(waits, []time.Duration{time.Second, 2 * time.Second}) {
					t.Errorf("Create error=%v tries=%d waits=%v", err, tries, waits)
				}
				if time.Since(start) != elapsed {
					t.Errorf("elapsed=%v, want %v", time.Since(start), elapsed)
				}
				if expire && !strings.Contains(err.Error(), "machine-init") {
					t.Errorf("Create error %q doesn't name the step that ran out of time", err)
				}
			})
		})
	}
}

// refusingDial is an engine whose DialWebSocket returns errs one at a time, then resumes the
// machine and connects.
type refusingDial struct {
	engine.Engine
	errs  []error
	dials int
}

func (e *refusingDial) DialWebSocket(ctx context.Context, id string, _ int, timeout time.Duration) (net.Conn, error) {
	e.dials++
	if len(e.errs) > 0 {
		err := e.errs[0]
		e.errs = e.errs[1:]
		return nil, err
	}
	if _, err := e.Engine.Resume(ctx, id, timeout); err != nil {
		return nil, err
	}
	near, far := net.Pipe()
	far.Close()
	return near, nil
}

func (e *refusingDial) DialRunningWebSocket(ctx context.Context, id string, _ int) (net.Conn, error) {
	em, err := e.Engine.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if em.State != engine.Running {
		return nil, engine.ErrNotRunning
	}
	near, far := net.Pipe()
	far.Close()
	return near, nil
}

func pausedWeb(t *testing.T) (*Manager, *fake.Engine) {
	t.Helper()
	m, e := sharedStateManager(t)
	if _, err := m.Create(t.Context(), "web", "dev", DefaultIdle); err != nil {
		t.Fatal(err)
	}
	if err := m.Pause(t.Context(), "web"); err != nil {
		t.Fatal(err)
	}
	return m, e
}

func recordWaits(m *Manager) *[]time.Duration {
	var waits []time.Duration
	m.waitCut = func(ctx context.Context, delay time.Duration) error {
		waits = append(waits, delay)
		return waitCut(ctx, delay)
	}
	return &waits
}

var errNotPlaced = fmt.Errorf("E2B returned 503: Failed to place sandbox: %w", engine.ErrNotPlaced)

func TestDialRetriesAResumeNotPlaced(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, e := pausedWeb(t)
		d := &refusingDial{Engine: e, errs: []error{errNotPlaced, errNotPlaced, errNotPlaced}}
		m.e = d
		waits := recordWaits(m)
		start := time.Now()
		conn, err := m.Dial(t.Context(), "web")
		if err != nil {
			t.Fatalf("Dial = %v, want it to retry until the engine places the machine", err)
		}
		conn.Close()
		if want := []time.Duration{2 * time.Second, 2 * time.Second, 2 * time.Second}; d.dials != 4 || !slices.Equal(*waits, want) {
			t.Errorf("Dial dialed %d times, waiting %v, want 4 times, waiting %v", d.dials, *waits, want)
		}
		if elapsed := time.Since(start); elapsed != 6*time.Second {
			t.Errorf("Dial took %v, want 6s", elapsed)
		}
	})
}

var errStartFailed = fmt.Errorf("E2B returned 500: Failed to place sandbox: %w", engine.ErrStartFailed)

func TestDialRetriesAFailedResumeFor30s(t *testing.T) {
	other := errors.New("E2B returned 500: Failed to place sandbox")
	cases := []struct {
		name  string
		errs  []error
		dials int
		want  error
		tried string
	}{
		{"failed once", []error{errStartFailed}, 2, nil, ""},
		{"failed for 28s", slices.Repeat([]error{errStartFailed}, 15), 16, nil, ""},
		{"failed for 30s", slices.Repeat([]error{errStartFailed}, 16), 16, engine.ErrStartFailed, "(tried 16 times)"},
		{"failed 30s apart", slices.Concat([]error{errStartFailed}, slices.Repeat([]error{errNotPlaced}, 14), []error{errStartFailed}), 16, engine.ErrStartFailed, "(tried 16 times)"},
		{"other error", []error{other}, 1, other, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				m, e := pausedWeb(t)
				d := &refusingDial{Engine: e, errs: c.errs}
				m.e = d
				conn, err := m.Dial(t.Context(), "web")
				if err == nil {
					conn.Close()
				}
				if d.dials != c.dials || !errors.Is(err, c.want) || (c.want == nil) != (err == nil) {
					t.Errorf("Dial = %v after %d dials, want %v after %d", err, d.dials, c.want, c.dials)
				}
				if err != nil && !strings.Contains(err.Error(), c.tried) {
					t.Errorf("Dial = %v, want it to say %q", err, c.tried)
				}
				if c.tried == "" && err != nil && strings.Contains(err.Error(), "tried") {
					t.Errorf("Dial = %v, want no count of tries", err)
				}
			})
		})
	}
}

func TestDialStopsRetryingWhenTheHostIsFull(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, e := pausedWeb(t)
		d := &refusingDial{Engine: e, errs: []error{errNotPlaced, errNotPlaced}}
		m.e = d
		waits := recordWaits(m)
		reads := 0
		m.readRoom = func() (room.Host, error) {
			reads++
			free := int64(3125)
			if reads > 1 {
				free = 100
			}
			return room.Host{PageSize: 2 << 20, Pages: 3200, FreePages: free, DiskFree: 80 << 30}, nil
		}
		conn, err := m.Dial(t.Context(), "web")
		if err == nil {
			conn.Close()
		}
		if !errors.Is(err, room.ErrFull) || d.dials != 1 || len(*waits) != 0 {
			t.Errorf("Dial = %v after %d dials, waiting %v, want the host full after 1 dial", err, d.dials, *waits)
		}
	})
}

func TestDialStopsRetryingWhenCut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, e := pausedWeb(t)
		m.e = &refusingDial{Engine: e, errs: []error{errNotPlaced, errNotPlaced, errNotPlaced, errNotPlaced}}
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		conn, err := m.Dial(ctx, "web")
		if err == nil {
			conn.Close()
		}
		if !errors.Is(err, engine.ErrNotPlaced) || !errors.Is(err, context.DeadlineExceeded) ||
			!strings.Contains(err.Error(), "(tried 3 times). Stopped retrying") {
			t.Errorf("Dial = %v, want the refusal after 3 tries and the deadline", err)
		}
	})
}

// refusingResume is an engine whose Resume returns errs one at a time.
type refusingResume struct {
	engine.Engine
	errs    []error
	resumes int
}

func (e *refusingResume) Resume(ctx context.Context, id string, timeout time.Duration) (engine.Machine, error) {
	e.resumes++
	if len(e.errs) > 0 {
		err := e.errs[0]
		e.errs = e.errs[1:]
		return engine.Machine{}, err
	}
	return e.Engine.Resume(ctx, id, timeout)
}

func TestDialRetriesTheResumeToRepin(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, e := pausedWeb(t)
		em, err := m.lookup(t.Context(), "web")
		if err != nil {
			t.Fatal(err)
		}
		pin, err := m.pinPath(em.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(pin); err != nil {
			t.Fatal(err)
		}
		r := &refusingResume{Engine: e, errs: []error{errNotPlaced, errStartFailed}}
		m.e = &refusingDial{Engine: r}
		conn, err := m.Dial(t.Context(), "web")
		if err != nil {
			t.Fatalf("Dial = %v, want it to retry the resume to repin the machine", err)
		}
		conn.Close()
		if r.resumes != 4 {
			t.Errorf("Dial resumed %d times, want 3 to repin and 1 to connect", r.resumes)
		}
	})
}
