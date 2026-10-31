// Package taskguest is the protocol between hibd and a task machine: where a run's files live,
// what each unit is called, the argv of every root Exec hibd sends, and how their output is
// parsed. It does no I/O itself. taskrun and taskmachine send the argv through engine.Exec.
//
// Every argv here is safe to send again after a lost reply: launching a stage that exists is
// refused by systemd and by the stage's start marker, freezing, thawing and killing converge, and
// reads read. No argv carries the prompt or a secret, since engines may log Exec requests, and
// none reads stdin.
package taskguest

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/task"
)

// Runner is the runner script hibd uploads into every run's folder.
//
//go:embed runner.sh
var Runner []byte

const (
	// Root holds every run's folder in a task machine.
	Root = "/var/lib/hib-task"
	// Slice holds every stage unit, so one freeze or kill covers whatever stage is live.
	Slice = "hib-task.slice"
	// User runs the stages. Root runs the Execs and the units' ExecStopPost.
	User = "user"
	// MaxLogChunk bounds one log read, far below engine.MaxExecOutput even after base64.
	MaxLogChunk = 1 << 20
	// ExecTimeout bounds every Exec here except Stop, which waits out task.StopGrace.
	ExecTimeout = 10 * time.Second
)

// ExitTryAgain is what a stage's ExecStart exits with when its marker already exists.
const ExitTryAgain = 75

// ExitFrozen is reserved for runner launch refusing a frozen or transitioning slice.
const ExitFrozen = 76

// RunDir is run r's folder in the machine.
func RunDir(r task.RunID) string {
	return path.Join(Root, string(r.Task), "r"+strconv.Itoa(r.N))
}

// Workdir is where setup clones the repository. Follow-ups work in it too.
func Workdir(id task.ID) string { return path.Join("/home", User, string(id)) }

// Unit is the systemd unit that runs stage s of run r.
func Unit(r task.RunID, s task.Stage) string {
	return fmt.Sprintf("hib-%s-r%d-%s.service", r.Task, r.N, s)
}

// Uploads returns the files StepUpload writes: the runner, every input and the manifest the
// runner checks them against. blob opens an input's bytes on the host by hash.
func Uploads(r task.Run, blob func(task.Hash) (io.ReadCloser, error)) []engine.File {
	dir := RunDir(r.ID)
	var manifest strings.Builder
	fmt.Fprintf(&manifest, "%x  ../runner\n", sha256.Sum256(Runner))
	files := []engine.File{bytesFile(path.Join(dir, "runner"), Runner)}
	for _, in := range r.Spec.Inputs {
		fmt.Fprintf(&manifest, "%s  %s\n", in.Hash, in.Name)
		files = append(files, engine.File{Path: path.Join(dir, "in", in.Name), Size: in.Size,
			Open: func() (io.ReadCloser, error) { return blob(in.Hash) }})
	}
	return append(files, bytesFile(path.Join(dir, "in", ".manifest"), []byte(manifest.String())))
}

func bytesFile(p string, b []byte) engine.File {
	return engine.File{Path: p, Size: int64(len(b)),
		Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }}
}

// Verify checks the uploads by hash inside the machine. Exit 0 means every file arrived whole.
func Verify(r task.RunID) []string {
	dir := RunDir(r)
	return []string{"sh", path.Join(dir, "runner"), "verify", dir}
}

