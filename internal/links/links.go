// Package links is the contract for machine links: the state a published port lives in, and the
// seam between the listener that carries link bytes and the admitter that decides whether they
// may move.
//
// It sits below both internal/linkproxy and internal/linkadmit so neither imports the other, and
// it holds every constant, error and type the two share. docs/milestones/m12-machine-links.md
// describes what these mean and why; this package is what they are. Changing anything here is
// changing the contract, so it changes that document in the same commit.
package links

import (
	"context"
	"net/netip"
	"time"
)

// Run identifies one run of one machine: the suffix of the E2B cgroup sbx-<id>-<run> that the
// run lives in. A pause, a resume, a save and a crash all end a run, and the next run of the
// same machine has a different suffix, so two sockets opened across a pause never share one.
//
// Machine IDs are not enough on their own, and neither is engine.Machine.StartedAt: pausing
// leaves StartedAt where it was, so a machine that paused and resumed between two reads looks
// unchanged.
type Run string

// Admission is permission for one link, between one source run and one target run, to use one
// published port. It is granted by an Admitter and held by the listener for as long as the link
// is open.
//
// Everything in it is fixed at admission except the lease, which Renew re-derives. The listener
// re-checks SourceRun, TargetRun, PortGen and both gates against the world before it moves the
// first byte, and abandons the link if any of them changed while it was waking or dialling.
type Admission struct {
	// Source and Target are sandbox IDs, never names: names are reused, IDs are not.
	Source string
	Target string
	// SourceRun and TargetRun are the runs the admission was granted against. A link may only
	// carry bytes while both are still the current run of their machine.
	SourceRun Run
	TargetRun Run
	// SourceGate and TargetGate are the two runs' gates in the kernel guard. Each is a slot's
	// address in E2B's host range, 10.11.0.0/16, with the token of the run at it. Guest egress
	// is source-NATed to that address inside the sandbox's namespace, and it is also the address
	// the host dials, so one address identifies a slot on both legs. The listener checked
	// SourceGate against the accepted socket's mark, dials TargetGate.Addr with
	// Mark(TargetGate.Token) and aborts the link when the Keeper retires either.
	SourceGate Gate
	TargetGate Gate
	// Port is the published port on the target.
	Port int
	// PortGen is the generation of that one port when the admission was granted. Publishing or
	// unpublishing a different port on the same machine does not change it, so one port's
	// revocation leaves the other ports' links alone.
	PortGen uint64
	// LeaseUntil is the lease as it stood when the admission was granted: the source's last
	// activity that was not a link, plus LinkGrace. It is absolute rather than a duration
	// because E2B applies a relative timeout from the moment a resume finishes, so a slow
	// wake would otherwise push the target's end past the lease that justified it.
	//
	// It is the initial value and nothing more. Renew re-derives the lease from the clock
	// every time, so a source that keeps working keeps its links; a link that has stopped
	// being renewed is not cut, it simply stops pushing the target's idle timeout back, and
	// the target pauses on its own timeout. Only a wake is held to this value, because a wake
	// that lands after its lease has passed has nothing left to justify it.
	LeaseUntil time.Time
	// Woke is true when this admission is the one that resumed the target.
	Woke bool
	// Ctx is cancelled when the link must stop: the port was unpublished, either run ended or
	// either machine is being paused or removed. The lease never cancels it (see Renew). The
	// listener aborts on it, and separately on either gate being retired.
	Ctx context.Context
}

// AdmitRequest is one connection asking to become a link.
type AdmitRequest struct {
	// Source is the calling machine's sandbox ID, from the veth the connection arrived on.
	Source string
	// SourceRun and SourceGate are the run the connection came from and that run's gate. The
	// listener has already checked that the gate's token is the one on the accepted socket and
	// its address the connection's source, so the connection belongs to this run and no other.
	SourceRun  Run
	SourceGate Gate
	// Target and Port are what the caller asked for, resolved from the virtual address.
	Target string
	Port   int
}

