package hibd

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// snapshotFenceWait bounds how long a save or a fork waits for its machine's fence and for hibd
// proxy to close the machine's links. It is a variable so tests can shorten it.
var snapshotFenceWait = 30 * time.Second

var errSnapshotFenceWait = errors.New("the fence wait ran out")

// snapshotFence is machines.Config.Fenced: with links on, it runs saves, forks and unused task
// duplicate removals behind pauseFenced's fence, close and wait. No link wakes or reaches id
// during op. With links off it is nil, and op runs with no fence.
//
// Fork, SaveImage and FindTask hold hibd.lock while they wait for the fence, and a pause or rm
// of the same machine holds the fence while it waits for hibd.lock. The wait before op is
// bounded: past snapshotFenceWait it leaves the machine as it was and lets the pause or rm go
// on. op itself runs under the caller's ctx, unbounded, as it does with links off.
func (a *App) snapshotFence(cfg config) func(ctx context.Context, id, name, why string, op func(context.Context) error) error {
	if !cfg.linksConfiguredOn() {
		return nil
	}
	return func(ctx context.Context, id, name, why string, op func(context.Context) error) error {
		wait, cancel := context.WithCancelCause(ctx)
		defer cancel(nil)
		timer := time.AfterFunc(snapshotFenceWait, func() { cancel(errSnapshotFenceWait) })
		defer timer.Stop()
		started := false
		// Fork and image save run as root, so closeLinks never asks for the command to be run
		// again as root, and the one named here is only ever informative.
		err := a.pauseFenced(wait, cfg, id, name, why, why+" "+name, func(context.Context) error {
			if !timer.Stop() {
				return errSnapshotFenceWait
			}
			started = true
			return op(ctx)
		})
		ranOut := errors.Is(err, errSnapshotFenceWait) ||
			errors.Is(err, context.Canceled) && errors.Is(context.Cause(wait), errSnapshotFenceWait)
		if !started && ranOut {
			return fmt.Errorf("left %s as it was: raising its link fence and closing its links took longer than %v. A pause or rm of %s may be waiting for this %s: try the %s again once that is done", name, snapshotFenceWait, name, why, why)
		}
		return err
	}
}