// Launch is the systemd-run argv that starts stage s of run r. runtimeMax is the time left to
// the deadline. It returns ExitFrozen for a frozen slice without thawing it. The prompt reaches
// the command stage as a file on stdin, never in argv.
func Launch(r task.Run, s task.Stage, runtimeMax time.Duration) []string {
	dir := RunDir(r.ID)
	runner := path.Join(dir, "runner")
	stdin := "null"
	if p, ok := r.Spec.Prompt(); ok && s == task.StageCommand {
		stdin = "file:" + path.Join(dir, "in", p.Name)
	}
	logFile := path.Join(dir, string(s)+".log")
	argv := []string{"systemd-run", "--expand-environment=no",
		"--unit=" + Unit(r.ID, s),
		"--slice=" + Slice,
		"--no-block", "--quiet",
		"--uid=" + User, "--gid=" + User,
		"--working-directory=" + workingDir(r, s),
		"--property=RuntimeMaxSec=" + strconv.Itoa(int(runtimeMax/time.Second)),
		"--property=TimeoutStopSec=" + strconv.Itoa(int(task.StopGrace/time.Second)),
		"--property=KillMode=control-group",
		"--property=CollectMode=inactive-or-failed",
		"--property=EnvironmentFile=-/etc/environment",
		"--property=StandardInput=" + stdin,
		"--property=StandardOutput=append:" + logFile,
		"--property=StandardError=append:" + logFile,
		"--property=ExecStopPost=+/bin/sh " + runner + " exit " + dir + " " + string(s),
		"--setenv=HIB_TASK=" + string(r.ID.Task),
		"--setenv=HIB_RUN=" + r.ID.String(),
		"--setenv=HIB_INPUTS=" + path.Join(dir, "in"),
		"--setenv=HIB_WORKDIR=" + Workdir(r.ID.Task),
		"--setenv=HIB_BRANCH=" + r.ID.Task.Branch(),
	}
	if r.Spec.Repo != (task.Repo{}) {
		argv = append(argv, "--setenv=HIB_REPO="+r.Spec.Repo.String(),
			"--setenv=HIB_BASE="+r.Spec.Base, "--setenv=HIB_PUBLISH="+string(r.Spec.Publish))
	}
	argv = append(argv, "--", "/bin/sh", runner, "start", dir, string(s))
	if s == task.StageCommand {
		argv = append(append(argv, "--"), r.Spec.Command...)
	}
	return append([]string{"sh", runner, "launch"}, argv...)
}

func workingDir(r task.Run, s task.Stage) string {
	if s == task.StageSetup || r.Spec.Repo == (task.Repo{}) {
		return path.Join("/home", User)
	}
	return Workdir(r.ID.Task)
}

// Look is the argv that reports everything a stage is judged by. ParseLook reads its output.
func Look(r task.RunID, s task.Stage) []string {
	dir := RunDir(r)
	return []string{"sh", path.Join(dir, "runner"), "look", dir, string(s), Unit(r, s)}
}

// Freeze freezes every stage unit through the cgroup v2 freezer. A frozen process doesn't run,
// so it can't act on the network when its machine resumes. ParseFreeze reads its answer.
func Freeze() []string { return control("freeze") }

// FreezeResult is Freeze's answer about the stage slice's cgroup. Only FreezeFrozen lets
// Owner record a frozen row.
type FreezeResult string

const (
	// FreezeFrozen means the kernel confirmed the stage slice is frozen.
	FreezeFrozen FreezeResult = "frozen"
	// FreezeEmpty means the stage slice has no cgroup, so nothing was frozen.
	FreezeEmpty FreezeResult = "empty"
)

// ParseFreeze reads Freeze's answer and rejects malformed output. Only FreezeFrozen permits
// Owner to record a frozen row. FreezeEmpty does not confirm a freeze.
func ParseFreeze(out []byte) (FreezeResult, error) {
	switch string(out) {
	case "freeze=frozen\n":
		return FreezeFrozen, nil
	case "freeze=empty\n":
		return FreezeEmpty, nil
	default:
		return "", fmt.Errorf("freeze: bad answer %q", out)
	}
}

// Thaw lets the stage units run again.
func Thaw() []string { return control("thaw") }

func control(cmd string, args ...string) []string {
	return append([]string{"sh", "-c", string(Runner), "runner", cmd}, args...)
}

// Stop asks stage s's unit to end: TERM, then KILL after task.StopGrace, as its unit says.
func Stop(r task.RunID, s task.Stage) []string {
	return []string{"systemctl", "stop", Unit(r, s)}
}

// Kill ends stage s's unit at once with KILL, frozen or not, for rescue and for a frozen stage
// past its deadline.
func Kill(r task.RunID, s task.Stage) []string {
	return control("kill", Unit(r, s))
}

// ReadLog reads at most max bytes of stage s's output from offset. The host's copy of the log
// is the only record of how far it has read, so a read repeated after a lost reply appends
// nothing twice.
func ReadLog(r task.RunID, s task.Stage, offset, max int64) []string {
	dir := RunDir(r)
	return []string{"sh", path.Join(dir, "runner"), "log", dir, string(s),
		strconv.FormatInt(offset, 10), strconv.FormatInt(min(max, MaxLogChunk), 10)}
}

// Verdict is how a stage stands.
type Verdict string

const (
	// NotStarted means no start marker: launching is safe.
	NotStarted Verdict = "not-started"
	// Running means the marker exists and the unit is live, frozen or not.
	Running Verdict = "running"
	// Exited means the exit record exists.
	Exited Verdict = "exited"
	// Vanished means the marker exists, the exit record doesn't and the unit is gone, as after
	// a guest reboot. The stage never starts again.
	Vanished Verdict = "vanished"
)

