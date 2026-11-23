// Package task is the task subsystem's domain: what a task and a run are, the ordered steps a
// run goes through, and every decision about them as a pure function of recorded facts.
//
// It does no I/O. taskstore records facts, taskrun acts on them, and everything a user sees,
// phase and outcome included, is derived here from what they recorded. Nothing a user sees is
// stored twice, so nothing can disagree with itself.
package task

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Limits tasks and their runs live within. They are constants, not settings, except where
// Admission says otherwise.
const (
	// DefaultTimeout is a run's deadline when --timeout isn't given. Any length is allowed, and
	// time paused counts.
	DefaultTimeout = 24 * time.Hour
	// MaxQueued is how long a run may wait for room before it ends as expired.
	MaxQueued = 24 * time.Hour
	// MaxQueuedRuns is how many runs may wait at once across all tasks.
	MaxQueuedRuns = 32
	// MaxLogBytes is how much of a run's output hibd keeps. The rest is dropped and says so.
	MaxLogBytes = 50 << 20
	// MaxInputBytes is how much a task's prompts and files may add up to over all its runs.
	MaxInputBytes = 100 << 20
	// KeepFor is how long a finished task's machine is kept paused before retention removes it.
	KeepFor = 7 * 24 * time.Hour
	// StopGrace is how long a stage gets between TERM and KILL.
	StopGrace = 15 * time.Second
	// StopLimit bounds attempts to prove a stop, before one final bounded KILL.
	StopLimit = time.Minute
	// CollectLimit bounds all guest reads in collect, including publication.
	CollectLimit = time.Minute
	// TimeoutMargin is how far past the deadline the machine's E2B timeout is set, so hibd's
	// stop always comes before E2B's pause.
	TimeoutMargin = 5 * time.Minute
)

// Label is the machine label that marks a task machine. Its value is the task ID, which is the
// machine's name too. Task machines are never copied: fork and image save refuse them.
const Label = "hib.task"

// ID names a task and its machine: "t-" and 8 lowercase base32 characters. It passes
// hibapi.CheckName, so ssh ID.hib works, and hibd new refuses names of this shape so a person
// can never take one.
type ID string

var idRE = regexp.MustCompile(`^t-[a-z2-7]{8}$`)

var idEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewID reads 5 random bytes from r and returns them as an ID.
func NewID(r io.Reader) (ID, error) {
	var b [5]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return "", fmt.Errorf("making a task ID: %w", err)
	}
	return ID("t-" + strings.ToLower(idEncoding.EncodeToString(b[:]))), nil
}

// ParseID returns s as an ID, or an error if it isn't one.
func ParseID(s string) (ID, error) {
	if !idRE.MatchString(s) {
		return "", fmt.Errorf("bad task %q: want t- and 8 of a-z and 2-7", s)
	}
	return ID(s), nil
}

// Branch is the branch the task pushes to.
func (id ID) Branch() string { return "hib/" + string(id) }

// RunID names one run of a task. N counts from 1. The first run creates the task's machine and
// every later run, a follow-up, reuses it.
type RunID struct {
	Task ID
	N    int
}

// String returns the run as TASK/N.
func (r RunID) String() string { return string(r.Task) + "/" + strconv.Itoa(r.N) }

// FollowUp reports whether the run reuses a machine an earlier run made.
func (r RunID) FollowUp() bool { return r.N > 1 }

// ParseRunID parses TASK/N.
func ParseRunID(s string) (RunID, error) {
	t, n, ok := strings.Cut(s, "/")
	id, err := ParseID(t)
	if !ok || err != nil {
		return RunID{}, fmt.Errorf("bad run %q: want TASK/N", s)
	}
	k, err := strconv.Atoi(n)
	if err != nil || k < 1 {
		return RunID{}, fmt.Errorf("bad run %q: want TASK/N with N from 1", s)
	}
	return RunID{Task: id, N: k}, nil
}

// Publish is what the finish stage does with the branch.
type Publish string

const (
	// PublishDraftPR commits, pushes the branch and opens a draft pull request.
	PublishDraftPR Publish = "draft-pr"
	// PublishNone commits and pushes nothing. The changes stay in the kept machine.
	PublishNone Publish = "none"
)

// Hash is the SHA-256 of an input, which names its blob on the host.
type Hash [sha256.Size]byte

// String returns the hash in hex.
func (h Hash) String() string { return hex.EncodeToString(h[:]) }

