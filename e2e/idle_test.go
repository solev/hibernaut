//go:build e2e

package e2e

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
)

// Commands that keep a machine busy after the ssh session that starts them ends. setsid detaches
// them from it. The agent is sleep named codex, so nothing else in the machine has codex in its
// command line.
const (
	cpuLoop   = `setsid -f sh -c 'while :; do :; done' </dev/null >/dev/null 2>&1`
	fakeAgent = `setsid -f bash -c 'exec -a codex sleep 6000' </dev/null >/dev/null 2>&1`
)

// TestIdlePauseWake is the milestone's main flow: a machine left idle pauses itself once its
// idle time is up, and connecting wakes it with its processes still running.
func TestIdlePauseWake(t *testing.T) {
	ctx := testContext(t, 15*time.Minute)
	name := newHibMachine(t, ctx, "--idle", "1m")
	if row := nameRow(hib(t, ctx, "ls"), name); !strings.Contains(row, " 1m ") {
		t.Errorf("hib ls row for %s = %q, want idle time 1m", name, row)
	}

	// A counter in tmux ticks once a second. Then the ssh session ends, and nothing keeps the
	// machine busy.
	pid := strings.TrimSpace(sshMachine(t, ctx, name, `tmux new -d -s count `+
		`'i=0; while :; do i=$((i+1)); echo $i >/tmp/count; sleep 1; done' && tmux list-panes -t count -F '#{pane_pid}'`))
	left := time.Now()
	id := machineID(t, ctx, name)
	waitFor(t, ctx, "the idle machine to pause itself", 4*time.Minute, func() bool {
		m, err := vm.engine.Get(ctx, id)
		return err == nil && m.State == engine.Paused
	})
	if idle := time.Since(left); idle < 50*time.Second {
		t.Errorf("the machine paused %v after the ssh session ended, before its idle time of 1m", idle.Round(time.Second))
	}
	if row := nameRow(hib(t, ctx, "ls"), name); !strings.Contains(row, "paused") {
		t.Errorf("hib ls row for %s = %q, want it paused", name, row)
	}

	start := time.Now()
	got := strings.TrimSpace(sshMachine(t, ctx, name, `tmux list-panes -t count -F '#{pane_pid}'`))
	t.Logf("resume on ssh took %v", time.Since(start).Round(time.Millisecond))
	if got != pid {
		t.Errorf("after the wake the counter's shell is PID %q, want %q", got, pid)
	}
	counts := strings.Fields(sshMachine(t, ctx, name, "cat /tmp/count; sleep 3; cat /tmp/count"))
	if len(counts) != 2 {
		t.Fatalf("reading the counter twice gave %q", counts)
	}
	before, err1 := strconv.Atoi(counts[0])
	after, err2 := strconv.Atoi(counts[1])
	if err1 != nil || err2 != nil || after <= before {
		t.Errorf("the counter read %q, then %q 3 seconds later; want it counting", counts[0], counts[1])
	}
}

// TestBusyStaysAwake checks that each busy signal alone keeps a machine with a 1-minute idle time
// running: an open ssh session, a CPU loop, and a coding agent.
func TestBusyStaysAwake(t *testing.T) {
	ctx := testContext(t, 15*time.Minute)
	busy := []struct{ why, name, id string }{{why: "an open ssh session"}, {why: "a CPU loop"}, {why: "a coding agent"}}
	for i := range busy {
		busy[i].name = newHibMachine(t, ctx, "--idle", "1m")
		busy[i].id = machineID(t, ctx, busy[i].name)
	}
	session := sshMachineCmd(ctx, nil, busy[0].name, "sleep 600")
	if err := session.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		session.Process.Kill()
		session.Wait()
	})
	sshMachine(t, ctx, busy[1].name, cpuLoop)
	sshMachine(t, ctx, busy[2].name, fakeAgent)

	start := time.Now()
	for time.Since(start) < 3*time.Minute {
		for _, b := range busy {
			if m, err := vm.engine.Get(ctx, b.id); err != nil || m.State != engine.Running {
				t.Fatalf("%s, kept busy by %s, is %s after %v (%v)", b.name, b.why, m.State, time.Since(start).Round(time.Second), err)
			}
		}
		sleepCtx(t, ctx, 10*time.Second)
	}

	// hibd's daemon says why it kept the CPU loop's and the agent's machines running.
	log := mustSSH(t, ctx, "journalctl -u hibd --no-pager -o cat --since=-20min")
	for _, want := range []string{busy[1].name + " is busy: using", busy[2].name + " is busy: a coding agent is running"} {
		if !strings.Contains(log, want) {
			t.Errorf("hibd's log doesn't say %q:\n%s", want, log)
		}
	}
}

// TestBusyPastAnHour checks that a busy machine keeps running, without a pause, past E2B's default
// 1-hour cap, which install-e2b.sh lifts.
func TestBusyPastAnHour(t *testing.T) {
	if os.Getenv("HIB_E2E_SLOW") == "" {
		t.Skip("takes over an hour; run make e2e-slow")
	}
	ctx := testContext(t, 80*time.Minute)
	name := newHibMachine(t, ctx, "--idle", "1m")
	sshMachine(t, ctx, name, fakeAgent)
	id := machineID(t, ctx, name)
	first, err := vm.engine.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	for time.Since(first.StartedAt) < 65*time.Minute {
		m, err := vm.engine.Get(ctx, id)
		if err != nil || m.State != engine.Running || !m.StartedAt.Equal(first.StartedAt) {
			t.Fatalf("%v after it started, the machine is %s, started at %v (first %v): %v",
				time.Since(first.StartedAt).Round(time.Second), m.State, m.StartedAt, first.StartedAt, err)
		}
		sleepCtx(t, ctx, 30*time.Second)
	}
}

// sleepCtx sleeps for d, and fails the test if ctx ends first.
func sleepCtx(t *testing.T, ctx context.Context, d time.Duration) {
	t.Helper()
	select {
	case <-ctx.Done():
		t.Fatalf("sleeping: %v", ctx.Err())
	case <-time.After(d):
	}
}
