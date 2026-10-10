// Package linkadmit decides whether a connection may become a link, wakes a paused target when
// one may, and keeps the decision true for as long as the link is open.
//
// It owns the room reserve, the caps, the wake rate limit, the activity clock, the run watcher
// and the control socket hibd's other invocations use to cancel links across processes.
//
// New refuses a configuration missing any of Engine, Runs, Keeper, Store or Clock.
package linkadmit

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/links"
	"github.com/solev/hibernaut/internal/machines"
	"github.com/solev/hibernaut/internal/room"
)

// Config is everything admission needs.
type Config struct {
	// Engine resumes targets and reads their state.
	Engine engine.Engine
	// Runs is the run watcher, usually the one NewRuns returns.
	Runs links.Runs
	// Keeper owns the gates. Admit asks it for the target run's gate after any wake, so the gate
	// is open before the listener dials.
	Keeper links.Keeper
	// Dropper drops a machine's gates when the listener does not acknowledge a close within
	// LinkCloseGrace.
	Dropper links.Dropper
	// Closer is the listener, for cancelling links. Leave it nil and call SetCloser: the
	// listener needs an admitter to be built and the admitter needs the listener to cancel,
	// so one of the two has to be bound late.
	Closer links.Closer
	// Resolver finds a machine's slot address for the close fallback's Drop.
	Resolver links.Resolver
	// Store is the one reader and writer of links.json, including the fence.
	Store links.Store
	// Clock is the record of non-link activity, which decides whether a source may wake or
	// renew anything.
	Clock links.Clock
	// Room reports the room for a wake of target, a sandbox ID, counted in default machines:
	// free is how many more may run after the reserve the owner is kept, and need is how many
	// waking target takes, at least 1.
	Room func(target string) (free, need int, err error)
	// Quiescing reports whether the host is winding down for the night.
	Quiescing func() bool
	// Socket is the control socket path, or "" for the default.
	Socket string
	// Now is the clock, so tests do not wait.
	Now func() time.Time
}

// Admitter is the admission side. It implements links.Admitter.
type Admitter struct {
	mu     sync.Mutex
	cfg    Config
	closer links.Closer

	now       func() time.Time
	quiescing func() bool
	// wake resumes a target by sandbox ID under an absolute lease: machines.Manager.Wake.
	wake func(ctx context.Context, id string, leaseUntil time.Time) (engine.Machine, error)
	// readRoom is what the wake checks the host's room with. Tests replace it.
	readRoom func() (room.Host, error)

	// Everything below is guarded by mu. Caps count every admission from its reservation to
	// its Release, pending ones included.
	perSource map[string]int
	perTarget map[string]int
	total     int
	// pendingWakes is the room held by wakes in flight, counted in default machines. A woken
	// machine is in Room's own count once it runs, so the reservation ends with the wake.
	pendingWakes int
	waking       map[string]*wakeup
	lastWake     map[string]time.Time
	lastRenew    map[string]time.Time
	adms         map[*adm]struct{}
}

var _ links.Admitter = (*Admitter)(nil)

// New checks the configuration and returns an admitter. It does not open the control socket.
func New(cfg Config) (*Admitter, error) {
	switch {
	case cfg.Engine == nil:
		return nil, errors.New("linkadmit: no engine")
	case cfg.Runs == nil:
		return nil, errors.New("linkadmit: no run watcher")
	case cfg.Keeper == nil:
		return nil, errors.New("linkadmit: no gate keeper")
	case cfg.Store == nil:
		return nil, errors.New("linkadmit: no link store")
	case cfg.Clock == nil:
		return nil, errors.New("linkadmit: no activity clock")
	}
	a := &Admitter{
		cfg: cfg, closer: cfg.Closer,
		now: cfg.Now, quiescing: cfg.Quiescing, readRoom: room.Read,
		perSource: map[string]int{}, perTarget: map[string]int{},
		waking: map[string]*wakeup{}, lastWake: map[string]time.Time{}, lastRenew: map[string]time.Time{},
		adms: map[*adm]struct{}{},
	}
	if a.now == nil {
		a.now = time.Now
	}
	if a.quiescing == nil {
		a.quiescing = func() bool { return false }
	}
	a.wake = machines.New(machines.Config{
		Engine:   cfg.Engine,
		Now:      a.now,
		ReadRoom: func() (room.Host, error) { return a.readRoom() },
		Activity: cfg.Clock,
		Fence:    cfg.Store.Fence,
	}).Wake
	return a, nil
}

// SetCloser binds the listener after both exist. Serve refuses to start until it is set, so a
// wiring mistake is a startup failure rather than a link that cannot be cancelled.
func (a *Admitter) SetCloser(c links.Closer) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closer = c
}

// closerOrNil returns the closer SetCloser bound, or nil before it is called.
func (a *Admitter) closerOrNil() links.Closer {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.closer
}

// Serve runs the control socket and the run sweep until ctx is done. The sweep is a backstop
// behind the cgroup events, not the main mechanism: it closes links whose endpoints no longer
// exist, are no longer running, or are running a different run from the admitted one.
//
// The socket is served whether or not the listener ever serves. A stale socket a killed proxy
// left behind is removed first, and the socket is unlinked on the way out.
func (a *Admitter) Serve(ctx context.Context) error {
	if a.closerOrNil() == nil {
		return links.ErrNoCloser
	}
	return a.serve(ctx)
}

// Admit reserves room and caps, wakes the target if it is paused, and returns an Admission.
func (a *Admitter) Admit(ctx context.Context, req links.AdmitRequest) (links.Admission, error) {
	return a.admit(ctx, req)
}

// Renew pushes the target's idle timeout back for payload bytes. It re-derives the lease from
// the activity clock on every call rather than reading ad.LeaseUntil, so a source still being
// worked on keeps renewing, and a source whose own non-link activity has gone stale stops, after
// which the target pauses on its own idle timeout. It never cuts a link, and link bytes never
// feed the clock.
func (a *Admitter) Renew(ad links.Admission, payload int64) { a.renew(ad, payload) }

// Release gives back the room and the caps. It keeps the rate history of a successful wake, so
// a link opened and closed in a loop cannot wake a machine every second.
func (a *Admitter) Release(ad links.Admission) { a.release(ad) }
