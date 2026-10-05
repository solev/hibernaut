package main

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
)

func TestStopOnSignalRecordsWhichSignal(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		ctx, cancel := context.WithCancelCause(t.Context())
		signals := make(chan os.Signal, 1)
		signals <- sig
		stopOnSignal(signals, cancel)
		if got := context.Cause(ctx); got != (stopped{sig}) {
			t.Errorf("after %v, cause %v", sig, got)
		}
	}
}

func TestSignalsSetTheExitCode(t *testing.T) {
	h := newHost(t)
	c := newClient(t, h)
	for sig, want := range map[syscall.Signal]int{syscall.SIGINT: 130, syscall.SIGTERM: 143, syscall.SIGHUP: 129} {
		ctx, cancel := context.WithCancelCause(t.Context())
		cancel(stopped{sig})
		if code := c.hibContext(ctx, "ls"); code != want {
			t.Errorf("ls stopped by %v = %d, want %d", sig, code, want)
		}
		if code := c.hibContext(ctx, "version"); code != 0 {
			t.Errorf("version stopped by %v = %d, want 0 as it finished", sig, code)
		}
	}
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(errors.New("not a signal"))
	if code := c.hibContext(ctx, "ls"); code != 1 {
		t.Errorf("ls cancelled without a signal = %d, want 1", code)
	}
}
