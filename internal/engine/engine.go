// Package engine is Hibernaut's seam over the microVM engine (E2B today, spec D4).
//
// Everything above this package talks to machines through Engine. The contract every
// implementation must meet is the test suite in package enginetest, which runs against the
// in-memory fake and, end to end, against real E2B.
package engine

import (
	"context"
	"errors"
	"time"
)

// State is a machine's lifecycle state.
type State string

const (
	// Running machines live in the host's RAM.
	Running State = "running"
	// Paused machines are snapshots on disk. They survive host restarts.
	Paused State = "paused"
)

// Machine is one microVM as the engine reports it.
type Machine struct {
	ID string
	// Template is the ID of the template the machine started from.
	Template  string
	State     State
	StartedAt time.Time
	// EndAt is when a running machine pauses itself unless its timeout is extended.
	EndAt    time.Time
	Metadata map[string]string
}

// CreateOptions describes a new machine.
type CreateOptions struct {
	// Template is a template ID or alias, e.g. "base".
	Template string
	// Timeout is how long the machine runs before it pauses itself. Zero means the engine's
	// default.
	Timeout  time.Duration
	Metadata map[string]string
	Env      map[string]string
}

// ListOptions filters List. The zero value lists every machine.
type ListOptions struct {
	// States keeps machines in any of these states. Empty means every state.
	States []State
	// Metadata keeps machines whose metadata contains every one of these pairs.
	Metadata map[string]string
}

// Metrics is one resource sample of a running machine.
type Metrics struct {
	At         time.Time
	CPUCount   int
	CPUUsedPct float64
	MemUsed    int64
	MemTotal   int64
	DiskUsed   int64
	DiskTotal  int64
}

// ExecRequest is a command to run inside a machine.
type ExecRequest struct {
	// Cmd is resolved through PATH, as a shell would.
	Cmd  string
	Args []string
	Env  map[string]string
	// Dir is the working directory. Empty means the user's home directory.
	Dir string
	// Stdin, if set, is written to the command's standard input.
	Stdin []byte
}

// ExecResult is the outcome of a command that ran to completion. A non-zero exit code is a
// result, not an error.
type ExecResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
}

// Engine runs machines.
//
// Methods that take a machine ID return an error wrapping ErrNotFound when no such machine
// exists.
type Engine interface {
	// Create starts a new running machine.
	Create(ctx context.Context, opts CreateOptions) (Machine, error)
	Get(ctx context.Context, id string) (Machine, error)
	List(ctx context.Context, opts ListOptions) ([]Machine, error)
	// Pause snapshots a running machine, memory included, and frees its RAM. It returns once
	// the machine is paused. Pausing a machine that isn't running returns ErrNotRunning.
	Pause(ctx context.Context, id string) error
	// Resume starts a paused machine from its snapshot, or extends a running machine's timeout.
	// It never shortens a running machine's timeout.
	Resume(ctx context.Context, id string, timeout time.Duration) (Machine, error)
	// Kill stops a machine for good and deletes it, whether running or paused.
	Kill(ctx context.Context, id string) error
	// SetTimeout makes a running machine pause itself timeout from now. It can shorten the
	// timeout. It returns ErrNotRunning for a paused machine.
	SetTimeout(ctx context.Context, id string, timeout time.Duration) error
	// Metrics returns the latest resource sample of a running machine, or ErrNoMetrics if the
	// engine has none yet.
	Metrics(ctx context.Context, id string) (Metrics, error)
	// Exec runs a command in a running machine as its default user and waits for it to exit.
	// It returns ErrNotRunning for a paused machine, and never wakes it.
	Exec(ctx context.Context, id string, req ExecRequest) (ExecResult, error)
}

var (
	ErrNotFound   = errors.New("machine not found")
	ErrNotRunning = errors.New("machine not running")
	ErrNoMetrics  = errors.New("no metrics yet")
)
