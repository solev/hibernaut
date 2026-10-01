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

// startCounter starts a process that counts seconds into /tmp/counter, outliving Exec, and
// prints its PID.
const startCounter = `setsid nohup sh -c 'i=0; while :; do i=$((i+1)); echo $i > /tmp/counter; sleep 1; done' </dev/null >/dev/null 2>&1 & echo $!`

// TestSurvivesHostRestart is spike S4: a machine that's running when the VM goes down resumes
// afterwards with its files and processes, and E2B comes back up on its own.
func TestSurvivesHostRestart(t *testing.T) {
	if os.Getenv("HIB_E2E_SLOW") == "" {
		t.Skip("restarts the VM; run make e2e-slow")
	}
	// A clean shutdown, as for Azure maintenance: the pause-all guard pauses machines while
	// systemd stops E2B.
	t.Run("reboot", func(t *testing.T) { survives(t, reboot) })
	// The nightly timer's path, which idle-deallocate shares: quiesce, then deallocate.
	t.Run("nightly-deallocate", func(t *testing.T) { survives(t, nightlyDeallocate) })
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
	// Without --start, wait-e2b.sh starts nothing.
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

// reboot restarts the VM cleanly.
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

// nightlyDeallocate runs the nightly unit, which deallocates the VM, then starts the VM without
// deploying or starting anything else.
func nightlyDeallocate(t *testing.T, ctx context.Context) {
	deallocate(t, ctx)
	if out, err := vm.azure(ctx, "start.sh"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// deallocate runs the nightly unit, which pauses the machines and deallocates the VM, and waits
// until the VM is asleep. It leaves the VM deallocated.
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

// TestWakeOnSSH is the M1 headline: once the VM has deallocated itself, ssh NAME.hib wakes it
// through hib's bridge and connects, resuming the machine with its files intact.
func TestWakeOnSSH(t *testing.T) {
	if os.Getenv("HIB_E2E_SLOW") == "" {
		t.Skip("deallocates the VM; run make e2e-slow")
	}
	ctx := testContext(t, 25*time.Minute)
	if vm.engine == nil {
		if err := vm.openTunnel(ctx); err != nil {
			t.Fatal(err)
		}
	}
	name := newHibMachine(t, ctx)
	sshMachine(t, ctx, name, "echo woke > marker")

	// Deallocate the VM, as the idle timer does, and leave it asleep. The harness tunnel goes
	// with it; hib's bridge reaches the host over its public IP, so it still works.
	vm.closeTunnel()
	deallocate(t, ctx)

	// A plain ssh wakes the host through hib and connects; the machine resumes with its marker.
	start := time.Now()
	got := sshMachine(t, ctx, name, "cat marker")
	t.Logf("ssh woke the VM and connected in %v", time.Since(start).Round(time.Second))
	if strings.TrimSpace(got) != "woke" {
		t.Errorf("marker after waking = %q, want %q", got, "woke")
	}

	// Reopen the tunnel so the machine's cleanup can reach the engine.
	if err := vm.openTunnel(ctx); err != nil {
		t.Fatal(err)
	}
}
