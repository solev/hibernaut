package vm

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// sshdSettings maps each key of an sshd config file, lowercased, to its first value, the one sshd
// uses.
func sshdSettings(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for line := range strings.SplitSeq(string(data), "\n") {
		key, value, _ := strings.Cut(strings.TrimSpace(line), " ")
		key = strings.ToLower(key)
		if _, seen := out[key]; !seen && key != "" && !strings.HasPrefix(key, "#") {
			out[key] = strings.TrimSpace(value)
		}
	}
	return out
}

// A sleeping laptop answers no keepalives. The host's sshd and each machine's both drop it, so
// the sooner one decides how long a session outlasts a sleep.
func TestKeepalivesOutlastAShortSleep(t *testing.T) {
	host := sshdSettings(t, "sshd/00-keepalive.conf")
	machine := sshdSettings(t, "../images/dev/sshd_config")
	for _, key := range []string{"clientaliveinterval", "clientalivecountmax"} {
		if host[key] != machine[key] {
			t.Errorf("%s is %q on the host but %q in machines, so the sooner one drops sleeping laptops", key, host[key], machine[key])
		}
	}
	interval, err := strconv.Atoi(host["clientaliveinterval"])
	if err != nil {
		t.Fatalf("clientaliveinterval: %v", err)
	}
	count, err := strconv.Atoi(host["clientalivecountmax"])
	if err != nil {
		t.Fatalf("clientalivecountmax: %v", err)
	}
	// The host's TCP gives up on unacknowledged data after about 15 minutes (tcp_retries2).
	if wait := time.Duration(interval*count) * time.Second; wait < 15*time.Minute {
		t.Errorf("sshd drops a silent laptop after %v, sooner than TCP would", wait)
	}
}
