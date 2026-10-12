//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/cleanup"
	"github.com/solev/hibernaut/internal/engine"
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
	hib(t, ctx, "rm", name, "--yes")
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
	sums := writeSentinels(t, ctx, name)
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
		hibdCleanup(t, ctx)
		cycle.Cleanup = time.Since(start)
		builds, done := snapshotBuilds(t, ctx, id)
		if !done {
			t.Fatal("E2B is still writing the machine's snapshot after cleanup")
		}
		cycle.Snapshot = snapshotStats(t, ctx, builds)
		start = time.Now()
		sshMachine(t, ctx, name, "tmux has-session -t keep")
		cycle.Resume = time.Since(start)
		checkSentinels(t, ctx, name, sums)
		cycles = append(cycles, cycle)
		t.Log(formatPauseCycle(cycle))
	}
	t.Log(pauseCycleSummary(options, cycles))
}

// TestTrim checks that hibd trims the old snapshots of a machine that paused many times, and
// keeps what its latest snapshot and a saved image read: it resumes cold with its memory and disk
// intact, and so do its image, a fork and a machine made from the image.
func TestTrim(t *testing.T) {
	if os.Getenv("HIB_E2E_SLOW") == "" {
		t.Skip("waits over an hour for the snapshots to age. run make e2e-slow")
	}
	imageA := fmt.Sprintf("e2e-image-%08x", rand.Uint32())
	imageB := fmt.Sprintf("e2e-image-%08x", rand.Uint32())
	var fork string
	t.Cleanup(func() {
		fresh, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if fork != "" {
			runHib(fresh, "rm", fork, "--yes")
		}
		for _, image := range []string{imageA, imageB} {
			if _, stderr, code := runHib(fresh, "image", "rm", image); code != 0 && vm.engine != nil {
				if _, err := vm.engine.Template(fresh, "hibimg-"+image); err == nil {
					t.Errorf("cleanup: image %s remains: %s", image, termsafe.String(stderr))
				}
			}
		}
	})

	ctx := testContext(t, 45*time.Minute)
	b := newHibMachine(t, ctx)
	idB := machineID(t, ctx, b)
	hib(t, ctx, "pause", b)
	sshMachine(t, ctx, b, "true")
	hib(t, ctx, "pause", b)
	hib(t, ctx, "image", "save", b, imageB)

	a := newHibMachine(t, ctx)
	idA := machineID(t, ctx, a)
	sums := writeSentinels(t, ctx, a)
	churn := pauseCycleOptions{Churn: "64M", Workload: "random"}.churnCommand()
	var latest string
	for i := 1; i <= 10; i++ {
		sshMachine(t, ctx, a, churn)
		hib(t, ctx, "pause", a)
		latest = waitForDurableSnapshot(t, ctx, idA, latest)
		if i < 10 {
			sshMachine(t, ctx, a, "true")
			checkSentinels(t, ctx, a, sums)
		}
	}
	lastPause := time.Now()

	var subjects trimSubjects
	var recorded []string
	var before storageRecord
	waitFor(t, ctx, "every file of A's and B's snapshots", 5*time.Minute, func() bool {
		var doneA, doneB, doneImage bool
		var image []string
		subjects.A, doneA = snapshotBuilds(t, ctx, idA)
		subjects.B, doneB = snapshotBuilds(t, ctx, idB)
		image, doneImage = imageBuilds(t, ctx, imageB)
		if !doneA || !doneB || !doneImage || len(subjects.A) == 0 || len(subjects.B) == 0 || len(image) == 0 {
			return false
		}
		subjects.Image = image[len(image)-1]
		recorded = distinctBuilds(subjects.A, subjects.B, []string{subjects.Image})
		var err error
		if before, err = readStorage(ctx, recorded); err != nil {
			t.Log(termsafe.String(err.Error()))
			return false
		}
		missing := before.missingFiles(recorded)
		for _, problem := range missing {
			t.Log(problem)
		}
		return len(missing) == 0
	})
	t.Logf("A's %d snapshot directories take %s. The image reads B's builds %v",
		len(subjects.A), diskBytes(before.disk(subjects.A)), slices.Sorted(maps.Keys(before.reads(subjects.Image, subjects.B))))

	// Go resumes a parallel test only once every sequential test has finished, and no other e2e
	// test is parallel. So the hour passes during the rest of the run, TestBusyPastAnHour included,
	// and the host restarts below disturb no other test.
	t.Parallel()

	setup := testContext(t, 5*time.Minute)
	t.Cleanup(func() {
		if vm.engine == nil {
			fresh, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if err := vm.openTunnel(fresh); err != nil {
				t.Logf("reopening the tunnel to clean up: %s", termsafe.String(err.Error()))
			}
		}
	})
	if vm.engine == nil {
		if err := vm.openTunnel(setup); err != nil {
			t.Fatal(termsafe.String(err.Error()))
		}
	}
	holdHostSession(t)

	giveUp := lastPause.Add(trimmableAfter + 20*time.Minute)
	waitTrimmable(t, testContext(t, max(time.Until(giveUp), 0)+10*time.Minute), subjects.A, before.disk(subjects.A), lastPause, giveUp)

	ctx = testContext(t, 10*time.Minute)
	out := hibdCleanup(t, ctx)
	t.Logf("hibd cleanup:\n%s", termsafe.String(out))
	if strings.Contains(out, "not trimming") {
		t.Error("hibd cleanup didn't trim")
	}
	after, err := readStorage(ctx, recorded)
	if err != nil {
		t.Fatal(termsafe.String(err.Error()))
	}
	limit, err := trimLimitFor(before, after, subjects.A)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("A's snapshot directories took %s after the pauses and %s after the trim, at most %s",
		diskBytes(before.disk(subjects.A)), diskBytes(after.disk(subjects.A)), limit)
	problems, err := trimProblems(before, after, subjects)
	if err != nil {
		t.Fatal(err)
	}
	for _, problem := range problems {
		t.Error(problem)
	}

	ctx = testContext(t, 30*time.Minute)
	restartHost(t, ctx, reboot)
	sshMachine(t, ctx, a, "true")
	checkSentinels(t, ctx, a, sums)

	hib(t, ctx, "pause", a)
	waitForDurableSnapshot(t, ctx, idA, latest)
	restartHost(t, ctx, reboot)
	sshMachine(t, ctx, a, "true")
	checkSentinels(t, ctx, a, sums)

	ctx = testContext(t, 30*time.Minute)
	hib(t, ctx, "image", "save", a, imageA)
	fork = a + "-1"
	hib(t, ctx, "fork", a)
	checkSentinels(t, ctx, fork, sums)
	checkSentinels(t, ctx, newHibMachine(t, ctx, "-image", imageA), sums)
}