// Seen is what Look reported.
type Seen struct {
	Marker bool
	Exit   *task.ExitRecord
	// Load, Active and Sub are the unit's systemd properties.
	Load, Active, Sub string
	// CgroupFrozen is the unit's cgroup.events frozen value.
	CgroupFrozen bool
	// LogSize is the stage log's size in the machine.
	LogSize int64
}

// Verdict judges the stage from what Look saw. A unit that is live without a marker is starting,
// so it counts as running and is never launched again.
func (s Seen) Verdict() Verdict {
	live := s.Load == "loaded" && s.Active != "" && s.Active != "inactive" && s.Active != "failed"
	switch {
	case s.Exit != nil:
		return Exited
	case live:
		return Running
	case !s.Marker:
		return NotStarted
	}
	return Vanished
}

// Frozen reports whether the kernel has frozen the unit, itself or through the slice.
func (s Seen) Frozen() bool { return s.CgroupFrozen }

// ParseLook parses Look's output.
func ParseLook(out []byte) (Seen, error) {
	var s Seen
	var exit task.ExitRecord
	hasExit := false
	showFailed := false
	for line := range strings.Lines(string(out)) {
		k, v, ok := strings.Cut(strings.TrimRight(line, "\n"), "=")
		if !ok {
			return Seen{}, fmt.Errorf("look: bad line %q", line)
		}
		switch k {
		case "show":
			if v != "failed" {
				return Seen{}, fmt.Errorf("look: bad show result %q", v)
			}
			showFailed = true
		case "marker":
			s.Marker = v == "yes"
		case "exit.result":
			exit.Result, hasExit = v, true
		case "exit.code":
			exit.Code = v
		case "exit.status":
			if v != "" {
				n, err := strconv.Atoi(v)
				if err != nil {
					// systemd reports a signal's name as the status of a killed stage.
					exit.Code = cmpOr(exit.Code, "killed")
					n = -1
				}
				exit.Status = n
			}
		case "LoadState":
			s.Load = v
		case "ActiveState":
			s.Active = v
		case "SubState":
			s.Sub = v
		case "frozen":
			if v != "0" && v != "1" {
				return Seen{}, fmt.Errorf("look: bad frozen value %q", v)
			}
			s.CgroupFrozen = v == "1"
		case "log":
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return Seen{}, fmt.Errorf("look: bad log size %q", v)
			}
			s.LogSize = n
		}
	}
	if hasExit {
		s.Exit = &exit
	} else if showFailed {
		return Seen{}, errors.New("look: systemctl show failed without an exit record")
	}
	return s, nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// ParseLog parses ReadLog's output into the log's size in the machine and the bytes read.
func ParseLog(out []byte) (size int64, data []byte, err error) {
	head, rest, ok := strings.Cut(string(out), "\n")
	v, found := strings.CutPrefix(head, "size=")
	if !ok || !found {
		return 0, nil, errors.New("log: no size line")
	}
	size, err = strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, nil, fmt.Errorf("log: bad size %q", v)
	}
	data, err = base64.StdEncoding.DecodeString(strings.TrimSpace(rest))
	if err != nil {
		return 0, nil, fmt.Errorf("log: %w", err)
	}
	return size, data, nil
}

// PublicationOf is the argv that prints what finish published, as key=value lines.
func PublicationOf(r task.RunID) []string {
	return []string{"cat", path.Join(RunDir(r), "publish")}
}

// ParsePublication parses PublicationOf's output.
func ParsePublication(out []byte) (task.Publication, error) {
	var p task.Publication
	for line := range strings.Lines(string(out)) {
		k, v, ok := strings.Cut(strings.TrimRight(line, "\n"), "=")
		if !ok {
			return task.Publication{}, fmt.Errorf("publish: bad line %q", line)
		}
		switch k {
		case "changes":
			p.Changes = v == "true"
		case "commit":
			p.Commit = v
		case "pushed":
			p.Pushed = v == "true"
		case "pr":
			p.PR = v
		case "author":
			p.Author = v
		}
	}
	return p, nil
}

// FormatPublication is how finish writes the publish file. guestsim writes it the same way.
func FormatPublication(p task.Publication) []byte {
	return fmt.Appendf(nil, "changes=%t\ncommit=%s\npushed=%t\npr=%s\nauthor=%s\n",
		p.Changes, p.Commit, p.Pushed, p.PR, p.Author)
}
