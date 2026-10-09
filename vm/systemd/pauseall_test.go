package systemd

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func TestPauseAllStopsBeforeTheProxyWithoutRequiringIt(t *testing.T) {
	data, err := os.ReadFile("hibd-pause-all.service")
	if err != nil {
		t.Fatal(err)
	}
	deps := map[string][]string{}
	for line := range strings.SplitSeq(string(data), "\n") {
		if key, value, ok := strings.Cut(line, "="); ok {
			deps[key] = append(deps[key], strings.Fields(value)...)
		}
	}
	if !slices.Contains(deps["After"], "hibd-proxy.service") {
		t.Error("pause-all must be ordered after hibd-proxy, so it pauses while the proxy still answers close")
	}
	for _, key := range []string{"Requires", "Wants", "BindsTo", "Requisite", "PartOf"} {
		if slices.Contains(deps[key], "hibd-proxy.service") {
			t.Errorf("pause-all must still run without the proxy, but %s= names it", key)
		}
	}
}
