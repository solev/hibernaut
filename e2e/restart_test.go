//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
)

const startCounter = `setsid nohup sh -c 'i=0; while :; do i=$((i+1)); echo $i > /tmp/counter; sleep 1; done' </dev/null >/dev/null 2>&1 & echo $!`

// TestSurvivesHostRestart checks that a machine that's running when the VM goes down resumes
// afterwards with its files and processes, and E2B comes back up on its own.
func TestSurvivesHostRestart(t *testing.T) {
	if os.Getenv("HIB_E2E_SLOW") == "" {
		t.Skip("restarts the VM; run make e2e-slow")
	}

	t.Run("reboot", func(t *testing.T) { survives(t, reboot) })
}

func survives(t *testing.T, restart func(t *testing.T, ctx context.Context)) {
	ctx := testContext(t, 20*time.Minute)
	if vm.engine == nil {
		if err := vm.openTunnel(ctx); err != nil {
			t.Fatal(err)
		}
	}
	m := newMachine(t, ctx)
	sh(t, ctx, m.ID, "echo survived > marker")
	pid := strings.TrimSpace(sh(t, ctx, m.ID, startCounter))
	time.Sleep(2 * time.Second)
	before := counter(t, ctx, m.ID)

	vm.closeTunnel()
	restart(t, ctx)

	if out, err := vm.ssh(ctx, "hibernaut/vm/wait-e2b.sh 2>&1"); err != nil {
		t.Fatalf("E2B didn't come up on its own: %v\n%s", err, out)
	}
	if err := vm.openTunnel(ctx); err != nil {
		t.Fatal(err)
	}

	paused := mustSSH(t, ctx, "journalctl -b -1 --no-pager -o short-iso -u hibd-pause-all -u hibernaut-nightly")
	t.Logf("pause-all guard and nightly unit, previous boot:\n%s", paused)
	t.Logf("end of the previous boot:\n%s", mustSSH(t, ctx, "journalctl -b -1 --no-pager -o short-iso -n 30"))
	if !strings.Contains(paused, "paused "+m.ID) {
		t.Errorf("neither the pause-all guard nor the nightly unit logged pausing %s", m.ID)
	}

	got, err := vm.engine.Get(ctx, m.ID)
	if err != nil || got.State != engine.Paused {
		t.Fatalf("after the restart: Get = %+v, %v; want paused", got, err)
	}
	start := time.Now()
	if _, err := vm.engine.Resume(ctx, m.ID, 10*time.Minute); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	t.Logf("resumed in %v", time.Since(start).Round(time.Millisecond))

	if got := sh(t, ctx, m.ID, "cat marker"); got != "survived\n" {
		t.Errorf("marker = %q, want %q", got, "survived\n")
	}
	sh(t, ctx, m.ID, fmt.Sprintf(`kill -0 %s || { echo "the counter process (%s) is gone" >&2; exit 1; }`, pid, pid))
	after := counter(t, ctx, m.ID)
	if after < before {
		t.Errorf("counter went from %d to %d, want it to carry on", before, after)
	}
	time.Sleep(2 * time.Second)
	if later := counter(t, ctx, m.ID); later <= after {
		t.Errorf("counter stayed at %d after resuming, want it counting", later)
	}
}

func counter(t *testing.T, ctx context.Context, id string) int {
	t.Helper()
	out := strings.TrimSpace(sh(t, ctx, id, "cat /tmp/counter"))
	n, err := strconv.Atoi(out)
	if err != nil {
		t.Fatalf("counter = %q: %v", out, err)
	}
	return n
}

func reboot(t *testing.T, ctx context.Context) {
	bootID := mustSSH(t, ctx, "cat /proc/sys/kernel/random/boot_id")
	// Returns once the reboot is queued, though ssh may lose the connection first.
	if _, err := vm.ssh(ctx, "sudo systemctl reboot"); err != nil {
		t.Logf("reboot: %v", err)
	}
	waitFor(t, ctx, "the VM to reboot", 10*time.Minute, func() bool {
		id, err := vm.ssh(ctx, "cat /proc/sys/kernel/random/boot_id")
		return err == nil && id != bootID
	})
}

