package systemd

import (
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/solev/hibernaut/internal/links"
)

// settings maps each key of a unit or tmpfiles file to every value it is given, split into
// fields.
func settings(t *testing.T, path string) map[string][]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for line := range strings.SplitSeq(string(data), "\n") {
		if key, value, ok := strings.Cut(line, "="); ok && !strings.HasPrefix(line, "#") {
			out[key] = append(out[key], strings.Fields(value)...)
		}
	}
	return out
}

func TestProxyUnitServesLinks(t *testing.T) {
	unit := settings(t, "hibd-proxy.service")
	if !slices.Contains(unit["ReadWritePaths"], links.RunDir) {
		t.Errorf("the proxy must be able to write %s, for the control socket and the guard's tokens", links.RunDir)
	}
	if _, ok := unit["RuntimeDirectory"]; ok {
		t.Error("RuntimeDirectory= would delete the quiescing marker whenever the proxy stops")
	}
	if slices.Contains(unit["ProtectKernelTunables"], "yes") {
		t.Error("ProtectKernelTunables=yes would stop the link guard from setting net.ipv4.tcp_fwmark_accept")
	}
	if weight, err := strconv.Atoi(strings.Join(unit["CPUWeight"], "")); err != nil || weight <= 100 {
		t.Errorf("CPUWeight=%v, want more than the default 100, so a busy host doesn't starve the Keeper", unit["CPUWeight"])
	}
	if !slices.Equal(unit["WatchdogSec"], []string{"30"}) || !slices.Equal(unit["Restart"], []string{"always"}) {
		t.Errorf("WatchdogSec=%v and Restart=%v, want 30 and always, so a wedged proxy is replaced", unit["WatchdogSec"], unit["Restart"])
	}
	if slices.Contains(unit["NotifyAccess"], "none") {
		t.Error("NotifyAccess=none would drop the proxy's watchdog pings")
	}
}

func TestTmpfilesCreatesRunDir(t *testing.T) {
	data, err := os.ReadFile("hibernaut-tmpfiles.conf")
	if err != nil {
		t.Fatal(err)
	}
	var entries [][]string
	for line := range strings.SplitSeq(string(data), "\n") {
		if fields := strings.Fields(line); len(fields) > 0 && !strings.HasPrefix(fields[0], "#") {
			entries = append(entries, fields)
		}
	}
	want := []string{"d", links.RunDir, "0755", "root", "root", "-"}
	if len(entries) != 1 || !slices.Equal(entries[0], want) {
		t.Errorf("entries %q, want only %q", entries, want)
	}
}

func TestSetupHostCreatesRunDirBeforeTheProxy(t *testing.T) {
	data, err := os.ReadFile("../setup-host.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)
	install := strings.Index(script, `install -D -m 644 "$here/systemd/hibernaut-tmpfiles.conf" /usr/lib/tmpfiles.d/hibernaut.conf`)
	create := strings.Index(script, "systemd-tmpfiles --create /usr/lib/tmpfiles.d/hibernaut.conf")
	restart := strings.Index(script, "systemctl restart hibd-proxy.service")
	if install < 0 || create < install || restart < create {
		t.Errorf("setup-host.sh must install the tmpfiles entry (at %d) and create it (at %d) before it restarts the proxy (at %d)", install, create, restart)
	}
}

func TestOrchestratorDeniesTheLinkRange(t *testing.T) {
	deny := settings(t, "e2b-orchestrator.service")["IPAddressDeny"]
	for _, addr := range []string{"168.63.129.16", "169.254.169.254", links.CIDR} {
		if !slices.Contains(deny, addr) {
			t.Errorf("IPAddressDeny=%v is missing %s", deny, addr)
		}
	}
}

func TestSandboxFirewallDropsTheLinkRange(t *testing.T) {
	data, err := os.ReadFile("../e2b/sandbox-firewall.nft")
	if err != nil {
		t.Fatal(err)
	}
	rule := `iifname "veth-*" ip daddr ` + links.CIDR + ` meta l4proto { udp, icmp } counter drop`
	if !strings.Contains(string(data), "\t\t"+rule+"\n") {
		t.Errorf("sandbox-firewall.nft must drop UDP and ICMP from machines to the link range:\n%s", rule)
	}
}
