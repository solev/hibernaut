//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/termsafe"
)

// TestSnapshotCleanup checks that once a machine is removed, hibd deletes its snapshots from the
// host's disk, and keeps what the other machines need: they and the image still start and
// resume.
func TestSnapshotCleanup(t *testing.T) {
	ctx := testContext(t, 15*time.Minute)
	other := newHibMachine(t, ctx)
	hib(t, ctx, "pause", other)
	otherID := machineID(t, ctx, other)
	var kept []string
	waitFor(t, ctx, "E2B to write the retained machine's snapshot", time.Minute, func() bool {
		var done bool
		kept, done = snapshotBuilds(t, ctx, otherID)
		dirs, _ := buildDirs(t, ctx, kept)
		return done && len(kept) > 0 && dirs == len(kept)
	})
	name := newHibMachine(t, ctx)
	created := time.Now()
	id := machineID(t, ctx, name)
	hib(t, ctx, "pause", name)
	sshMachine(t, ctx, name, "true")
	hib(t, ctx, "pause", name)
	var builds []string
	waitFor(t, ctx, "E2B to write the machine's two snapshots", time.Minute, func() bool {
		var done bool
		builds, done = snapshotBuilds(t, ctx, id)
		dirs, _ := buildDirs(t, ctx, builds)
		return done && len(builds) >= 2 && dirs == len(builds)
	})

	time.Sleep(time.Until(created.Add(20 * time.Second)))
	hib(t, ctx, "rm", name)
	waitFor(t, ctx, "hibd to delete the removed machine's snapshots", 2*time.Minute, func() bool {
		dirs, _ := buildDirs(t, ctx, builds)
		return dirs == 0
	})
	if dirs, _ := buildDirs(t, ctx, kept); dirs != len(kept) {
		t.Errorf("cleanup kept %d of the other machine's %d snapshot directories", dirs, len(kept))
	}

	if out := sshMachine(t, ctx, other, "echo woke"); strings.TrimSpace(out) != "woke" {
		t.Errorf("ssh to paused %s after the cleanup printed %q, want woke", other, out)
	}
	survivesPause(t, ctx, newHibMachine(t, ctx))
}

// TestPauseCycles measures snapshot sources and cleanup retention across pauses and resumes.
func TestPauseCycles(t *testing.T) {
	options, err := pauseCycleOptionsFromEnv(os.LookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("HIB_E2E_SLOW") == "" {
		t.Skip("takes about 10 minutes by default. run make e2e-slow")
	}
	ctx := testContext(t, options.timeout())
	name := newHibMachine(t, ctx)
	id := machineID(t, ctx, name)
	sshMachine(t, ctx, name, "tmux new -d -s keep 'sleep 100000'")
	var cycles []pauseCycle
	var latest string
	for i := 1; i <= options.Cycles; i++ {
		if command := options.churnCommand(); command != "" {
			sshMachine(t, ctx, name, command)
		}
		cycle := pauseCycle{Cycle: i}
		start := time.Now()
		hib(t, ctx, "pause", name)
		cycle.Pause = time.Since(start)
		start = time.Now()
		latest = waitForDurableSnapshot(t, ctx, id, latest)
		cycle.Durable = time.Since(start)
		start = time.Now()
		cleanUp(t, ctx)
		cycle.Cleanup = time.Since(start)
		builds, done := snapshotBuilds(t, ctx, id)
		if !done {
			t.Fatal("E2B is still writing the machine's snapshot after cleanup")
		}
		cycle.Snapshot = snapshotStats(t, ctx, builds)
		start = time.Now()
		sshMachine(t, ctx, name, "tmux has-session -t keep")
		cycle.Resume = time.Since(start)
		cycles = append(cycles, cycle)
		t.Log(formatPauseCycle(cycle))
	}
	t.Log(pauseCycleSummary(options, cycles))
}

func cleanUp(t *testing.T, ctx context.Context) {
	t.Helper()
	waitFor(t, ctx, "hibd cleanup to run", time.Minute, func() bool {
		_, err := vm.ssh(ctx, "sudo hibd cleanup")
		if err != nil && !strings.Contains(err.Error(), "E2B is writing a build") {
			t.Fatalf("hibd cleanup: %s", termsafe.String(err.Error()))
		}
		return err == nil
	})
}

func snapshotBuilds(t *testing.T, ctx context.Context, id string) (builds []string, done bool) {
	t.Helper()
	out, err := readSnapshotListing(ctx, id)
	if err != nil {
		t.Fatal(termsafe.String(err.Error()))
	}
	builds, done, err = parseSnapshotBuilds(out)
	if err != nil {
		t.Fatal(err)
	}
	return builds, done
}

func readSnapshotListing(ctx context.Context, id string) (string, error) {
	query, err := snapshotQuery(id)
	if err != nil {
		return "", err
	}
	out, err := vm.ssh(ctx, `sudo docker exec "$(sudo docker ps -q `+
		`--filter label=com.docker.compose.project=local-dev --filter label=com.docker.compose.service=postgres)" `+
		`psql -qAt -U postgres -d postgres -c "`+query+`"`)
	if err != nil {
		return "", fmt.Errorf("reading snapshot builds: %w", err)
	}
	return out, nil
}

func waitForDurableSnapshot(t *testing.T, ctx context.Context, id, previous string) string {
	t.Helper()
	latest, err := pollDurable(ctx, 5*time.Minute, time.Second, func(ctx context.Context) (string, bool, error) {
		out, err := readSnapshotListing(ctx, id)
		if err != nil {
			return "", false, err
		}
		latest, ready, err := latestDurableBuild(out, previous)
		if err != nil || !ready {
			return latest, false, err
		}
		command, err := durableFilesCommand(latest)
		if err != nil {
			return "", false, err
		}
		out, err = vm.ssh(ctx, command)
		if err != nil {
			return "", false, fmt.Errorf("checking durable snapshot files: %w", err)
		}
		ready, err = parseDurableFiles(out)
		return latest, ready, err
	})
	if err != nil {
		t.Fatal(termsafe.String(err.Error()))
	}
	return latest
}

func snapshotStats(t *testing.T, ctx context.Context, builds []string) snapshotMetrics {
	t.Helper()
	command, err := snapshotReadCommand(builds)
	if err != nil {
		t.Fatal(err)
	}
	out, err := vm.ssh(ctx, command)
	if err != nil {
		t.Fatalf("reading snapshot files: %s", termsafe.String(err.Error()))
	}
	files, err := parseSnapshotFiles(out, builds)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := measureSnapshot(builds[len(builds)-1], files)
	if err != nil {
		t.Fatal(err)
	}
	return stats
}

func buildDirs(t *testing.T, ctx context.Context, builds []string) (dirs int, used int64) {
	t.Helper()
	command, err := buildDirsCommand(builds)
	if err != nil {
		t.Fatal(err)
	}
	if command == "" {
		return 0, 0
	}
	out, err := vm.ssh(ctx, command)
	if err != nil {
		t.Fatalf("reading snapshot directories: %s", termsafe.String(err.Error()))
	}
	bytes, err := parseBuildDirs(out, builds)
	if err != nil {
		t.Fatal(err)
	}
	usage, err := diskUsage(bytes)
	if err != nil {
		t.Fatal(err)
	}
	return usage.Builds, usage.Bytes
}
