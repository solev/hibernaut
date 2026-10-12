//go:build linux

package hook

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeIptables writes an iptables that prints before until iptables-restore has run and after
// from then on, fails its failAt-th listing, and an iptables-restore that records each input.
func fakeIptables(t *testing.T, before, after string, failAt int) (iptables, count, restored string) {
	t.Helper()
	dir := t.TempDir()
	iptables = filepath.Join(dir, "iptables")
	count = filepath.Join(dir, "count")
	restored = filepath.Join(dir, "restored")
	list := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
n=0
if [[ -f %[1]q ]]; then n=$(cat %[1]q); fi
n=$((n + 1))
echo "$n" > %[1]q
if [[ "$*" != "-w 5 -t nat -S" ]]; then echo "unexpected: $*" >&2; exit 2; fi
if [[ "$n" == %[2]q ]]; then echo "xtables lock timed out" >&2; exit 4; fi
if [[ -f %[3]q ]]; then cat <<'END'
%[4]s
END
else cat <<'END'
%[5]s
END
fi
`, count, strconv.Itoa(failAt), restored, after, before)
	restore := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
if [[ "$*" != "--wait=5 --noflush" ]]; then echo "unexpected: $*" >&2; exit 2; fi
cat >> %q
`, restored)
	if err := os.WriteFile(iptables, []byte(list), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(iptables+"-restore", []byte(restore), 0o755); err != nil {
		t.Fatal(err)
	}
	return iptables, count, restored
}

type logRecorder struct {
	mu   sync.Mutex
	logs []string
}

func (l *logRecorder) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.logs = append(l.logs, fmt.Sprintf(format, args...))
}

func (l *logRecorder) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.logs, "\n")
}

func startKeep(t *testing.T, r Rule) *logRecorder {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	logs := &logRecorder{}
	errc := make(chan error, 1)
	go func() { errc <- r.Keep(ctx, 10*time.Millisecond, logs.logf) }()
	t.Cleanup(func() {
		cancel()
		if err := <-errc; err != nil {
			t.Errorf("Keep returned %v after cancel", err)
		}
	})
	return logs
}

func listings(path string) int {
	data, _ := os.ReadFile(path)
	n, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return n
}

func TestKeepLogsEnsureFailureAndContinues(t *testing.T) {
	iptables, count, restored := fakeIptables(t, "-P PREROUTING ACCEPT", "-P PREROUTING ACCEPT\n"+saveLegacy, 3)
	logs := startKeep(t, Rule{ProxyPort: 5443, Iptables: iptables})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(logs.String(), "Ensure failed") && listings(count) >= 4 {
			if data, _ := os.ReadFile(restored); !strings.Contains(string(data), "-I PREROUTING 1 ") {
				t.Fatalf("iptables-restore input = %q, want the hook inserted", data)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Keep did not log and continue after failure. logs=%q listings=%d", logs, listings(count))
}

func TestKeepIsSilentWhenSaveRendersMatchTCP(t *testing.T) {
	installed := strings.Join([]string{"-P PREROUTING ACCEPT", "-N HIBERNAUT", saveJump, saveE2B, saveLink, save443}, "\n")
	iptables, count, restored := fakeIptables(t, installed, installed, -1)
	logs := startKeep(t, Rule{ProxyPort: 5443, LinkPort: 5444, LinkCIDR: "198.18.0.0/15", Iptables: iptables})

	deadline := time.Now().Add(2 * time.Second)
	for listings(count) < 10 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := listings(count); n < 10 {
		t.Fatalf("Keep listed the table %d times in 2s", n)
	}
	if logs.String() != "" {
		t.Fatalf("Keep logged %q for an installed layout", logs)
	}
	if _, err := os.Stat(restored); err == nil {
		t.Fatal("Keep ran iptables-restore for an installed layout")
	}
}
