package hibd

import (
	"context"

	"github.com/solev/hibernaut/internal/statefile"
)

// The bodies live in internal/statefile, which sits below hibd so that links can take the same
// locks. These wrappers keep hibd's own call sites unchanged.

func waitLock(ctx context.Context, stateDir, name, what string) (func(), error) {
	return statefile.WaitLock(ctx, stateDir, name, what)
}

func tryLock(stateDir, name, what string) (unlock func(), ok bool, err error) {
	return statefile.TryLock(stateDir, name, what)
}
