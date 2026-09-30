package lib

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const fakeHibd = `#!/bin/sh
echo "hibd $*" >>"$CALLS"
exit "${PAUSE_EXIT:-0}"
`

const fakeSystemctl = `#!/bin/sh
echo "systemctl $*" >>"$CALLS"
case "$1" in
is-active) exit "${ACTIVE_EXIT:-0}" ;;
stop) exit "${STOP_EXIT:-0}" ;;
esac
exit 2
`

const fakeSync = `#!/bin/sh
echo sync >>"$CALLS"
`

func TestQuiesce(t *testing.T) {
	const (
		isActive = "systemctl is-active -q e2b-orchestrator.service"
		pauseAll = "hibd pause-all --timeout 4m"
		stop     = "systemctl stop e2b-infra.service"
		sync     = "sync"
	)
	for _, tc := range []struct {
		name      string
		env       []string
		wantOK    bool
		wantCalls []string
	}{
		{"pauses every machine, stops E2B, then flushes", nil, true, []string{isActive, pauseAll, stop, sync}},
		{"only flushes while E2B is stopped", []string{"ACTIVE_EXIT=3"}, true, []string{isActive, sync}},
		{"keeps E2B running when pausing fails", []string{"PAUSE_EXIT=1"}, false, []string{isActive, pauseAll}},
		{"fails when E2B doesn't stop", []string{"STOP_EXIT=1"}, false, []string{isActive, pauseAll, stop}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, script := range map[string]string{"hibd": fakeHibd, "systemctl": fakeSystemctl, "sync": fakeSync} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			calls := filepath.Join(dir, "calls")
			cmd := exec.Command("bash", "quiesce")
			cmd.Env = append([]string{
				"PATH=" + os.Getenv("PATH"),
				"HIBD=" + filepath.Join(dir, "hibd"),
				"SYSTEMCTL=" + filepath.Join(dir, "systemctl"),
				"SYNC=" + filepath.Join(dir, "sync"),
				"CALLS=" + calls,
			}, tc.env...)
			out, err := cmd.CombinedOutput()
			t.Logf("output: %s", out)
			if ok := err == nil; ok != tc.wantOK {
				t.Errorf("succeeded = %v (%v), want %v", ok, err, tc.wantOK)
			}
			data, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Split(strings.TrimSpace(string(data)), "\n"); !slices.Equal(got, tc.wantCalls) {
				t.Errorf("calls %q, want %q", got, tc.wantCalls)
			}
		})
	}
}
