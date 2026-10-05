package e2e

import (
	"context"
	"fmt"
	"strings"
	"time"
)

var snapshotSidecars = [...]string{"memfile.header", "rootfs.ext4.header", "snapfile", "metadata.json"}

type snapshotAssignment struct {
	ID, Status string
}

func parseSnapshotAssignments(out string) ([]snapshotAssignment, error) {
	var builds []snapshotAssignment
	seen := map[string]bool{}
	for line := range strings.Lines(out) {
		fields := strings.Split(strings.TrimSuffix(line, "\n"), "|")
		if len(fields) != 2 || !buildUUID.MatchString(fields[0]) || seen[fields[0]] {
			return nil, fmt.Errorf("invalid snapshot build listing")
		}
		switch fields[1] {
		case "ready", "failed", "pending", "in_progress":
		default:
			return nil, fmt.Errorf("snapshot build has an unknown status")
		}
		builds = append(builds, snapshotAssignment{ID: fields[0], Status: fields[1]})
		seen[fields[0]] = true
	}
	return builds, nil
}

func latestDurableBuild(out, previous string) (string, bool, error) {
	builds, err := parseSnapshotAssignments(out)
	if err != nil {
		return "", false, err
	}
	if len(builds) == 0 {
		return "", false, nil
	}
	latest := builds[len(builds)-1]
	if latest.Status == "failed" {
		return "", false, fmt.Errorf("latest snapshot build %s failed", latest.ID)
	}
	if latest.ID == previous {
		return latest.ID, false, nil
	}
	return latest.ID, latest.Status == "ready", nil
}

func durableFilesCommand(id string) (string, error) {
	if !buildUUID.MatchString(id) {
		return "", fmt.Errorf("snapshot build must be a UUID")
	}
	return `sudo sh -c 'set -eu; d=/var/lib/e2b/local-template-storage/` + id + `
for f in ` + strings.Join(snapshotSidecars[:], " ") + `; do
	if [ ! -f "$d/$f" ]; then
		printf "pending\n"
		exit 0
	fi
done
printf "ready\n"'`, nil
}

func parseDurableFiles(out string) (bool, error) {
	switch strings.TrimSuffix(out, "\n") {
	case "ready":
		return true, nil
	case "pending":
		return false, nil
	default:
		return false, fmt.Errorf("invalid snapshot file readiness")
	}
}

func pollDurable(parent context.Context, limit, interval time.Duration, check func(context.Context) (string, bool, error)) (string, error) {
	ctx, cancel := context.WithTimeout(parent, limit)
	defer cancel()
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		if err := durableContextError(parent, ctx, limit); err != nil {
			return "", err
		}
		id, ready, err := check(ctx)
		if expired := durableContextError(parent, ctx, limit); expired != nil {
			return "", expired
		}
		if err != nil {
			return "", err
		}
		if ready {
			return id, nil
		}
		select {
		case <-ctx.Done():
		case <-tick.C:
		}
	}
}

func durableContextError(parent, ctx context.Context, limit time.Duration) error {
	if ctx.Err() == nil {
		return nil
	}
	if parent.Err() != nil {
		return fmt.Errorf("waiting for durable snapshot: %w", parent.Err())
	}
	return fmt.Errorf("snapshot wasn't durable within %v: %w", limit, ctx.Err())
}
