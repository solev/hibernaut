package machines

import (
	"context"
	"errors"

	"github.com/solev/hibernaut/internal/hibapi"
)

// MaxCopies is the most copies one Fork makes.
const MaxCopies = 10

// Fork makes count copies of machine, from 1 to MaxCopies, named MACHINE-k with the lowest free
// k. Each copy gets a fresh host key, Config.Setup, and machine's idle time and image. Fork
// stops at the first copy it can't make, and returns the copies made with the error. machine
// is saved as SaveImage saves, and left running or paused as it was.
func (m *Manager) Fork(ctx context.Context, machine string, count int) ([]hibapi.Machine, error) {
	return nil, errors.ErrUnsupported
}

// SweepForks deletes the templates Fork saved machines into, except those copies still use. It
// returns an error wrapping ErrBusy if it can't take the state lock at once.
func (m *Manager) SweepForks(ctx context.Context) error {
	return errors.ErrUnsupported
}
