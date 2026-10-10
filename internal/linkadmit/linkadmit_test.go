package linkadmit_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/solev/hibernaut/internal/linkadmit"
	"github.com/solev/hibernaut/internal/links"
)

// The listener needs an admitter to be built and the admitter needs the listener to cancel
// links, so one of the two has to be bound late. Forgetting must be a startup failure, not a
// link that nothing can close.
func TestServeRefusesWithoutACloser(t *testing.T) {
	a := newEnv(t, func(c *linkadmit.Config) { c.Closer, c.Socket = nil, socketPath(t) }).a
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
	a := newEnv(t, func(c *linkadmit.Config) { c.Socket = socketPath(t) }).a
	if err := a.Serve(stopped()); err != nil {
		t.Errorf("serving with a configured closer = %v", err)
	}
}

// New refuses a configuration missing a part admission needs, and names the part.
func TestNewNamesTheMissingPart(t *testing.T) {
	var full linkadmit.Config
	newEnv(t, func(c *linkadmit.Config) { full = *c })
	for _, tc := range []struct {
		part  string
		clear func(*linkadmit.Config)
	}{
		{"engine", func(c *linkadmit.Config) { c.Engine = nil }},
		{"run watcher", func(c *linkadmit.Config) { c.Runs = nil }},
		{"gate keeper", func(c *linkadmit.Config) { c.Keeper = nil }},
		{"link store", func(c *linkadmit.Config) { c.Store = nil }},
		{"activity clock", func(c *linkadmit.Config) { c.Clock = nil }},
	} {
		cfg := full
		tc.clear(&cfg)
		if _, err := linkadmit.New(cfg); err == nil || !strings.Contains(err.Error(), tc.part) {
			t.Errorf("New without the %s = %v, want an error naming it", tc.part, err)
		}
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