func deallocate(t *testing.T, ctx context.Context) {
	mustSSH(t, ctx, "sudo systemctl start --no-block hibernaut-nightly.service")
	waitFor(t, ctx, "the VM to deallocate itself", 10*time.Minute, func() bool {
		if state, err := vm.azure(ctx, "state.sh"); err == nil && state == "VM deallocated" {
			return true
		}
		if _, err := vm.ssh(ctx, "systemctl is-failed -q hibernaut-nightly.service"); err == nil {
			log, _ := vm.ssh(ctx, "journalctl -b --no-pager -u hibernaut-nightly")
			t.Fatalf("the nightly unit failed:\n%s", log)
		}
		return false
	})
}

// TestWakeOnSSH checks that once the VM has deallocated itself, ssh NAME.hib wakes it
// through hib's bridge and connects, resuming the machine with its files and processes intact.
func TestWakeOnSSH(t *testing.T) {
	if os.Getenv("HIB_E2E_SLOW") == "" {
		t.Skip("deallocates the VM; run make e2e-slow")
	}
	t.Run("nightly-deallocate", func(t *testing.T) {
		ctx := testContext(t, 25*time.Minute)
		if vm.engine == nil {
			if err := vm.openTunnel(ctx); err != nil {
				t.Fatal(err)
			}
		}
		name := newHibMachine(t, ctx)
		sshMachine(t, ctx, name, "echo woke > marker")
		id := machineID(t, ctx, name)
		pid := strings.TrimSpace(sh(t, ctx, id, startCounter))
		waitFor(t, ctx, "the counter to write its first value", time.Minute, func() bool {
			res, err := vm.engine.Exec(ctx, id, engine.ExecRequest{Cmd: "test", Args: []string{"-s", "/tmp/counter"}})
			return err == nil && res.ExitCode == 0
		})
		before := counter(t, ctx, id)

		vm.closeTunnel()
		deallocate(t, ctx)

		start := time.Now()
		got := sshMachine(t, ctx, name, "cat marker")
		t.Logf("ssh woke the VM and connected in %v", time.Since(start).Round(time.Second))
		if strings.TrimSpace(got) != "woke" {
			t.Errorf("marker after waking = %q, want %q", got, "woke")
		}

		if out, err := vm.ssh(ctx, "hibernaut/vm/wait-e2b.sh 2>&1"); err != nil {
			t.Fatalf("E2B didn't come up on its own: %v\n%s", err, out)
		}
		if err := vm.openTunnel(ctx); err != nil {
			t.Fatal(err)
		}
		paused := mustSSH(t, ctx, "journalctl -b -1 --no-pager -o short-iso -u hibd-pause-all -u hibernaut-nightly")
		t.Logf("pause-all guard and nightly unit, previous boot:\n%s", paused)
		t.Logf("end of the previous boot:\n%s", mustSSH(t, ctx, "journalctl -b -1 --no-pager -o short-iso -n 30"))
		if !strings.Contains(paused, "paused "+id) {
			t.Errorf("neither the pause-all guard nor the nightly unit logged pausing %s", id)
		}
		res, err := vm.engine.Exec(ctx, id, engine.ExecRequest{Cmd: "kill", Args: []string{"-0", pid}})
		if err != nil || res.ExitCode != 0 {
			t.Fatal("the counter process didn't survive the nightly deallocation")
		}
		after := counter(t, ctx, id)
		if after < before {
			t.Errorf("counter went from %d to %d, want it to carry on", before, after)
		}
		waitFor(t, ctx, "the resumed counter to advance", time.Minute, func() bool {
			return counter(t, ctx, id) > after
		})
	})
}
