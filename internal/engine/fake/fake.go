// Package fake is an in-memory engine.Engine for tests.
//
// Timeouts are recorded but never fire: machines pause only when told to. Like a real engine's
// requests, every method fails once its context is done.
package fake

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/solev/hibernaut/internal/engine"
)

// DefaultTimeout applies when CreateOptions.Timeout or a Resume timeout is zero, as in E2B.
const DefaultTimeout = 300 * time.Second

// DefaultMemoryMB is the RAM of the machines Create makes: E2B's default.
const DefaultMemoryMB = 512

// ExecFunc runs a command for Exec. It's called only for running machines. home is the
// machine's home directory, or "" without WithLocalExec.
type ExecFunc func(ctx context.Context, m engine.Machine, home string, req engine.ExecRequest) (engine.ExecResult, error)

// PauseHook runs at the start of every Pause of a running machine, before the machine is
// paused. If it returns an error, Pause fails with it and the machine keeps running.
type PauseHook func(ctx context.Context, id string) error

// DialFunc connects DialWebSocket to port in m, which is running by then.
type DialFunc func(ctx context.Context, m engine.Machine, port int) (net.Conn, error)

// Option configures an Engine.
type Option func(*Engine)

// WithClock replaces time.Now.
func WithClock(now func() time.Time) Option {
	return func(e *Engine) { e.now = now }
}

// WithExec makes Exec call f.
func WithExec(f ExecFunc) Option {
	return func(e *Engine) { e.exec = f }
}

// WithLocalExec makes Exec run commands on this computer, each machine in its own home
// directory under root. The directory is removed when the machine is killed. Only use it with
// trusted test commands: nothing is isolated.
func WithLocalExec(root string) Option {
	return func(e *Engine) {
		e.homeRoot = root
		e.exec = localExec
	}
}

// WithPauseHook installs h.
func WithPauseHook(h PauseHook) Option {
	return func(e *Engine) { e.pauseHook = h }
}

// WithTemplates makes Create accept only these templates, and those BuildTemplate builds.
// Without it, Create accepts any.
func WithTemplates(names ...string) Option {
	return func(e *Engine) {
		e.templates = map[string]bool{}
		for _, n := range names {
			e.templates[n] = true
		}
	}
}

// WithDialer makes DialWebSocket call f.
func WithDialer(f DialFunc) Option {
	return func(e *Engine) { e.dial = f }
}

// Engine is the fake. The zero value isn't usable; call New.
type Engine struct {
	now       func() time.Time
	exec      ExecFunc
	pauseHook PauseHook
	dial      DialFunc
	homeRoot  string

	mu        sync.Mutex
	templates map[string]bool
	machines  map[string]*engine.Machine
	homes     map[string]string
	built     []engine.TemplateSpec
}

var _ engine.Engine = (*Engine)(nil)

