//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/cleanup"
	"github.com/solev/hibernaut/internal/hibapi"
)

// TestSnapshotCleanup checks that once a machine is removed, hibd deletes its snapshots from the
// host's disk, and keeps what the other machines need: they and the image still start and
// resume.
func TestSnapshotCleanup(t *testing.T) {
	ctx := testContext(t, 15*time.Minute)
	other := newHibMachine(t, ctx)
	hib(t, ctx, "pause", other)
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

	// hibd cleans up at once only after a machine it saw is removed, and it looks every 15 s.
	time.Sleep(time.Until(created.Add(20 * time.Second)))
	hib(t, ctx, "rm", name)
	waitFor(t, ctx, "hibd to delete the removed machine's snapshots", 2*time.Minute, func() bool {
		dirs, _ := buildDirs(t, ctx, builds)
		return dirs == 0
	})
	log := mustSSH(t, ctx, "journalctl -u hibd --no-pager -o cat --since=-15min")
	for _, b := range builds {
		if !strings.Contains(log, "cleanup: deleted "+b) {
			t.Errorf("hibd's log doesn't say it deleted %s:\n%s", b, log)
		}
	}

	if out := sshMachine(t, ctx, other, "echo woke"); strings.TrimSpace(out) != "woke" {
		t.Errorf("ssh to paused %s after the cleanup printed %q, want woke", other, out)
	}
	survivesPause(t, ctx, newHibMachine(t, ctx))
}

// TestPauseCycles pauses and resumes one machine 20 times, cleaning up after each pause, and
// logs how long each resume takes, the disk its snapshots use, and how many of them its latest
// snapshot needs. E2B never compacts a machine's chain of snapshot diffs, so if the resume time
// or what the latest snapshot needs keeps growing, compaction needs planning. Cleanup keeps
// every snapshot for an hour, so the disk used grows anyway.
func TestPauseCycles(t *testing.T) {
	if os.Getenv("HIB_E2E_SLOW") == "" {
		t.Skip("takes about 10 minutes; run make e2e-slow")
	}
	ctx := testContext(t, 40*time.Minute)
	name := newHibMachine(t, ctx)
	id := machineID(t, ctx, name)
	sshMachine(t, ctx, name, "tmux new -d -s keep 'sleep 100000'")
	for i := 1; i <= 20; i++ {
		// The machine changes some memory and disk each time, as it would in use.
		sshMachine(t, ctx, name, "head -c 64M /dev/urandom > /home/user/churn")
		hib(t, ctx, "pause", name)
		cleanUp(t, ctx)
		builds, _ := snapshotBuilds(t, ctx, id)
		dirs, used := buildDirs(t, ctx, builds)
		needs := latestNeeds(t, ctx, builds)
		_, needed := buildDirs(t, ctx, needs)
		start := time.Now()
		sshMachine(t, ctx, name, "tmux has-session -t keep")
		t.Logf("cycle %2d: resumed in %v; its snapshots use %s in %d build directories; the latest needs %d of them, %s",
			i, time.Since(start).Round(time.Millisecond), hibapi.FormatMB(used>>20), dirs, len(needs), hibapi.FormatMB(needed>>20))
	}
}

// cleanUp runs hibd cleanup on the host, again while E2B is still writing a build.
func cleanUp(t *testing.T, ctx context.Context) {
	t.Helper()
	waitFor(t, ctx, "hibd cleanup to run", time.Minute, func() bool {
		_, err := vm.ssh(ctx, "sudo hibd cleanup")
		if err != nil && !strings.Contains(err.Error(), "E2B is writing a build") {
			t.Fatal(err)
		}
		return err == nil
	})
}

// snapshotBuilds returns the builds of machine id's snapshots from E2B's database on the host,
// oldest first, and whether E2B is done writing them.
func snapshotBuilds(t *testing.T, ctx context.Context, id string) (builds []string, done bool) {
	t.Helper()
	query := fmt.Sprintf("SELECT b.id, b.status_group FROM snapshots s "+
		"JOIN env_build_assignments a ON a.env_id = s.env_id JOIN env_builds b ON b.id = a.build_id "+
		"WHERE s.sandbox_id = '%s' GROUP BY b.id, b.status_group ORDER BY max(a.created_at)", id)
	out := mustSSH(t, ctx, `sudo docker exec "$(sudo docker ps -q `+
		`--filter label=com.docker.compose.project=local-dev --filter label=com.docker.compose.service=postgres)" `+
		`psql -qAt -U postgres -d postgres -c "`+query+`"`)
	done = true
	for line := range strings.Lines(out) {
		build, status, _ := strings.Cut(strings.TrimSpace(line), "|")
		builds = append(builds, build)
		done = done && status != "pending" && status != "in_progress"
	}
	return builds, done
}

// latestNeeds returns which of a machine's snapshot builds, oldest first, its latest one needs:
// the ones its headers name.
func latestNeeds(t *testing.T, ctx context.Context, builds []string) []string {
	t.Helper()
	if len(builds) == 0 {
		t.Fatal("the machine has no snapshots")
	}
	out := mustSSH(t, ctx, "sudo sh -c 'cd /var/lib/e2b/local-template-storage/"+builds[len(builds)-1]+" && "+
		"for f in "+strings.Join(cleanup.Headers, " ")+"; do base64 -w0 $f && echo || exit 1; done'")
	named := map[string]bool{}
	for line := range strings.Lines(out) {
		b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(line))
		if err != nil {
			t.Fatal(err)
		}
		refs, err := cleanup.ParseHeader(b)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range refs {
			named[r] = true
		}
	}
	var needs []string
	for _, b := range builds {
		if named[b] {
			needs = append(needs, b)
		}
	}
	return needs
}

// buildDirs returns how many of builds still have a directory on the host, and the disk they use.
func buildDirs(t *testing.T, ctx context.Context, builds []string) (dirs int, used int64) {
	t.Helper()
	if len(builds) == 0 {
		return 0, 0
	}
	out := mustSSH(t, ctx, "sudo sh -c 'cd /var/lib/e2b/local-template-storage && "+
		"ls -d "+strings.Join(builds, " ")+" 2>/dev/null | xargs -r du -sB1'")
	for line := range strings.Lines(out) {
		n, err := strconv.ParseInt(strings.Fields(line)[0], 10, 64)
		if err != nil {
			t.Fatalf("du printed %q", line)
		}
		dirs++
		used += n
	}
	return dirs, used
}
