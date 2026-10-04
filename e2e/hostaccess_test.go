//go:build e2e

package e2e

import (
	"context"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"
)

const e2bServiceAddr = "192.0.2.1"

// TestNoHostAccess checks that a machine can't reach the host's own services or Azure's
// endpoints. It tries every TCP port the host listens on, at every address a machine could use
// for the host.
func TestNoHostAccess(t *testing.T) {
	ctx := testContext(t, 15*time.Minute)
	name := newHibMachine(t, ctx)

	publicIP := vm.target[strings.LastIndex(vm.target, "@")+1:]
	gateway := mustSSHMachine(t, ctx, nil, name, "ip -4 route show default | awk '{print $3; exit}'")
	addrs := append(hostAddrs(t, ctx), publicIP, gateway, e2bServiceAddr, "169.254.169.254", "168.63.129.16")
	e2bPorts := e2bServicePorts(t, ctx)
	// 32526 is the other port of Azure's host services at 168.63.129.16.
	ports := append(hostPorts(t, ctx), "22", "80", "443", "32526")
	ports = append(ports, e2bPorts...)
	slices.Sort(ports)
	ports = slices.Compact(ports)

	allowed := map[string]bool{publicIP + ":22": true}
	for _, p := range e2bPorts {
		allowed[e2bServiceAddr+":"+p] = true
	}
	const control = "github.com:22"
	targets := []string{control}
	for _, a := range addrs {
		for _, p := range ports {
			targets = append(targets, a+":"+p)
		}
	}

	answered := probe(t, ctx, name, targets)
	if !answered[control] {
		t.Fatalf("%s didn't answer, so the probe can't see anything", control)
	}
	delete(answered, control)
	for _, target := range slices.Sorted(maps.Keys(answered)) {
		if !allowed[target] {
			t.Errorf("the machine reached %s", target)
		}
	}
	t.Logf("probed %d addresses on %d ports. Answered: %v", len(addrs), len(ports), slices.Sorted(maps.Keys(answered)))
}

func hostAddrs(t *testing.T, ctx context.Context) []string {
	t.Helper()
	var addrs []string
	for addr := range strings.FieldsSeq(mustSSH(t, ctx, "ip -4 -o addr show | awk '{print $4}'")) {
		addr, _, _ = strings.Cut(addr, "/")
		if !strings.HasPrefix(addr, "127.") {
			addrs = append(addrs, addr)
		}
	}
	if len(addrs) == 0 {
		t.Fatal("the host has no IPv4 address")
	}
	return addrs
}

func hostPorts(t *testing.T, ctx context.Context) []string {
	t.Helper()
	var ports []string
	for local := range strings.FieldsSeq(mustSSH(t, ctx, "sudo ss -Hltn | awk '{print $4}'")) {
		ports = append(ports, local[strings.LastIndex(local, ":")+1:])
	}
	if len(ports) == 0 {
		t.Fatal("nothing listens on the host, not even SSH")
	}
	return ports
}

func e2bServicePorts(t *testing.T, ctx context.Context) []string {
	t.Helper()
	var ports []string
	for line := range strings.SplitSeq(mustSSH(t, ctx, "sudo iptables -t nat -S PREROUTING"), "\n") {
		f := strings.Fields(line)
		if !slices.Contains(f, e2bServiceAddr+"/32") {
			continue
		}
		if i := slices.Index(f, "--dport"); i >= 0 && i+1 < len(f) {
			ports = append(ports, f[i+1])
		}
	}
	return ports
}

func probe(t *testing.T, ctx context.Context, name string, targets []string) map[string]bool {
	t.Helper()
	script := `probe() {
	timeout 4 bash -c '
		exec 3<>"/dev/tcp/${0%:*}/${0##*:}" || exit 1
		case "${0##*:}" in
		22) printf "SSH-2.0-hibernaut-probe\r\n" >&3 ;;
		*) printf "\000\000\000\010\004\322\026\057\r\n" >&3 ;;
		esac
		[ "$(head -c1 <&3 | wc -c)" -gt 0 ]' "$1" 2>/dev/null && echo "answered $1"
	true
}
export -f probe
xargs -P 100 -n 1 bash -c 'probe "$0"' <<'EOF'
` + strings.Join(targets, "\n") + `
EOF
`
	cmd := sshMachineCmd(ctx, nil, name, "bash -s")
	cmd.Stdin = strings.NewReader(script)
	out, err := output(cmd)
	if err != nil {
		t.Fatalf("probing from %s: %v", name, err)
	}
	answered := map[string]bool{}
	for line := range strings.SplitSeq(out, "\n") {
		if target, ok := strings.CutPrefix(line, "answered "); ok {
			answered[target] = true
		}
	}
	return answered
}
