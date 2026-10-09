// Package linkadmit decides whether a connection may become a link, wakes a paused target when
// one may, and keeps the decision true for as long as the link is open.
//
// It owns the room reserve, the caps, the wake rate limit, the activity clock, the run watcher
// and the control socket hibd's other invocations use to cancel links across processes.
//
// The shape here is the contract M12's slices are built against. The bodies are the admission
// slice's to write; until then every entry point reports that links are off.
package linkadmit

import (
	"context"
	"sync"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/links"
)

// Config is everything admission needs.
type Config struct {
	// Engine resumes targets and reads their state.
	Engine engine.Engine
	// Runs is the run watcher, usually the one NewRuns returns.
	Runs links.Runs
	// Guard is the kernel fence. The admitter drops a machine's elements itself when the
	// listener fails to acknowledge a close in time.
	Guard links.Guard
	// Closer is the listener, for cancelling links. Leave it nil and call SetCloser: the
	// listener needs an admitter to be built and the admitter needs the listener to cancel,
	// so one of the two has to be bound late.
	Closer links.Closer
	// Resolver turns a machine into its slot address and a slot address back into the run
	// and token at it. Admission checks a connection against it twice: once before the dial
	// and once after, before the first byte moves.
	Resolver links.Resolver
	// Store is the one reader and writer of links.json, including the fence.
	Store links.Store
	// Clock is the record of non-link activity, which decides whether a source may wake or
	// renew anything.
	Clock links.Clock
	// Room reports how many more machines may run, after the reserve the owner is kept.
	Room func() (int, error)
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
}

var _ links.Admitter = (*Admitter)(nil)

// New checks the configuration and returns an admitter. It does not open the control socket.
func New(cfg Config) (*Admitter, error) {
	return &Admitter{cfg: cfg, closer: cfg.Closer}, nil
}

// SetCloser binds the listener after both exist. Serve refuses to start until it is set, so a
// wiring mistake is a startup failure rather than a link that cannot be cancelled.
func (a *Admitter) SetCloser(c links.Closer) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closer = c
}

// closerOrNil is how the bodies reach the listener.
func (a *Admitter) closerOrNil() links.Closer {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.closer
}

// Serve runs the control socket and the run sweep until ctx is done. The sweep is a backstop
// behind the cgroup events, not the main mechanism: it closes links whose endpoints no longer
// exist, are no longer running, or are running a different run from the admitted one.
func (a *Admitter) Serve(ctx context.Context) error {
	if a.closerOrNil() == nil {
		return links.ErrNoCloser
	}
	return links.ErrLinksOff
}

// Admit reserves room and caps, wakes the target if it is paused, and returns an Admission.
func (a *Admitter) Admit(ctx context.Context, req links.AdmitRequest) (links.Admission, error) {
	return links.Admission{}, links.ErrLinksOff
}

// Renew pushes the target's idle timeout back, while the source's own activity is fresh and
// never past the admission's lease.
func (a *Admitter) Renew(ad links.Admission, payload int64) {}

// Release gives back the room, the caps and the rate accounting.
func (a *Admitter) Release(ad links.Admission) {}

// RunWatcher answers which run owns a machine, from E2B's cgroups. It implements links.Runs.
type RunWatcher struct {
	cgroups string
}

var _ links.Runs = (*RunWatcher)(nil)

// NewRuns watches runs under the E2B cgroup root, /sys/fs/cgroup/e2b when cgroups is "".
func NewRuns(cgroups string) (*RunWatcher, error) {
	return &RunWatcher{cgroups: cgroups}, nil
}

// Current returns the run that owns id now.
func (w *RunWatcher) Current(id string) (links.Run, error) {
	return "", links.ErrNotRunning
}

// Watch closes the returned channel when run of machine id ends. A run that has already ended
// gives an already-closed channel, so admission cannot race the first read.
func (w *RunWatcher) Watch(id string, run links.Run) (<-chan struct{}, error) {
	return nil, links.ErrNotRunning
}

// Root is where the watcher is looking, for tests and for hibd's diagnostics.
func (w *RunWatcher) Root() string {
	if w.cgroups == "" {
		return "/sys/fs/cgroup/e2b"
	}
	return w.cgroups
}
