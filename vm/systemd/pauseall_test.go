package systemd

import (
	"slices"
	"testing"
)

func TestPauseAllStopsBeforeTheProxyWithoutRequiringIt(t *testing.T) {
	deps := settings(t, "hibd-pause-all.service")
	if !slices.Contains(deps["After"], "hibd-proxy.service") {
		t.Error("pause-all must be ordered after hibd-proxy, so it pauses while the proxy still answers close")
	}
	for _, key := range []string{"Requires", "Wants", "BindsTo", "Requisite", "PartOf"} {
		if slices.Contains(deps[key], "hibd-proxy.service") {
			t.Errorf("pause-all must still run without the proxy, but %s= names it", key)
		}
	}
}

func TestPauseAllStartsWithTheOrchestrator(t *testing.T) {
	deps := settings(t, "hibd-pause-all.service")
	if !slices.Contains(deps["WantedBy"], "multi-user.target") {
		t.Error("pause-all must start at boot, or nothing pauses the machines at the next shutdown")
	}
	// Its stop leaves the quiescing marker, and only its start removes it.
	if !slices.Contains(deps["WantedBy"], "e2b-orchestrator.service") {
		t.Error("starting the orchestrator by hand must start pause-all too, or the quiescing marker " +
			"it left stays and hib new and links refuse until a reboot")
	}
}