// New returns an empty fake engine.
func New(opts ...Option) *Engine {
	e := &Engine{
		now:      time.Now,
		machines: map[string]*engine.Machine{},
		homes:    map[string]string{},
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

// Add inserts a machine as is, for tests that need a given starting state.
func (e *Engine) Add(m engine.Machine) {
	e.mu.Lock()
	defer e.mu.Unlock()
	m.Metadata = maps.Clone(m.Metadata)
	e.machines[m.ID] = &m
}

func (e *Engine) Create(ctx context.Context, opts engine.CreateOptions) (engine.Machine, error) {
	if err := ctx.Err(); err != nil {
		return engine.Machine{}, err
	}
	if opts.Template == "" {
		return engine.Machine{}, errors.New("fake: template is required")
	}
	e.mu.Lock()
	known := e.templates == nil || e.templates[opts.Template]
	e.mu.Unlock()
	if !known {
		return engine.Machine{}, fmt.Errorf("fake: create from %s: %w", opts.Template, engine.ErrNoTemplate)
	}
	id := NewID()
	var home string
	if e.homeRoot != "" {
		dir := filepath.Join(e.homeRoot, id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return engine.Machine{}, err
		}
		// Resolve symlinks (macOS /var → /private/var) so the path matches what `pwd` prints.
		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return engine.Machine{}, err
		}
		home = resolved
	}

	now := e.now()
	m := &engine.Machine{
		ID:        id,
		Template:  opts.Template,
		State:     engine.Running,
		StartedAt: now,
		EndAt:     now.Add(orDefault(opts.Timeout)),
		MemoryMB:  DefaultMemoryMB,
		Metadata:  maps.Clone(opts.Metadata),
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.machines[id] = m
	if home != "" {
		e.homes[id] = home
	}
	return clone(m), nil
}

func (e *Engine) Get(ctx context.Context, id string) (engine.Machine, error) {
	if err := ctx.Err(); err != nil {
		return engine.Machine{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	m, err := e.lookup(id)
	if err != nil {
		return engine.Machine{}, err
	}
	return clone(m), nil
}

func (e *Engine) List(ctx context.Context, opts engine.ListOptions) ([]engine.Machine, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []engine.Machine
	for _, m := range e.machines {
		if len(opts.States) > 0 && !slices.Contains(opts.States, m.State) {
			continue
		}
		if !hasAll(m.Metadata, opts.Metadata) {
			continue
		}
		out = append(out, clone(m))
	}
	// Newest first, like E2B.
	slices.SortFunc(out, func(a, b engine.Machine) int {
		if c := b.StartedAt.Compare(a.StartedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out, nil
}

func (e *Engine) Pause(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, _, err := e.running(id); err != nil {
		return err
	}
	if e.pauseHook != nil {
		if err := e.pauseHook(ctx, id); err != nil {
			return err
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	m, err := e.lookup(id)
	if err != nil {
		return err
	}
	if m.State != engine.Running {
		return fmt.Errorf("pause %s: %w", id, engine.ErrNotRunning)
	}
	m.State = engine.Paused
	return nil
}

func (e *Engine) Resume(ctx context.Context, id string, timeout time.Duration) (engine.Machine, error) {
	if err := ctx.Err(); err != nil {
		return engine.Machine{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	m, err := e.lookup(id)
	if err != nil {
		return engine.Machine{}, err
	}
	e.resume(m, timeout)
	return clone(m), nil
}

func (e *Engine) resume(m *engine.Machine, timeout time.Duration) {
	now := e.now()
	endAt := now.Add(orDefault(timeout))
	if m.State == engine.Paused {
		m.State = engine.Running
		m.StartedAt = now
		m.EndAt = endAt
	} else if endAt.After(m.EndAt) {
		m.EndAt = endAt
	}
}

func (e *Engine) Kill(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, err := e.lookup(id); err != nil {
		return err
	}
	delete(e.machines, id)
	if home, ok := e.homes[id]; ok {
		delete(e.homes, id)
		return os.RemoveAll(home)
	}
	return nil
}

func (e *Engine) SetTimeout(ctx context.Context, id string, timeout time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	m, err := e.lookup(id)
	if err != nil {
		return err
	}
	if m.State != engine.Running {
		return fmt.Errorf("set timeout %s: %w", id, engine.ErrNotRunning)
	}
	m.EndAt = e.now().Add(timeout)
	return nil
}

func (e *Engine) Metrics(ctx context.Context, id string) (engine.Metrics, error) {
	if err := ctx.Err(); err != nil {
		return engine.Metrics{}, err
	}
	if _, _, err := e.running(id); err != nil {
		return engine.Metrics{}, err
	}
	return engine.Metrics{
		At:        e.now(),
		CPUCount:  2,
		MemTotal:  512 << 20,
		MemUsed:   128 << 20,
		DiskTotal: 1 << 30,
		DiskUsed:  256 << 20,
	}, nil
}

func (e *Engine) Exec(ctx context.Context, id string, req engine.ExecRequest) (engine.ExecResult, error) {
	if err := ctx.Err(); err != nil {
		return engine.ExecResult{}, err
	}
	m, home, err := e.running(id)
	if err != nil {
		return engine.ExecResult{}, err
	}
	if e.exec == nil {
		return engine.ExecResult{}, errors.New("fake: exec not configured (use WithExec or WithLocalExec)")
	}
	return e.exec(ctx, m, home, req)
}

func (e *Engine) DialWebSocket(ctx context.Context, id string, port int, timeout time.Duration) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	e.mu.Lock()
	m, err := e.lookup(id)
	if err != nil {
		e.mu.Unlock()
		return nil, err
	}
	e.resume(m, timeout)
	c := clone(m)
	e.mu.Unlock()
	if e.dial == nil {
		return nil, errors.New("fake: dial not configured (use WithDialer)")
	}
	return e.dial(ctx, c, port)
}

// BuildTemplate records spec and returns "tmpl-" and its name.
func (e *Engine) BuildTemplate(ctx context.Context, spec engine.TemplateSpec, logs io.Writer) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if spec.Name == "" || spec.FromImage == "" {
		return "", errors.New("fake: Name and FromImage are required")
	}
	id := "tmpl-" + spec.Name
	e.mu.Lock()
	e.built = append(e.built, spec)
	if e.templates != nil {
		e.templates[spec.Name] = true
		e.templates[id] = true
	}
	e.mu.Unlock()
	if logs != nil {
		fmt.Fprintf(logs, "fake: built %s from %s in %d steps\n", spec.Name, spec.FromImage, len(spec.Steps))
	}
	return id, nil
}

// Built returns the specs BuildTemplate was called with, oldest first.
func (e *Engine) Built() []engine.TemplateSpec {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.built)
}

func localExec(ctx context.Context, _ engine.Machine, home string, req engine.ExecRequest) (engine.ExecResult, error) {
	if req.User != "" {
		return engine.ExecResult{}, fmt.Errorf("fake: local exec can't run as %q", req.User)
	}
	cmd := exec.CommandContext(ctx, req.Cmd, req.Args...)
	cmd.Dir = home
	if req.Dir != "" {
		cmd.Dir = req.Dir
	}
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "USER=user", "LOGNAME=user", "PWD=" + cmd.Dir}
	for k, v := range req.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stdout, stderr capped
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if stdout.over || stderr.over {
		return engine.ExecResult{}, fmt.Errorf("fake: %s wrote more than %d bytes", req.Cmd, engine.MaxExecOutput)
	}
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return engine.ExecResult{}, err
	}
	return engine.ExecResult{ExitCode: cmd.ProcessState.ExitCode(), Stdout: stdout.buf.Bytes(), Stderr: stderr.buf.Bytes()}, nil
}

// The buffer isn't embedded: its ReadFrom would get around Write.
type capped struct {
	buf  bytes.Buffer
	over bool
}

func (c *capped) Write(p []byte) (int, error) {
	if c.buf.Len()+len(p) > engine.MaxExecOutput {
		c.over = true
		return 0, errors.New("too much output")
	}
	return c.buf.Write(p)
}

func (e *Engine) running(id string) (engine.Machine, string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	m, err := e.lookup(id)
	if err != nil {
		return engine.Machine{}, "", err
	}
	if m.State != engine.Running {
		return engine.Machine{}, "", fmt.Errorf("%s: %w", id, engine.ErrNotRunning)
	}
	return clone(m), e.homes[id], nil
}

func (e *Engine) lookup(id string) (*engine.Machine, error) {
	m, ok := e.machines[id]
	if !ok {
		return nil, fmt.Errorf("%s: %w", id, engine.ErrNotFound)
	}
	return m, nil
}

// NewID returns a random ID in E2B's format: 20 characters of [a-z0-9].
func NewID() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 20)
	rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

func clone(m *engine.Machine) engine.Machine {
	c := *m
	c.Metadata = maps.Clone(m.Metadata)
	return c
}

func hasAll(have, want map[string]string) bool {
	for k, v := range want {
		if got, ok := have[k]; !ok || got != v {
			return false
		}
	}
	return true
}

func orDefault(d time.Duration) time.Duration {
	if d <= 0 {
		return DefaultTimeout
	}
	return d
}
