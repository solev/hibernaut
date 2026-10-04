// Package engine is Hibernaut's seam over the microVM engine (E2B today, spec D4).
//
// The contract every implementation must meet is the test suite in package enginetest.
package engine

import (
	"context"
	"errors"
	"io"
	"net"
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
	Template string
	State    State
	// StartedAt is when the machine last started: when it was created or last resumed.
	// Extending its timeout leaves it alone.
	StartedAt time.Time
	// EndAt is when a running machine pauses itself unless its timeout is extended.
	EndAt time.Time
	// MemoryMB is the machine's RAM in MiB, which it holds while it runs.
	MemoryMB int
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
//
// Engines may log requests in full, arguments and environment included: never put a secret in
// one.
type ExecRequest struct {
	// Cmd is resolved through PATH, as a shell would.
	Cmd  string
	Args []string
	Env  map[string]string
	// Dir is the working directory. Empty means the user's home directory.
	Dir string
	// User runs the command as this user. Empty means the machine's default user.
	User string
}

// TemplateSpec describes a template: the disk, and the processes already running, that every
// machine created from it starts with.
type TemplateSpec struct {
	// Name is how CreateOptions.Template refers to the template.
	Name string
	// FromImage is the container image the template starts from, e.g. "ubuntu:24.04".
	FromImage string
	// Steps run in order, each in a login shell.
	Steps []BuildStep
	// StartCmd, if set, runs as the default user after the steps; whatever it leaves running is
	// running in every machine. ReadyCmd must then exit 0 before the template is snapshotted.
	StartCmd string
	ReadyCmd string
	CPUs     int
	MemoryMB int
	// MinFreeDiskMB is the free disk space every machine starts with.
	MinFreeDiskMB int
	// Force runs every step again instead of reusing what earlier builds cached.
	Force bool
}

// BuildStep is one shell command of a template build.
type BuildStep struct {
	Cmd string
	// User runs Cmd as this user. Empty means the template's default user.
	User string
}

// TemplateInfo is a template machines can be created from, built or saved.
type TemplateInfo struct {
	ID string
	// Alias is the template's name, without a namespace or tag.
	Alias string
	// CPUs and MemoryMB are what each machine created from the template gets.
	CPUs     int
	MemoryMB int
	// Created is when the template's newest build finished.
	Created time.Time
}

// MaxExecOutput bounds what Exec keeps of each of a command's stdout and stderr. A command that
// writes more fails Exec with an error, and may be left running.
const MaxExecOutput = 8 << 20

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
	// Create starts a new running machine. It returns an error wrapping ErrNoTemplate if there's
	// no such template.
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
	// Exec runs a command in a running machine and waits for it to exit. It returns
	// ErrNotRunning for a paused machine, and never wakes it. It returns an error wrapping
	// ErrConnectionLost if the connection broke before the command's exit was reported.
	Exec(ctx context.Context, id string, req ExecRequest) (ExecResult, error)
	// DialWebSocket connects to the WebSocket server listening on port inside the machine and
	// returns the connection as a byte stream carried in binary messages. It resumes a paused
	// machine, and makes sure a running one runs for at least timeout more. ctx bounds only the
	// dial: the connection lasts until it's closed.
	DialWebSocket(ctx context.Context, id string, port int, timeout time.Duration) (net.Conn, error)
	// BuildTemplate builds spec, or rebuilds the template of that name, copying build logs to
	// logs as they arrive. It returns the template's ID once machines can be created from it.
	BuildTemplate(ctx context.Context, spec TemplateSpec, logs io.Writer) (string, error)
	// SaveTemplate saves running machine id, memory included, as template alias, which machines
	// can then be created from with only the metadata their own CreateOptions give them. The
	// machine keeps running. Saving to an alias that exists adds a build to that template. It
	// returns an error wrapping ErrNotRunning when the machine is paused, or busy pausing, being
	// killed or being saved, and one wrapping ErrNotFound when it's missing. It fails with either
	// only when it created nothing. An error wrapping ErrCheckpoint means the engine stopped the
	// machine while saving: it's paused then, at its last snapshot, or lost if it had none. Any
	// other failure may leave alias behind with no build, unusable, and hidden from Template the
	// way a missed lookup hides it. Saving into a hidden alias fails with a plain error, and the
	// machine keeps running.
	SaveTemplate(ctx context.Context, id, alias string) (TemplateInfo, error)
	// Template returns the template called alias, built or saved, or an error wrapping
	// ErrNoTemplate. It also finds an alias a failed save left behind. The engine remembers a
	// lookup that found nothing, Create's and SaveTemplate's included. So Template can report
	// ErrNoTemplate for an alias that exists, for about a minute after a lookup of it missed, and
	// up to 5 minutes. A successful save or build of the alias, or deleting its template, ends
	// that.
	Template(ctx context.Context, alias string) (TemplateInfo, error)
	// SavedTemplates returns the templates SaveTemplate made, sorted by alias. Aliases a failed
	// save left behind may be missing.
	SavedTemplates(ctx context.Context) ([]TemplateInfo, error)
	// DeleteTemplate deletes template id. It returns an error wrapping ErrTemplateInUse while a
	// machine, running or paused, was created from it, and one wrapping ErrNoTemplate if there's
	// no such template.
	DeleteTemplate(ctx context.Context, id string) error
}

var (
	ErrNotFound      = errors.New("machine not found")
	ErrNotRunning    = errors.New("machine not running")
	ErrNoMetrics     = errors.New("no metrics yet")
	ErrNoTemplate    = errors.New("template not found")
	ErrCheckpoint    = errors.New("checkpoint failed")
	ErrTemplateInUse = errors.New("template in use")
	// ErrConnectionLost means Exec's connection broke before the command's exit was reported.
	// The command may not have run, may have run in whole or in part, and may still be running.
	// Retry only commands that are safe to run twice, even at the same time.
	ErrConnectionLost = errors.New("connection lost before the command finished")
)
