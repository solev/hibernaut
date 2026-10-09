package linkadmit_test

import (
	"context"
	"errors"
	"testing"

	"github.com/solev/hibernaut/internal/linkadmit"
	"github.com/solev/hibernaut/internal/links"
)

// The listener needs an admitter to be built and the admitter needs the listener to cancel
// links, so one of the two has to be bound late. Forgetting must be a startup failure, not a
// link that nothing can close.
func TestServeRefusesWithoutACloser(t *testing.T) {
	a, err := linkadmit.New(linkadmit.Config{Socket: socketPath(t)})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := a.Serve(context.Background()); !errors.Is(err, links.ErrNoCloser) {
		t.Errorf("serving unbound = %v, want %v", err, links.ErrNoCloser)
	}
	a.SetCloser(stubCloser{})
	if err := a.Serve(stopped()); err != nil {
		t.Errorf("serving after SetCloser = %v", err)
	}
}

// A closer passed in the config is used without a call to SetCloser, so a wiring that does not
// need late binding does not have to ask for it.
func TestAConfiguredCloserCounts(t *testing.T) {
	a, err := linkadmit.New(linkadmit.Config{Closer: stubCloser{}, Socket: socketPath(t)})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := a.Serve(stopped()); err != nil {
		t.Errorf("serving with a configured closer = %v", err)
	}
}

// stopped is a context that is already done, so Serve binds its socket and returns at once.
func stopped() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

type stubCloser struct{}

func (stubCloser) AbortMachine(context.Context, string, string) (int, error) { return 0, nil }

func (stubCloser) AbortPorts(context.Context, string, []int, string) (int, error) { return 0, nil }

func (stubCloser) Stat() links.Stats { return links.Stats{} }
