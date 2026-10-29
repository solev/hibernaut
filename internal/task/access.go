package task

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Access is what may touch a task machine. AccessOf derives it from the task's latest run, and
// nothing stores it. Every hibd path that wakes, pauses, removes, copies, sets up or renews a
// task machine asks Allows first, so the rules are one table.
type Access string

const (
	// AccessLive is the machine of a run that holds it: from the run's grant until it ends. It
	// may wake, for the daemon and for ssh, and nothing else touches it. The host may have paused
	// it with the freeze, and the daemon then resumes it as soon as it can.
	AccessLive Access = "live"
	// AccessHeld is a live stage's machine paused without the freeze, by E2B's timeout or a
	// failed freeze. It stays paused until hib task rescue or hib task rm. Cancel and the
	// deadline leave it held.
	AccessHeld Access = "held"
	// AccessOverdue is a frozen stage's paused machine past the run's deadline. Nothing wakes it
	// until the daemon records the stop, which it does at its next look.
	AccessOverdue Access = "overdue"
	// AccessKept is the machine of a run that ended, or of a follow-up still waiting for its
	// grant. It is the person's again, except that only hib task rm removes it and nothing
	// copies it.
	AccessKept Access = "kept"
)

// Accesses returns every Access, in the order the diagram and the table show them.
func Accesses() []Access { return []Access{AccessLive, AccessHeld, AccessOverdue, AccessKept} }

// Op is a way hibd touches a machine.
type Op string

const (
	// OpWake resumes or dials a machine: ssh, exec, a link, a repin and the daemon's own wakes.
	OpWake Op = "wake"
	// OpPause pauses a machine by hand: hib pause, and hibd leakscan of a running machine. The
	// pauses hibd makes itself, at a run's keep step and at a host shutdown, freeze the stage
	// and don't ask.
	OpPause Op = "pause"
	// OpRemove removes a machine by name: hib rm.
	OpRemove Op = "remove"
	// OpCopy copies a machine: hib fork and hib image save.
	OpCopy Op = "copy"
	// OpSetup writes the machine setup into it: the env block, the Codex login and the hosts
	// block.
	OpSetup Op = "setup"
	// OpRenew is busy's care of a machine: renewing its timeout while it is busy, and recording
	// its activity, which lets its links keep other machines awake.
	OpRenew Op = "renew"
)

// Ops returns every Op, in the order the table shows them.
func Ops() []Op { return []Op{OpWake, OpPause, OpRemove, OpCopy, OpSetup, OpRenew} }

var (
	// ErrTaskMachine means only the task removes its machine, so its records stay true.
	ErrTaskMachine = errors.New("only hib task rm TASK removes a task's machine")
	// ErrNoCopy means a task machine is never copied. A copy would carry the task's work and
	// secrets into a machine no task owns.
	ErrNoCopy = errors.New("a task's machine is never copied: hib task run TASK works in it again")
)

// AccessOf returns the access of run r's machine at now. r is the latest run of the machine's
// task, with r.Machine from E2B. A run holds its machine from its grant until it ends. While a
// stage is live and the machine is paused, it is held when unfrozen and not rescued, and overdue
// when frozen past the deadline with no stop recorded.
func AccessOf(r Run, now time.Time) Access {
	deadline, granted := r.Deadline()
	if r.Ended() || !granted {
		return AccessKept
	}
	_, stopped := r.Stop()
	if _, live := r.LiveStage(); live && r.Machine == MachinePaused {
		switch {
		case !r.Frozen() && !(stopped && r.Rescued()):
			return AccessHeld
		case r.Frozen() && !stopped && !now.Before(deadline):
			return AccessOverdue
		}
	}
	return AccessLive
}

// Allows returns nil if op may touch a machine with access a, and otherwise why not. A kept
// machine allows every op but remove and copy, and a live one allows only wake. A held or
// overdue machine allows nothing. An access or op Allows doesn't know is refused.
func (a Access) Allows(op Op) error {
	if !slices.Contains(Accesses(), a) || !slices.Contains(Ops(), op) {
		return fmt.Errorf("unknown access %q or op %q", a, op)
	}
	switch {
	case op == OpRemove:
		return ErrTaskMachine
	case op == OpCopy:
		return ErrNoCopy
	case a == AccessHeld:
		return ErrHeld
	case a == AccessOverdue:
		return ErrPastDeadline
	case a == AccessLive && op != OpWake:
		return ErrRunLive
	}
	return nil
}

// The two ends of the diagram, which no run's access is.
const (
	noMachine Access = "none"
	removed   Access = "removed"
)

type transition struct {
	from, to Access
	why      string
}

// transitions are the edges between accesses. The test proves each with a run before and after.
var transitions = []transition{
	{noMachine, AccessLive, "the first run gets its grant"},
	{AccessLive, AccessHeld, "E2B pauses it without the freeze"},
	{AccessLive, AccessOverdue, "the deadline passes while the host has it paused"},
	{AccessLive, AccessKept, "the run ends, or a follow-up finds no room"},
	{AccessHeld, AccessLive, "rescue, or rm ends the stage"},
	{AccessOverdue, AccessLive, "the daemon records the stop"},
	{AccessKept, AccessLive, "a follow-up gets its grant"},
	{AccessKept, removed, "hib task rm, or 7 days on"},
}

// AccessMermaid returns the access diagram, generated from transitions. testdata/access.mmd is
// its golden copy and SKETCH.md embeds it, and a test fails when either differs.
func AccessMermaid() string {
	var b strings.Builder
	b.WriteString("flowchart TD\n")
	fmt.Fprintf(&b, "    %s([\"no machine yet\"])\n", noMachine)
	for _, a := range Accesses() {
		fmt.Fprintf(&b, "    %s[\"%s\"]\n", a, a)
	}
	fmt.Fprintf(&b, "    %s([\"%s\"])\n", removed, removed)
	for _, t := range transitions {
		fmt.Fprintf(&b, "    %s -->|%s| %s\n", t.from, t.why, t.to)
	}
	return b.String()
}

// AccessTable returns Allows as a markdown table, a row per Access and a column per Op.
// testdata/access.md is its golden copy and SKETCH.md embeds it.
func AccessTable() string {
	var b strings.Builder
	b.WriteString("| access |")
	for _, op := range Ops() {
		fmt.Fprintf(&b, " %s |", op)
	}
	b.WriteString("\n|---|")
	for range Ops() {
		b.WriteString("---|")
	}
	b.WriteString("\n")
	for _, a := range Accesses() {
		fmt.Fprintf(&b, "| %s |", a)
		for _, op := range Ops() {
			cell := "yes"
			if a.Allows(op) != nil {
				cell = "no"
			}
			fmt.Fprintf(&b, " %s |", cell)
		}
		b.WriteString("\n")
	}
	return b.String()
}
