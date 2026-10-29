package machines

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/room"
	"github.com/solev/hibernaut/internal/task"
)

func TestTaskAdmissionSerializesPlacement(t *testing.T) {
	for _, other := range []string{"task", "person", "wake", "ssh"} {
		t.Run(other, func(t *testing.T) {
			m, e := sharedStateManager(t)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			var free atomic.Int64
			free.Store(4096)
			m.readRoom = func() (room.Host, error) { return placementHost(int(free.Load())), nil }
			started, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			m.e = &placementEngine{Engine: e, create: func(ctx context.Context, o engine.CreateOptions) (engine.Machine, error) {
				assertPlacementLocked(t, m.dir)
				close(started)
				select {
				case <-release:
				case <-ctx.Done():
					return engine.Machine{}, ctx.Err()
				}
				em, err := e.Create(ctx, o)
				if err == nil {
					em.MemoryMB, em.CPUs = 2048, 2
					e.Add(em)
					free.Store(2048)
				}
				return em, err
			}}
			first := make(chan error, 1)
			go func() {
				_, err := m.CreateTask(ctx, placementRun("t-aaaaaaaa", "medium"), time.Hour)
				first <- err
			}()
			select {
			case <-started:
			case err := <-first:
				t.Fatalf("first admission did not reach E2B: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			second := New(Config{Engine: e, StateDir: m.dir, AuthorizedKeys: m.keysFile, ReadRoom: m.readRoom})
			e.Add(engine.Machine{ID: "target", State: engine.Paused, MemoryMB: 4096, CPUs: 4,
				Metadata: map[string]string{labelName: "target", labelIdle: DefaultIdle.String()}})
			if err := second.writePin("target", mustReadPlacementKey(t, m)); err != nil {
				t.Fatal(err)
			}
			done, entered := make(chan error, 1), make(chan struct{})
			go func() {
				close(entered)
				var err error
				switch other {
				case "task":
					_, err = second.CreateTask(ctx, placementRun("t-bbbbbbbb", "medium"), time.Hour)
				case "person":
					size, _ := images.SizeNamed("large")
					_, err = second.CreateSize(ctx, "person", "dev", size, DefaultIdle)
				case "wake":
					_, err = second.Wake(ctx, "target", time.Now().Add(time.Hour))
				case "ssh":
					var c *Conn
					c, err = second.Dial(ctx, "target")
					if c != nil {
						c.Close()
					}
				}
				done <- err
			}()
			<-entered
			select {
			case err := <-done:
				t.Fatalf("second placement passed the held admission: %v", err)
			case <-time.After(50 * time.Millisecond):
			}
			unblock()
			if err := <-first; err != nil {
				t.Fatal(err)
			}
			if err := <-done; !errors.Is(err, room.ErrFull) {
				t.Fatalf("second placement = %v, want ErrFull after the first spent its pages", err)
			}
		})
	}
}

func mustReadPlacementKey(t *testing.T, m *Manager) ssh.PublicKey {
	t.Helper()
	keys, err := m.authorizedKeys()
	if err != nil {
		t.Fatal(err)
	}
	key, _, _, _, err := ssh.ParseAuthorizedKey(keys)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestPlacementRefusalDoesNotRelockRoom(t *testing.T) {
	for _, op := range []string{"create", "task", "resume task", "wake", "ssh"} {
		t.Run(op, func(t *testing.T) {
			m, e := sharedStateManager(t)
			e.Add(engine.Machine{ID: "target", State: engine.Paused, MemoryMB: 2048, CPUs: 2,
				Metadata: map[string]string{labelName: "t-aaaaaaaa", task.Label: "t-aaaaaaaa"}})
			if err := m.checkState(); err != nil {
				t.Fatal(err)
			}
			if err := m.writePin("target", mustReadPlacementKey(t, m)); err != nil {
				t.Fatal(err)
			}
			full := false
			m.readRoom = func() (room.Host, error) {
				if full {
					return placementHost(0), nil
				}
				return placementHost(8192), nil
			}
			refuse := func() {
				assertPlacementLocked(t, m.dir)
				full = true
			}
			m.e = &placementEngine{Engine: e,
				create: func(context.Context, engine.CreateOptions) (engine.Machine, error) {
					refuse()
					return engine.Machine{}, engine.ErrNotPlaced
				},
				resume: func(context.Context, string, time.Duration) (engine.Machine, error) {
					refuse()
					return engine.Machine{}, engine.ErrNotPlaced
				},
			}
			if op == "ssh" {
				m.e = &refusalSocket{Engine: m.e, refuse: refuse}
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			at := time.Now()
			var err error
			switch op {
			case "create":
				_, err = m.Create(ctx, "person", "dev", DefaultIdle)
			case "task":
				_, err = m.CreateTask(ctx, placementRun("t-bbbbbbbb", "medium"), time.Hour)
			case "resume task":
				_, err = m.ResumeTask(ctx, placementRun("t-aaaaaaaa", "medium"), "target", time.Hour)
			case "wake":
				_, err = m.Wake(ctx, "target", time.Now().Add(time.Hour))
			case "ssh":
				_, err = m.Dial(ctx, "t-aaaaaaaa")
			}
			if !errors.Is(err, room.ErrFull) || time.Since(at) >= 500*time.Millisecond {
				t.Fatalf("placement = %v after %v, want prompt ErrFull without recursive flock", err, time.Since(at))
			}
		})
	}
}

type refusalSocket struct {
	engine.Engine
	refuse func()
}

func (e *refusalSocket) DialWebSocket(context.Context, string, int, time.Duration) (net.Conn, error) {
	e.refuse()
	return nil, engine.ErrNotPlaced
}

func TestHostAdmissionAcrossProcesses(t *testing.T) {
	if dir := os.Getenv("HIB_ROOM_TEST_DIR"); dir != "" {
		e := fake.New()
		e.Add(engine.Machine{ID: "target", State: engine.Paused, MemoryMB: 4096, CPUs: 4,
			Metadata: map[string]string{labelName: "target"}})
		m := New(Config{Engine: e, StateDir: dir, ReadRoom: func() (room.Host, error) {
			_, err := os.Stat(filepath.Join(dir, "spent"))
			if err == nil {
				return placementHost(2048), nil
			}
			if !errors.Is(err, os.ErrNotExist) {
				return room.Host{}, err
			}
			return placementHost(4096), nil
		}})
		if err := os.WriteFile(filepath.Join(dir, "ready"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		_, err := m.Wake(ctx, "target", time.Now().Add(time.Hour))
		if !errors.Is(err, room.ErrFull) {
			t.Fatalf("child Wake = %v, want ErrFull after parent's admission", err)
		}
		return
	}
	m, e := sharedStateManager(t)
	m.readRoom = func() (room.Host, error) { return placementHost(4096), nil }
	m.e = &placementEngine{Engine: e, create: func(ctx context.Context, o engine.CreateOptions) (engine.Machine, error) {
		assertPlacementLocked(t, m.dir)
		childCtx, stopChild := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		t.Cleanup(stopChild)
		cmd := exec.CommandContext(childCtx, os.Args[0], "-test.run=^TestHostAdmissionAcrossProcesses$", "-test.v")
		cmd.Env = append(os.Environ(), "HIB_ROOM_TEST_DIR="+m.dir)
		var output strings.Builder
		cmd.Stdout, cmd.Stderr = &output, &output
		if err := cmd.Start(); err != nil {
			return engine.Machine{}, err
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for {
			if _, err := os.Stat(filepath.Join(m.dir, "ready")); err == nil {
				break
			} else if !errors.Is(err, os.ErrNotExist) {
				return engine.Machine{}, err
			}
			select {
			case err := <-done:
				t.Fatalf("child exited before waiting: %v, %s", err, output.String())
			case <-ctx.Done():
				return engine.Machine{}, ctx.Err()
			case <-tick.C:
			}
		}
		select {
		case err := <-done:
			t.Fatalf("child spent the same memory before E2B returned: %v, %s", err, output.String())
		case <-time.After(50 * time.Millisecond):
		}
		if err := os.WriteFile(filepath.Join(m.dir, "spent"), nil, 0o600); err != nil {
			return engine.Machine{}, err
		}
		t.Cleanup(func() {
			if err := <-done; err != nil {
				t.Errorf("child admission: %v\n%s", err, output.String())
			}
		})
		return e.Create(ctx, o)
	}}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	if _, err := m.CreateTask(ctx, placementRun("t-aaaaaaaa", "medium"), time.Hour); err != nil {
		t.Fatal(err)
	}
}
