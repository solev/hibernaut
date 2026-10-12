package linkadmit

import (
	"context"
	"time"

	"github.com/solev/hibernaut/internal/linkctl"
	"github.com/solev/hibernaut/internal/room"
)

// RunBackstop is how often the run watcher re-reads its cgroups without an event.
const RunBackstop = runBackstop

// NewPollNotifier returns the wait and close of the notifier the run watcher uses without
// inotify, which wakes only every interval.
func NewPollNotifier(every time.Duration) (wait, stop func()) {
	p := newPollNotifier(every)
	return p.wait, p.close
}

// SetReadRoom replaces what wakes read the host's room with. Call it before admitting.
func SetReadRoom(a *Admitter, f func() (room.Host, error)) { a.readRoom = f }

// Sweep runs one sweep now.
func Sweep(a *Admitter, ctx context.Context) { a.sweep(ctx) }

// Handle answers one control-socket request without the socket.
func Handle(a *Admitter, ctx context.Context, req linkctl.Request) linkctl.Response {
	return a.handle(ctx, req)
}

// Waiters is how many admissions wait on target's wake.
func Waiters(a *Admitter, target string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if w := a.waking[target]; w != nil {
		return w.waiters
	}
	return 0
}
