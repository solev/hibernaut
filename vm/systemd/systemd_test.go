package systemd

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func TestCodexStateWritable(t *testing.T) {
	for _, unit := range []string{"hibd.service", "hibd-proxy.service"} {
		t.Run(unit, func(t *testing.T) {
			data, err := os.ReadFile(unit)
			if err != nil {
				t.Fatal(err)
			}
			var writable []string
			for line := range strings.SplitSeq(string(data), "\n") {
				if value, ok := strings.CutPrefix(line, "ReadWritePaths="); ok {
					writable = append(writable, strings.Fields(value)...)
				}
			}
			if !strings.Contains(string(data), "ProtectSystem=strict\n") {
				t.Error("the unit must keep strict filesystem protection")
			}
			if !slices.Contains(writable, "/var/lib/hibernaut") {
				t.Error("the unit must allow Codex refreshes to write /var/lib/hibernaut")
			}
			if unit == "hibd.service" && !slices.Contains(writable, "/var/lib/e2b/local-template-storage") {
				t.Error("the daemon must still be able to clean up E2B snapshots")
			}
		})
	}
}
