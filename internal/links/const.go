package links

import (
	"net/netip"
	"time"
)

// The virtual addresses machines resolve names to. 198.18.0.0/15 is IANA's benchmarking range:
// it is not private, so E2B's per-sandbox firewall does not deny it the way it denies 10/8,
// 172.16/12, 192.168/16, 127/8 and 169.254/16, and it is not routed on the internet either, so
// nothing outside the host can be reached by accident.
const (
	// CIDR is the whole range. Nothing outside it is ever redirected into the listener.
	CIDR = "198.18.0.0/15"
	// MaxMachines is how many machines may hold a virtual address at once.
	MaxMachines = 256
	// MaxPorts is how many ports one machine may publish.
	MaxPorts = 16
)

// First is the first virtual address handed out. Addresses are handed out in order and never
// reused, so a name that resolved to an address in a machine's /etc/hosts can never come to mean
// a different machine.
var First = netip.MustParseAddr("198.18.0.1")

// Reserved ports are never publishable. 8081 is the SSH bridge envd exposes, and publishing it
// would let one machine reach another's shell with the host's own credentials; 49983 is envd
// itself, which would hand over filesystem and exec access.
var Reserved = []int{8081, 49983}

// Where state lives. The two files sit in HIBD_STATE_DIR beside vault.json, are written through
// internal/atomicfile and are read under the lock at LockName.
const (
	// StateName is the published-port state. hibd writes it as root, but the state directory
	// belongs to the login user (internal/machines/ownership.go:16-31) and every file hibd
	// publishes there is given that owner, so this one is too. The mode keeps it to the owner
	// alone, because it names every machine that has published anything.
	StateName = "links.json"
	// LockName is flocked for the whole of a read-modify-write of StateName.
	LockName = "links.lock"
	// ActivityDir is the activity clock: one empty file per machine, named by its sandbox ID,
	// whose modification time is the machine's last activity that was not a link. It is under
	// the state directory so a restart of hibd does not reset every machine's authority over
	// the others. busy, the SSH bridge, create and fork feed it.
	ActivityDir = "links/activity"
	// FenceName is the lock file whose holder owns a machine's pause fence, formatted with
	// the sandbox ID. A checker takes LOCK_SH|LOCK_NB on it: failing means the pause that
	// raised the fence is still running, whatever the fence's age says. A save may run for
	// ten minutes (internal/machines/snapshot.go:16-17), so age alone cannot tell a long
	// save from a crash.
	FenceName = "links-fence-%s.lock"
	// StateMode and LockMode are the modes both files are created with: the login owner only.
	// hib ls, hib pause and hib rm never open either file. They ask hibd, so the owner is the
	// login user for consistency with the rest of the directory, not so that hib can read it.
	StateMode = 0o600
	LockMode  = 0o600
	// CtlSocket is the control socket the listener serves and hibd's other invocations dial.
	// It is in /run rather than the state directory because it must not survive a reboot.
	CtlSocket = "/run/hibernaut/links.sock"
	// RunDir is created by vm/systemd/hibernaut-tmpfiles.conf. systemd sets up the unit's
	// ReadWritePaths before the unit's own code runs, so a unit that creates this directory
	// itself fails with 226/NAMESPACE on the first boot after an install.
	RunDir = "/run/hibernaut"
	// TokenFile holds the next guard token. It is in RunDir, not the state directory, so a
	// reboot restarts it: tokens only have to be unique among the runs that exist at once,
	// and after a reboot none do.
	TokenFile = "/run/hibernaut/token"
)

// Version is the version field every links.json carries. A reader that meets a version it does
// not know stops rather than guessing, the way hibapi does.
const Version = 1

// The caps. They are M12's own: the egress proxy has none to inherit, its Accept loop starts a
// goroutine per connection with nothing counting them.
const (
	// MaxPerSource and MaxPerTarget are live links per machine, counted by sandbox ID so a
	// rename does not reset them.
	MaxPerSource = 64
	MaxPerTarget = 64
	// MaxLinks is the whole host. Past it the listener stops accepting for AcceptPause rather
	// than accepting and closing, so the kernel's backlog does the queueing.
	MaxLinks = 512
	// AcceptRate and AcceptBurst limit connections from one source machine.
	AcceptRate  = 20
	AcceptBurst = 40
)

// The timings.
const (
	// AcceptPause is how long the listener stops accepting for when MaxLinks is reached.
	AcceptPause = 100 * time.Millisecond
	// AdmitTimeout bounds admission itself, not the wake it may start.
	AdmitTimeout = 2 * time.Second
	// WakeTimeout bounds a resume. A warm resume takes about 0.6 s.
	WakeTimeout = 60 * time.Second
	// WakeEvery is the shortest gap between two link-started wakes of one target, so a client
	// that retries in a loop cannot resume a machine over and over.
	WakeEvery = 30 * time.Second
	// DialTimeout bounds the target leg's connect.
	DialTimeout = 10 * time.Second
	// LinkRenew is the shortest gap between two renewals of one target, however much payload
	// moves in between.
	LinkRenew = 30 * time.Second
	// LinkGrace is how long a machine's own activity keeps authorising links after it stops.
	// It is long enough that a pause for thought does not drop a benchmark's links, and short
	// enough that an abandoned ring of machines costs at most LinkGrace plus one idle timeout.
	LinkGrace = 5 * time.Minute
	// LinkCloseGrace is how long the admitter waits for the listener to acknowledge a close
	// before it drops the machine's gates itself.
	LinkCloseGrace = 5 * time.Second
	// CtlTimeout bounds one control-socket call. It is longer than LinkCloseGrace, because a
	// close call has to wait out that grace and then answer over the socket: a shorter timeout
	// would make the caller give up on a handshake that was about to succeed. A hibd that gets
	// no answer within it falls back to Drop itself.
	CtlTimeout = 7 * time.Second
	// Keepalive is the TCP keepalive set on both legs. Links have no idle timeout of their
	// own: a database connection pool is allowed to sit idle, and the lease is what bounds it.
	Keepalive = 30 * time.Second
	// SweepEvery is the backstop that closes links whose endpoints are gone, behind the
	// cgroup events that normally do it first.
	SweepEvery = 5 * time.Second
	// DrainTimeout bounds the wait for a socket's send queue after shutdown(SHUT_WR), before
	// the listener closes it anyway.
	DrainTimeout = 2 * time.Second
)

