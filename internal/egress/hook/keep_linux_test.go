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

func TestKeepLogsEnsureFailureAndContinues(t *testing.T) {
	dir := t.TempDir()
	iptables := filepath.Join(dir, "iptables")
	state := filepath.Join(dir, "state")
	script := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
state=%q
count=0
if [[ -f "$state" ]]; then
  count=$(cat "$state")
fi
if [[ "$*" == "-w 5 -t nat -S PREROUTING" ]]; then
  count=$((count + 1))
  echo "$count" > "$state"
  if [[ "$count" == "1" ]]; then
    echo "-P PREROUTING ACCEPT"
    exit 0
  fi
  if [[ "$count" == "3" ]]; then
    echo "xtables lock timed out" >&2
    exit 4
  fi
  echo "-A PREROUTING -i veth-+ -p tcp -m tcp --dport 443 -m comment --comment hibernaut -j REDIRECT --to-ports 5443"
  exit 0
fi
exit 0
`, state)
	if err := os.WriteFile(iptables, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var logsMu sync.Mutex
	var logs []string
	errc := make(chan error, 1)
	go func() {
		errc <- (Rule{Port: 5443, Iptables: iptables}).Keep(ctx, 10*time.Millisecond, func(format string, args ...any) {
			logsMu.Lock()
			defer logsMu.Unlock()
			logs = append(logs, fmt.Sprintf(format, args...))
		})
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-errc; err != nil {
			t.Errorf("Keep returned %v after cancel", err)
		}
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		logsMu.Lock()
		joined := strings.Join(logs, "\n")
		logsMu.Unlock()
		data, _ := os.ReadFile(state)
		count, _ := strconv.Atoi(strings.TrimSpace(string(data)))
		if strings.Contains(joined, "Ensure failed") && count >= 4 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	logsMu.Lock()
	defer logsMu.Unlock()
	t.Fatalf("Keep did not log and continue after failure. logs=%q state=%s", logs, readFile(state))
}

func readFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return err.Error()
	}
	return string(data)
}