const trimmableAfter = max(cleanup.MinAge, cleanup.SelectionGrace)

func waitTrimmable(t *testing.T, ctx context.Context, a []string, before int64, lastPause, giveUp time.Time) {
	t.Helper()
	old := a[:len(a)-1]
	aged := lastPause.Add(trimmableAfter + time.Minute)
	for {
		out := hibdCleanup(t, ctx, "-n")
		t.Logf("hibd cleanup -n:\n%s", termsafe.String(out))
		if time.Now().After(aged) {
			if wouldTrimAny(out, old) {
				return
			}
			if _, now := buildDirs(t, ctx, a); 2*now < before {
				t.Logf("A's snapshot directories already take %s, down from %s", diskBytes(now), diskBytes(before))
				return
			}
		}
		if time.Now().After(giveUp) {
			t.Fatalf("hibd cleanup -n would trim none of A's old builds %v after its last pause. It printed:\n%s",
				time.Since(lastPause).Round(time.Second), termsafe.String(out))
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for A's old builds to become trim candidates: %v", ctx.Err())
		case <-time.After(3 * time.Minute):
		}
	}
}

func writeSentinels(t *testing.T, ctx context.Context, name string) sentinelSums {
	t.Helper()
	sums, err := parseSentinelSums(sshMachine(t, ctx, name, writeSentinelsCommand))
	if err != nil {
		t.Fatalf("writing the sentinels in %s: %v", name, err)
	}
	return sums
}

func checkSentinels(t *testing.T, ctx context.Context, name string, want sentinelSums) {
	t.Helper()
	res, err := vm.engine.Exec(ctx, machineID(t, ctx, name), engine.ExecRequest{
		Cmd: "sh", Args: []string{"-c", readSentinelsCommand}, User: "root",
	})
	checkSentinelResult(t, name, res, err, want)
}

func readStorage(ctx context.Context, builds []string) (storageRecord, error) {
	command, err := storageRecordCommand(builds)
	if err != nil {
		return storageRecord{}, err
	}
	out, err := vm.ssh(ctx, command)
	if err != nil {
		return storageRecord{}, fmt.Errorf("reading snapshot files: %w", err)
	}
	return parseStorageRecord(out, builds)
}

func holdHostSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			_ = exec.CommandContext(ctx, "ssh", append(slices.Clone(sshOptions), vm.target, "while :; do sleep 60; echo; done")...).Run()
			select {
			case <-ctx.Done():
			case <-time.After(10 * time.Second):
			}
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func hibdCleanup(t *testing.T, ctx context.Context, flags ...string) string {
	t.Helper()
	command := strings.Join(append([]string{"sudo hibd cleanup"}, flags...), " ") + " 2>&1"
	var out string
	waitFor(t, ctx, "hibd cleanup to run", time.Minute, func() bool {
		var err error
		out, err = vm.ssh(ctx, command)
		if err != nil && !strings.Contains(out, "E2B is writing a build") {
			t.Fatalf("hibd cleanup: %s\n%s", termsafe.String(err.Error()), termsafe.String(out))
		}
		return err == nil
	})
	return out
}

func snapshotBuilds(t *testing.T, ctx context.Context, id string) (builds []string, done bool) {
	t.Helper()
	query, err := snapshotQuery(id)
	if err != nil {
		t.Fatal(err)
	}
	return listedBuilds(t, ctx, query)
}

func imageBuilds(t *testing.T, ctx context.Context, image string) (builds []string, done bool) {
	t.Helper()
	query, err := templateQuery("hibimg-" + image)
	if err != nil {
		t.Fatal(err)
	}
	return listedBuilds(t, ctx, query)
}

func listedBuilds(t *testing.T, ctx context.Context, query string) (builds []string, done bool) {
	t.Helper()
	out, err := readBuildListing(ctx, query)
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
	return readBuildListing(ctx, query)
}

func readBuildListing(ctx context.Context, query string) (string, error) {
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