// The kernel guard. It is what makes a run's gate close whether or not the listener is alive,
// and every number in it is chosen against one deadline: E2B returns a slot to its pool after
// SlotReturnDelay, and nothing of the old run may still pass once another machine holds it.
const (
	// GuardTable is the nftables table the sets and chains live in. It is a table of its own,
	// not an addition to hibernaut-sandboxes, so that deleting it is a complete uninstall and
	// so that it can never weaken rules that are already there: nftables runs every table's
	// chains on a hook and a drop in any of them is final.
	GuardTable = "hibernaut-links"
	// GuardSet is the set of gates that are open, keyed ipv4_addr . mark: a slot's host
	// address together with the mark carrying that run's token. Keying it on the token as
	// well as the address is what stops a new occupant of a slot inheriting the old run's
	// gate, which an address-only key could not tell apart.
	GuardSet = "link_live"
	// GuardMap maps a slot's host address to the mark of the run at that slot now. A packet
	// from a machine arrives with no mark, so prerouting looks its source up here to give it
	// one. A miss stops the rule that sets the mark, so the mark stays zero, which matches no
	// element and so is dropped.
	GuardMap = "link_src"
	// GuardMark is the top byte of every link mark. The low GuardTokenBits are the run's
	// token, so a mark is GuardMark<<GuardTokenBits | token.
	GuardMark = 0x4c
	// GuardTokenBits is the width of the token.
	GuardTokenBits = 24
	// GuardMaxToken is the largest token. Tokens are never reused within a boot, so a host
	// that has used them all opens no new gate until it reboots: gates already open go on
	// being extended, and a run that needs a new one gets ErrTokensSpent. At one new gate a
	// second that is more than six months.
	GuardMaxToken = 1<<GuardTokenBits - 1
	// GuardTTL is the timeout each element carries. An element is checked for expiry when it
	// is looked up, before any garbage collection, so it stops matching the moment it is due.
	GuardTTL = 1 * time.Second
	// GuardKeep is how often the Keeper extends the live gates, after re-reading which runs
	// are live and which machines are fenced. A Keeper that stalls for longer than
	// GuardTTL - GuardKeep loses its gates, which is the point: it fails closed.
	GuardKeep = 250 * time.Millisecond
	// GuardPriority is the hook priority of the guard's chains. It is before NAT
	// (dstnat -100), so prerouting still sees the original 198.18.0.0/15 destination. The
	// guard reads packet and socket marks and never conntrack, so where it sits relative to
	// conntrack does not matter.
	GuardPriority = -150
	// GuardSelfCheck is how often the Keeper confirms the table, sets, chains and
	// net.ipv4.tcp_fwmark_accept are still as Install left them. E2B reloads its own
	// nftables rules, and an operator can flush.
	GuardSelfCheck = 5 * time.Second
	// SlotReturnDelay is how long E2B waits before handing a freed slot to another machine
	// (packages/orchestrator/internal/sandbox/pool.go:25-32 at the pinned ref). Teardown
	// waits for Firecracker to exit before the return starts, so this is the whole budget.
	// A gate outlives its run by less than 2*GuardTTL, and that has to fit inside this with
	// room left over; TestGuardClosesBeforeTheSlotIsReused holds us to it.
	SlotReturnDelay = 3 * time.Second
	// GuardMargin is the room left between 2*GuardTTL and SlotReturnDelay.
	GuardMargin = 1 * time.Second
)

// Mark builds the packet mark for a run's token, which is at most GuardMaxToken.
func Mark(token uint32) uint32 {
	return GuardMark<<GuardTokenBits | token
}

// Token takes the token back out of a mark. It reports false for a mark that is not one of
// ours and for token 0, which no run holds, so traffic that happens to carry some other mark
// is never mistaken for a link.
func Token(mark uint32) (uint32, bool) {
	if mark>>GuardTokenBits != GuardMark {
		return 0, false
	}
	t := mark & GuardMaxToken
	return t, t != 0
}

// The link log. It is separate from audit.jsonl because audit.Entry's fields are request-shaped
// and hib log --json is a stable shape, so link records there would print empty columns in every
// hib log.
const (
	// LogPath is the default, overridden by HIBD_LINK_LOG.
	LogPath = "/var/log/hibernaut/links.jsonl"
	// LogMaxBytes is when the log rotates, keeping one older file.
	LogMaxBytes = 10 << 20
	// FoldWindow is how long identical refusals are folded into one line with a count, so a
	// client retrying in a loop writes one line per window instead of thousands.
	FoldWindow = 10 * time.Second
)

// Port is the default port the listener serves on. It is not the default value of the setting:
// HIBD_LINK_PORT defaults to 0, which means links are off, and it stays 0 until the slice that
// turns links on changes it.
const Port = 5444

// IsReserved reports whether p may never be published.
func IsReserved(p int) bool {
	for _, r := range Reserved {
		if p == r {
			return true
		}
	}
	return false
}