// Admitter decides whether a connection may become a link, and keeps the decision true for as
// long as the link is open. internal/linkadmit implements it; internal/linkproxy calls it.
//
// Every connection is admitted, whether or not the target is already running. A running target
// that skipped admission would also skip the caps, the revocation check and a pause that
// internal/machines is holding the machine open for.
type Admitter interface {
	// Admit reserves room and caps, wakes the target if it is paused, and returns an
	// Admission. It reserves before it wakes and rolls the reservation back if the wake
	// fails, so concurrent wakes of different targets cannot spend the same reserve twice.
	// Concurrent calls for one target coalesce into one wake.
	//
	// It blocks for at most AdmitTimeout, plus WakeTimeout when it wakes.
	Admit(ctx context.Context, req AdmitRequest) (Admission, error)
	// Renew reports that payload bytes moved on a link, so the target's idle timeout is pushed
	// back. It is called often and must be cheap.
	//
	// It re-derives the lease from the clock each time rather than reading a.LeaseUntil, so a
	// source that is still being worked on keeps renewing past the lease it was admitted
	// under. A source whose own non-link activity has gone stale stops renewing, and the
	// target then pauses on its own idle timeout. Renew never cuts a link, and link bytes
	// never feed the clock.
	Renew(a Admission, payload int64)
	// Release gives back the room and the caps. It is idempotent, and the listener calls it in
	// a defer so a panic in the copy loop cannot leak a reservation.
	Release(a Admission)
}

// Closer is the listener's side of the close handshake. internal/linkproxy implements it;
// internal/linkadmit calls it when hibd pauses, removes or unpublishes something.
//
// Both methods are acknowledged only once every socket in scope is closed: not marked, not
// scheduled, closed. Hibernaut never pauses a machine past a listener that may still hold a
// socket to it, because E2B hands a paused machine's slot to another machine about ReturnDelay
// later, and a socket that outlived its run would then be pointed at a stranger.
//
// All three work whether or not the listener is serving. One that is not, before Serve or after
// it has returned, holds no sockets, so the aborts return 0 and nil and Stat returns empty counts.
// That is how the control socket answers while links are off.
type Closer interface {
	// AbortMachine aborts every link with id at either end and returns the number it aborted.
	AbortMachine(ctx context.Context, id string, reason string) (int, error)
	// AbortPorts aborts links to the listed published ports of id, and leaves links on that
	// machine's other ports open.
	AbortPorts(ctx context.Context, id string, ports []int, reason string) (int, error)
	// Stat reports the open links.
	Stat() Stats
}

// Open names one published port of one machine.
type Open struct {
	// Target is the sandbox ID of the machine that published the port.
	Target string
	Port   int
}

// Stats is what the listener knows about the links that are open now.
type Stats struct {
	// Ports counts live links per published port. hibapi.PublishedPort.Open is this number,
	// which is how hib port ls can say why a machine is awake while it is still happening,
	// rather than leaving it to be worked out from the log afterwards.
	Ports map[Open]int
	// Machines counts live links with that sandbox ID at either end. It is the cap
	// accounting, and it covers a machine that is only ever a source.
	Machines map[string]int
}

// Controller is Closer reached from another process, over the control socket at CtlSocket.
// hibd's port, pause, rm and ls commands run as separate invocations from the one serving
// links, so every cross-process cancellation goes through here.
type Controller interface {
	// Revoke aborts the links using the listed ports of id. hib port rm reports success only
	// after this returns, and it is the one caller that never downgrades a failure to a
	// warning.
	Revoke(ctx context.Context, id string, ports []int) (closed int, err error)
	// Close aborts every link with id at either end, before the caller pauses or removes it.
	Close(ctx context.Context, id string) (closed int, err error)
	// Stat reports the open links, for hib port ls and hib ls.
	Stat(ctx context.Context) (Stats, error)
}

// Runs says which run owns a machine now, and tells the listener when that run ends.
//
// internal/linkadmit implements it by watching the run's cgroup under E2B's cgroup root:
// cgroup.events reports populated 0 when the last process in the run exits, and the directory
// goes away when the sandbox is torn down. That is an event rather than a poll, which matters
// because E2B returns a slot to its pool ReturnDelay after a run ends. Tests implement it with
// a channel.
type Runs interface {
	// Current returns the run that owns id now, or ErrNotRunning if nothing does.
	Current(id string) (Run, error)
	// Watch returns a channel that is closed when run of machine id ends. Watching a run that
	// has already ended returns an already-closed channel, so there is no race between
	// admission and the first read.
	Watch(id string, run Run) (<-chan struct{}, error)
	// Live returns every machine that has a run now, with that run. It reads the cgroups at
	// the time of the call rather than a cache, because the Keeper extends gates on the
	// strength of it.
	Live() (map[string]Run, error)
}

// Gate is one gate in the kernel guard: a slot's host address together with the token of the
// run at that slot. Both halves are needed. A gate keyed on the address alone would still be
// open for whoever E2B put in the slot next, which is the hole tokens close.
type Gate struct {
	Addr  netip.Addr
	Token uint32
}

