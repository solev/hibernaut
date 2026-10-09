package links

import "errors"

// The errors the two sides share. A refusal reason in the link log is the Error text of one of
// these, so adding a reason means adding an error here.
var (
	// ErrLinksOff is returned by everything when HIBD_LINK_PORT is 0.
	ErrLinksOff = errors.New("links are off")
	// ErrNotPublished is a connection to a port the target has not published.
	ErrNotPublished = errors.New("not published")
	// ErrReservedPort is an attempt to publish 8081 or 49983.
	ErrReservedPort = errors.New("reserved port")
	// ErrUnknownAddress is a destination inside CIDR that no machine holds.
	ErrUnknownAddress = errors.New("unknown address")
	// ErrSelfLink is a machine reaching its own published port. It has a localhost to use
	// instead, and a self-link is the shortest way to keep a machine awake for ever.
	ErrSelfLink = errors.New("self link")
	// ErrSourceIdle is a source whose own non-link activity has gone stale. It gates waking as
	// well as renewing: gating renewal alone lets a ring pause and then be woken again by the
	// first client that retries.
	ErrSourceIdle = errors.New("source idle")
	// ErrPausedByHand is a target a person paused. Nothing a machine does resumes it.
	ErrPausedByHand = errors.New("paused by hand")
	// ErrFenced is a target hibd is in the middle of pausing, saving, forking or removing.
	ErrFenced = errors.New("pause in progress")
	// ErrQuiescing is any link at all while the host is winding down for the night.
	ErrQuiescing = errors.New("quiescing")
	// ErrNoRoom is the room reserve: the host keeps space for one machine the owner may want
	// to start by hand.
	ErrNoRoom = errors.New("no room")
	// ErrRateLimited is more than one wake of a target per WakeEvery.
	ErrRateLimited = errors.New("rate limited")
	// ErrTooManyLinks is any of MaxPerSource, MaxPerTarget and MaxLinks.
	ErrTooManyLinks = errors.New("too many links")
	// ErrRevoked is a link whose port was unpublished under it.
	ErrRevoked = errors.New("revoked")
	// ErrRunEnded is an endpoint whose run ended: paused, removed, saved, timed out or
	// crashed. It is also what the re-check before the first byte returns when a slot has
	// been handed to another machine.
	ErrRunEnded = errors.New("run ended")
	// ErrLeaseExpired is a link outliving the source activity that justified it.
	ErrLeaseExpired = errors.New("lease expired")
	// ErrNotRunning is Runs.Current for a machine with no run.
	ErrNotRunning = errors.New("not running")
	// ErrDialFailed is the target refusing the connection, or not answering within
	// DialTimeout. It almost always means nothing is listening on the published port.
	ErrDialFailed = errors.New("dial failed")
	// ErrGuardDown is the kernel fence not being installed. Links stay off until it is: a
	// listener without the fence cannot promise that a wedged process stops leaking bytes.
	ErrGuardDown = errors.New("link firewall is not installed")
	// ErrTooManyPorts is MaxPorts.
	ErrTooManyPorts = errors.New("too many published ports")
	// ErrTooManyMachines is MaxMachines.
	ErrTooManyMachines = errors.New("too many machines with published ports")
	// ErrNoAddresses is the allocator running off the end of CIDR.
	ErrNoAddresses = errors.New("no virtual addresses left")
	// ErrAlreadyPublished and ErrNotFound are reported for a whole batch that asks for
	// nothing else. Mixed with real work they are no-ops, because hib port add NAME 8080 8081
	// run twice should end with both ports published and no error, not with a batch that
	// half-applied.
	ErrAlreadyPublished = errors.New("already published")
	ErrNotFound         = errors.New("no such published port")
	// ErrTokensSpent is the guard's token counter reaching GuardMaxToken. Tokens are never
	// reused, so the only way on is to abort every link, reinstall the table and start again.
	ErrTokensSpent = errors.New("out of link tokens")
	// ErrNoConntrack is a connection with no conntrack entry, so nothing says which run its
	// first packet came from. It is refused exactly like a token mismatch.
	ErrNoConntrack = errors.New("no connection record")
	// ErrWrongRun is a connection whose conntrack mark carries a token that is not the one of
	// the run at that slot now. It is the stale accept queue: the listener stalled, the slot
	// was recycled, and the bytes waiting to be read belong to the machine that has gone.
	ErrWrongRun = errors.New("connection belongs to an earlier run")
	// ErrStateUnreadable is links.json being missing a version, carrying one this build does
	// not know, or failing to parse. Links are refused rather than guessed at, the same way
	// hibapi stops on a version it does not know.
	ErrStateUnreadable = errors.New("link state is unreadable")
	// ErrFull is the listener at MaxLinks. It is distinct from ErrTooManyLinks, which is a
	// per-machine cap: this one is the host, and the listener answers it by not accepting for
	// AcceptPause rather than by accepting and closing.
	ErrFull = errors.New("the host is at its link limit")
	// ErrNoCloser is the admitter being started before the listener was bound to it. The
	// listener needs an admitter to be built, so one of the two has to be bound late; this
	// turns forgetting into a startup failure rather than a link nobody can cancel.
	ErrNoCloser = errors.New("the link listener is not bound to the admitter")
)