// CheckInputName returns nil if name may name an input: one path element that isn't hidden,
// so it can't clash with the manifest or leave the input folder.
func CheckInputName(name string) error {
	if name == "" || name[0] == '.' || strings.ContainsAny(name, "/\\\x00\n") || len(name) > 255 {
		return fmt.Errorf("bad file name %q: want one path element that doesn't start with a dot",
			name)
	}
	return nil
}

// File is one input of a run: a file the user gave, or the prompt.
type File struct {
	// Name is where it lands in the machine, relative to the run's input folder. It is a single
	// path element, checked at the boundary.
	Name string
	Size int64
	Hash Hash
}

// PromptName is the File.Name of the prompt.
const PromptName = "prompt"

// Repo is a GitHub repository as owner/name. The zero Repo means the run clones nothing.
type Repo struct{ Owner, Name string }

// String returns owner/name.
func (r Repo) String() string {
	if r == (Repo{}) {
		return ""
	}
	return r.Owner + "/" + r.Name
}

// Spec is what one run does, checked at the boundary. A Spec is immutable once submitted: the
// run's log refers to it and never copies it.
type Spec struct {
	Repo Repo
	// Base is the branch the task branches from. "" means the repository's default.
	Base string
	// Command is the argv of the command stage. A recipe is resolved into it at submission, and
	// Recipe keeps the name for display.
	Command []string
	Recipe  string
	// Inputs are the prompt, if any, and the files, in the order the request listed them.
	Inputs []File
	// Image and Size are the machine's. A follow-up keeps the first run's.
	Image   string
	Size    string
	Timeout time.Duration
	Publish Publish
}

// Prompt returns the prompt's File, if the run has one.
func (s Spec) Prompt() (File, bool) {
	for _, f := range s.Inputs {
		if f.Name == PromptName {
			return f, true
		}
	}
	return File{}, false
}

// Actor is who asked for something, recorded with it.
type Actor struct {
	// Owner is the account the task belongs to. It is the host's login user today.
	Owner string
	// Name is who acted: the login user, or a gate's caller later.
	Name string
	// Source is where the request came from: "cli" today.
	Source string
}

// Submission is a request to start a task, or a follow-up run of one, parsed at the boundary.
type Submission struct {
	// RequestID makes the submission safe to retry. The same ID with the same content returns
	// the first answer, and with other content fails with ErrConflict. Receipts are kept forever.
	RequestID string
	// Task is the task a follow-up runs on, or "" for a new task.
	Task  ID
	Spec  Spec
	Actor Actor
	// At is when hibd received it. It isn't part of the digest.
	At time.Time
}

// Digest is what makes two submissions with one RequestID the same: every field but the ID.
func (s Submission) Digest() Hash {
	h := sha256.New()
	fmt.Fprintf(h, "%q|%q|%q|%q|%q|%d|%q|%q|%q", s.Task, s.Spec.Repo, s.Spec.Base,
		s.Spec.Command, s.Spec.Recipe, s.Spec.Timeout, s.Spec.Image, s.Spec.Size, s.Spec.Publish)
	for _, f := range s.Spec.Inputs {
		fmt.Fprintf(h, "|%q:%d:%s", f.Name, f.Size, f.Hash)
	}
	fmt.Fprintf(h, "|%q|%q|%q", s.Actor.Owner, s.Actor.Name, s.Actor.Source)
	var out Hash
	copy(out[:], h.Sum(nil))
	return out
}

// Receipt is the answer to a submission, returned again for every retry of it.
type Receipt struct {
	RequestID string
	Run       RunID
	At        time.Time
}

// Errors a submission or request can fail with. hibd maps each to a hibapi error code.
var (
	// ErrConflict means a request ID was reused for different content.
	ErrConflict = errors.New("request ID already used for a different request")
	// ErrQueueFull means MaxQueuedRuns runs are waiting already.
	ErrQueueFull = errors.New("the task queue is full")
	// ErrInputFull means the task's inputs would exceed MaxInputBytes across its runs.
	ErrInputFull = errors.New("the task inputs exceed the size limit")
	// ErrNoTask means no task has that ID.
	ErrNoTask = errors.New("no such task")
	// ErrRunLive means the task has a run that hasn't ended. A follow-up waits for it, and once
	// the run holds its machine, only wake touches the machine.
	ErrRunLive = errors.New("the task has a run that hasn't ended")
)

// Recipe is a named command with its defaults, kept in recipes.json. M15 adds the store and
// hibd recipe. Submission resolves a recipe into Spec.Command and the Spec defaults, so a run
// never depends on a recipe that changes later.
type Recipe struct {
	Name    string
	Command []string
	Image   string
	Size    string
	Timeout time.Duration
	Publish Publish
}