// Guard is the kernel's own fence around link traffic, and the only part of machine links that
// still holds when the proxy stops running. internal/linkguard implements it.
//
// It is a pair of nftables objects in a table of their own: a set of the gates that are open,
// and a map from a slot address to the mark of the run there now, which is how a connection's
// first packet gets a mark. Link traffic whose (address, mark) pair is not in the set is
// dropped in all four directions, checked against the packet's mark on the way out and the
// receiving socket's mark on the way in, so a proxy that is wedged, killed or paging cannot
// leak bytes to a slot whose run has ended. Elements carry GuardTTL, and an element that has
// timed out stops matching at lookup, before any garbage collection runs, so the fence closes
// itself.
//
// Only the Keeper opens and extends gates. Extend is a liveness claim, not bookkeeping: the
// Keeper re-checks that the run behind each gate is live and unfenced immediately before it.
type Guard interface {
	// Install replaces the whole table with this version's sets, chains and rules, in one
	// atomic operation, with both sets empty, and sets net.ipv4.tcp_fwmark_accept to 1 so an
	// accepted socket takes its SYN's mark. It is how the proxy starts, so a crash cannot leave
	// a gate open across a restart.
	Install() error
	// Installed reports whether the table, both sets and every chain are exactly as Install
	// leaves them and net.ipv4.tcp_fwmark_accept is still 1. The Keeper checks this every
	// GuardSelfCheck, because E2B reloads its own nftables rules and an operator can flush.
	Installed() (bool, error)
	// Remove deletes the table. Nothing else of ours is left anywhere, so this is a complete
	// uninstall. It leaves net.ipv4.tcp_fwmark_accept at 1, which changes nothing once no
	// packet carries a mark.
	Remove() error
	// Open creates g's element in the set and points g.Addr at g.Token in the map, both
	// create-only (NLM_F_EXCL) in one batch, so it fails rather than touch anything already
	// there: the caller drops the address first. It refuses a token at or below the highest it
	// has been asked to open, with ErrGateSpent, and records the token before it sends, so a
	// gate that has gone can never be opened again, not even by a retry.
	Open(g Gate) error
	// Extend refreshes the given gates with GuardTTL, in one atomic batch, each element
	// deleted and then added again. It never creates an element.
	//
	// Deleting and adding is not an optimisation to be skipped. Adding an element that is
	// already in the set does not reset its timeout, so an add-only refresh would silently
	// stop refreshing. Deleting one that has expired or been removed fails with ENOENT and
	// takes the batch with it, which is also deliberate: it means a refresh that was delayed
	// past a gate's expiry can never bring the gate back.
	Extend(gates []Gate) error
	Dropper
	// Has reports whether g is still open. Because nothing can recreate a gate that has gone,
	// a false here stays false.
	Has(g Gate) (bool, error)
}

// Dropper is the part of the Guard that the close fallbacks use: the admitter's when the
// listener does not acknowledge a close in time, and hibd's when the proxy does not answer.
type Dropper interface {
	// Drop removes every gate for addr whatever its token, and addr's map entry. An element can
	// expire between being read and being deleted, so Drop reads again until the kernel has
	// none of them, and returns only once that read is empty. hibd's pause fallback pauses a
	// machine on the strength of this, so a lie here is a leaked link. It needs CAP_NET_ADMIN:
	// a caller without it gets an error and must not go on as if it had dropped.
	//
	// A missing table is an error, because a wedged proxy whose table was flushed may still hold
	// sockets. The one exception is a TokenFile that does not exist: no token has been handed
	// out since boot, so no gate and no link socket can have existed, and Drop returns nil.
	// Without it, a host whose proxy never installed the guard could pause nothing.
	Drop(addr netip.Addr) error
}

// Tokens hands out the run tokens that gates are keyed on. A token is never reused within a
// boot, so a stale socket's mark can never match a later run.
type Tokens interface {
	// Next returns a token no run has held before, starting at 1, or ErrTokensSpent once the
	// counter has passed GuardMaxToken. The counter is persisted under RunDir so it stays
	// monotonic across a proxy restart, and a reboot restarts it because no run survives one.
	// Running out is final until that reboot. It writes the counter before it returns a
	// token, so TokenFile exists whenever any token is in use, which Dropper relies on.
	Next() (uint32, error)
}

