package leakscan

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

// WaitOptions bounds a build's readiness wait. Zero values use a 15-minute timeout,
// one-second polling, time.Now and a context-aware timer.
type WaitOptions struct {
	Timeout  time.Duration
	Interval time.Duration
	Now      func() time.Time
	Sleep    func(context.Context, time.Duration) error
}

// WaitBuild waits for this build to be ready and its local files to be complete.
// The build ID stays fixed. A failed build or invalid artifact fails immediately.
func WaitBuild(ctx context.Context, query Query, storageDir, buildID string, opts WaitOptions) error {
	if !buildIDRE.MatchString(buildID) {
		return fmt.Errorf("bad build ID %q", buildID)
	}
	if opts.Timeout < 0 || opts.Interval < 0 {
		return errors.New("build wait timeout and interval must not be negative")
	}
	if opts.Timeout == 0 {
		opts.Timeout = 15 * time.Minute
	}
	if opts.Interval == 0 {
		opts.Interval = time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Sleep == nil {
		opts.Sleep = sleep
	}
	waitCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	deadline := opts.Now().Add(opts.Timeout)
	last := errors.New("build status not yet read")
	timedOut := func() error {
		return fmt.Errorf("timed out after %s waiting for build %s to be ready and complete: %v: %w",
			opts.Timeout, buildID, last, context.DeadlineExceeded)
	}
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("waiting for build %s: %w", buildID, err)
		}
		if waitCtx.Err() != nil || !opts.Now().Before(deadline) {
			return timedOut()
		}
		rows, err := query(waitCtx, "SELECT status_group FROM env_builds WHERE id = '"+buildID+"'")
		if err != nil {
			if waitCtx.Err() != nil {
				continue
			}
			return fmt.Errorf("reading build %s status: %w", buildID, err)
		}
		if len(rows) != 1 || len(rows[0]) != 1 {
			return fmt.Errorf("build %s: got rows %q, want one status", buildID, rows)
		}
		switch status := rows[0][0]; status {
		case "failed":
			return fmt.Errorf("build %s failed", buildID)
		case "pending", "in_progress":
			last = fmt.Errorf("build status is %s", status)
		case "ready":
			_, last = readBuildSet(storageDir, buildID)
			if last == nil {
				if waitCtx.Err() != nil || !opts.Now().Before(deadline) {
					last = errors.New("readiness checks finished after the deadline")
					continue
				}
				return nil
			}
			if !errors.Is(last, os.ErrNotExist) && !errors.Is(last, errIncomplete) {
				return fmt.Errorf("build %s cannot be scanned: %w", buildID, last)
			}
		default:
			return fmt.Errorf("build %s: unknown status %q", buildID, status)
		}
		if err := opts.Sleep(waitCtx, min(opts.Interval, deadline.Sub(opts.Now()))); err != nil {
			if waitCtx.Err() != nil {
				continue
			}
			return fmt.Errorf("waiting for build %s: %w", buildID, err)
		}
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
