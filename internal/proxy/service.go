package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/solev/hibernaut/internal/egress"
	"github.com/solev/hibernaut/internal/egress/hook"
)

// Rule owns the redirect and its periodic repair.
type Rule interface {
	Ensure(context.Context) (bool, error)
	Keep(context.Context, time.Duration, func(string, ...any)) error
	Remove(context.Context) error
}

// Service serves intercepted traffic and owns the rule and audit writer.
type Service struct {
	Server *egress.Server
	Rule   Rule
	Audit  io.Closer
	Logf   func(string, ...any)
}

// Serve installs the redirect, repairs it every five seconds and serves until ctx ends.
// On every exit it stops repairs, removes the redirect, stops serving and closes Audit.
// A stray hibernaut rule (hook.ErrStrayRule) is logged through Logf and does not stop it.
func (s *Service) Serve(ctx context.Context, l net.Listener) (result error) {
	serveCtx, stopServing := context.WithCancel(context.WithoutCancel(ctx))
	keepCtx, stopKeeping := context.WithCancel(ctx)
	var served, kept chan error
	defer func() {
		stopKeeping()
		if kept != nil {
			<-kept
		}
		removeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		err := s.Rule.Remove(removeCtx)
		cancel()
		if err != nil {
			result = errors.Join(result, fmt.Errorf("removing proxy hook: %w", err))
		}
		stopServing()
		_ = l.Close()
		if served != nil {
			err := <-served
			if !errors.Is(err, context.Canceled) {
				result = errors.Join(result, err)
			}
		}
		result = errors.Join(result, s.Audit.Close())
	}()
	if _, err := s.Rule.Ensure(ctx); errors.Is(err, hook.ErrStrayRule) {
		// The hook is in place around a rule iptables would not delete, which is only a warning.
		if s.Logf != nil {
			s.Logf("installing proxy hook: %v", err)
		}
	} else if err != nil {
		return fmt.Errorf("installing proxy hook: %w", err)
	}
	kept = make(chan error, 1)
	go func() { kept <- s.Rule.Keep(keepCtx, 5*time.Second, s.Logf) }()
	served = make(chan error, 1)
	go func() { served <- s.Server.Serve(serveCtx, l) }()
	select {
	case <-ctx.Done():
		return nil
	case err := <-served:
		served = nil
		return err
	case err := <-kept:
		kept = nil
		return err
	}
}