// Keeper owns every gate in the kernel guard. internal/linkguard implements it, and nothing
// else in the proxy opens or extends a gate.
//
// Every GuardKeep it reads which runs are live and which machines are fenced, extends the gates
// of the runs that still are live and unfenced in one batch, retires the rest and opens a gate
// for each live, unfenced run that has none. Every live run has a gate, not only those with
// links, because a source's first SYN gets through only on its own gate. The listener and the
// admitter read gates from it and never write them.
type Keeper interface {
	// Ready is closed once the table is installed and the first cycle has run. The listener
	// accepts nothing before it.
	Ready() <-chan struct{}
	// Gate returns the open gate of run of machine id. A run that has no gate yet, such as one
	// a wake has just resumed, gets one in a cycle that starts after the call, and Gate nudges
	// that cycle and waits for it. It returns ErrRunEnded when run is not id's live run,
	// ErrFenced when id is fenced, ErrTokensSpent, ErrGuardDown while the guard is down or being
	// reinstalled, the error resolving the run's slot or ctx's error.
	Gate(ctx context.Context, id string, run Run) (Gate, error)
	// Retired returns a channel that is closed once g is no longer extended. It is already
	// closed for a gate the Keeper does not hold. The listener aborts a link when either of its
	// gates is retired. That is cleanup, not safety: the kernel drops the link's packets once
	// the gate expires, within GuardTTL, whether or not the listener acts.
	Retired(g Gate) <-chan struct{}
}

// Resolver turns machines into slot addresses and back. The Keeper and the close fallbacks are
// given one rather than reaching for internal/slots themselves, because slots.Finder only works
// on Linux and their tests also run on macOS. slots.Resolver is the production one.
type Resolver interface {
	// Addr returns the host address of the slot machine id is running at, and the run that is
	// there now. It returns ErrNotRunning when the machine is not running.
	Addr(id string) (netip.Addr, Run, error)
	// At returns the machine and run at a slot address now.
	At(addr netip.Addr) (Slot, error)
}

// Slot is who is at a slot address now.
type Slot struct {
	ID  string
	Run Run
}

// Store is the one way into links.json. Everything uses it rather than reading or writing the
// file, so the lock order and the fence rules are written once.
type Store interface {
	// Read returns the document. It never blocks on the lock, so the read paths that only
	// render state cannot be held up by a publish.
	Read() (Doc, error)
	// Update takes the lock, reads, calls f, and writes the result only if f returns nil. A
	// caller that already holds hibd's cleanup lock must release it first: cleanup takes
	// links second, and taking them the other way round would deadlock.
	Update(ctx context.Context, f func(*Doc) error) error
	// Fence marks id as being paused by Hibernaut and returns a release that clears the mark.
	// Admission refuses a fenced machine at either end.
	//
	// The fence is held by a lock file that the kernel closes when the holder dies, so a
	// checker can tell a pause that is still running from one whose process is gone without
	// guessing from a timestamp. A save may legitimately run for ten minutes
	// (internal/machines/snapshot.go:16-17), which no staleness rule could tell apart from a
	// crash.
	//
	// release clears the fence whether the pause succeeded or failed, and is safe to call
	// twice.
	Fence(ctx context.Context, id string, why string) (release func(), err error)
}

// Clock is the host's record of when each machine last did something that was not a link.
// internal/busy feeds it, and the admitter reads it: a machine whose own activity has gone
// stale may not wake or renew anything, which is what stops a ring of machines from holding
// each other up for ever.
//
// Link bytes never feed it. Neither does a link waking a machine.
type Clock interface {
	// Active reports when id last showed non-link activity. The zero time means never.
	Active(id string) time.Time
	// Touch records non-link activity at t. It is called by the busy watcher, by the SSH
	// bridge while a session is open, and once by a successful create or fork. It only moves
	// the clock forward: a t at or before what is recorded changes nothing, even when another
	// process recorded it, because callers pass their own times and can arrive out of order.
	// The first touch records t itself, never the time the record was made.
	Touch(id string, t time.Time)
}

// Fresh reports whether id's activity at now is recent enough to justify waking or renewing
// another machine. It is the one rule behind both, written once so the two cannot drift.
func Fresh(c Clock, id string, now time.Time) bool {
	last := c.Active(id)
	return !last.IsZero() && now.Sub(last) < LinkGrace
}

// LeaseUntil is the absolute end of the authority id's activity gives it over other machines.
func LeaseUntil(c Clock, id string, now time.Time) time.Time {
	last := c.Active(id)
	if last.IsZero() {
		return time.Time{}
	}
	return last.Add(LinkGrace)
}
