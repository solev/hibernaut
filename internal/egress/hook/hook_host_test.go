//go:build linux && host

package hook

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHostRedirectOriginalDstAndOnVeth(t *testing.T) {
	host := newNetnsHost(t)
	proxy := host.startListener(t, "proxy", "0.0.0.0:5443")
	plain := host.startListener(t, "plain", "203.0.113.10:80")

	rule := Rule{ProxyPort: 5443, Iptables: host.iptables}
	changed, err := rule.Ensure(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("Ensure reported unchanged for an empty namespace")
	}

	host.machineDial(t, "203.0.113.10:443")
	got := proxy.result(t)
	if got.original != "203.0.113.10:443" {
		t.Fatalf("OriginalDst = %s, want 203.0.113.10:443", got.original)
	}
	if got.onVeth != "ok" {
		t.Fatalf("OnVeth = %s, want ok", got.onVeth)
	}

	host.machineDial(t, "203.0.113.10:80")
	got = plain.result(t)
	if got.original != "203.0.113.10:80" {
		t.Fatalf("plain listener local = %s, want 203.0.113.10:80", got.original)
	}
}

func TestHostKeepReaddsRule(t *testing.T) {
	host := newNetnsHost(t)
	rule := Rule{ProxyPort: 5443, Iptables: host.iptables}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() {
		errc <- rule.Keep(ctx, 100*time.Millisecond, t.Logf)
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-errc; err != nil {
			t.Errorf("Keep returned %v after cancel", err)
		}
	})

	host.waitFirstRule(t, "hibernaut")
	host.must(t, host.iptables, "-w", "5", "-t", "nat", "-D", "PREROUTING", "-i", "veth-+", "-p", "tcp", "--dport", "443", "-m", "comment", "--comment", "hibernaut", "-j", "REDIRECT", "--to-ports", "5443")
	host.waitFirstRule(t, "hibernaut")

	host.must(t, host.iptables, "-w", "5", "-t", "nat", "-I", "PREROUTING", "1", "-p", "tcp", "--dport", "443", "-j", "RETURN")
	host.waitFirstRule(t, "hibernaut")
}

func TestHostOnVethRejectsNonMachineInterfaces(t *testing.T) {
	host := newNetnsHost(t)
	listener := host.startListener(t, "proxy", "127.0.0.1:5443")
	host.hostDial(t, "127.0.0.1:5443")
	got := listener.result(t)
	if !strings.Contains(got.onVeth, "lo") {
		t.Fatalf("OnVeth error = %q, want it to name lo", got.onVeth)
	}
}

func TestHostRemoveAndEnsureIdempotent(t *testing.T) {
	host := newNetnsHost(t)
	rule := Rule{ProxyPort: 5443, Iptables: host.iptables}

	changed, err := rule.Ensure(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("first Ensure changed = false, want true")
	}
	changed, err = rule.Ensure(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("second Ensure changed = true, want false")
	}

	host.must(t, host.iptables, "-w", "5", "-t", "nat", "-I", "PREROUTING", "1", "-i", "veth-+", "-p", "tcp", "--dport", "443", "-m", "comment", "--comment", "hibernaut", "-j", "REDIRECT", "--to-ports", "5444")
	if err := Remove(context.Background(), host.iptables); err != nil {
		t.Fatal(err)
	}
	host.assertNoHibernautRules(t)
	if err := Remove(context.Background(), host.iptables); err != nil {
		t.Fatal(err)
	}
}

func TestHostChainInstallsFromNothing(t *testing.T) {
	host := newNetnsHost(t)
	proxy := host.startListener(t, "proxy", "0.0.0.0:5443")
	link := host.startListener(t, "link", "0.0.0.0:5444")
	rule := Rule{ProxyPort: 5443, LinkPort: 5444, LinkCIDR: "198.18.0.0/15", Iptables: host.iptables}

	changed, err := rule.Ensure(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("Ensure reported unchanged for an empty namespace")
	}
	host.assertLayout(t, rule)
	if changed, err := rule.Ensure(context.Background()); err != nil || changed {
		t.Fatalf("second Ensure = %v, %v, want unchanged", changed, err)
	}

	// A machine that publishes 443 is reached over the link path, not sent to the egress proxy.
	host.machineDial(t, "198.18.0.9:443")
	if got := link.result(t); got.original != "198.18.0.9:443" {
		t.Fatalf("link listener OriginalDst = %s, want 198.18.0.9:443", got.original)
	}
	host.machineDial(t, "203.0.113.10:443")
	if got := proxy.result(t); got.original != "203.0.113.10:443" {
		t.Fatalf("proxy OriginalDst = %s, want 203.0.113.10:443", got.original)
	}
}

func TestHostChainRepairsForeignRuleAboveJump(t *testing.T) {
	host := newNetnsHost(t)
	rule := Rule{ProxyPort: 5443, LinkPort: 5444, LinkCIDR: "198.18.0.0/15", Iptables: host.iptables}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- rule.Keep(ctx, 100*time.Millisecond, t.Logf) }()
	t.Cleanup(func() {
		cancel()
		if err := <-errc; err != nil {
			t.Errorf("Keep returned %v after cancel", err)
		}
	})

	host.waitFirstRule(t, "hibernaut")
	host.must(t, host.iptables, "-w", "5", "-t", "nat", "-I", "PREROUTING", "1", "-p", "tcp", "--dport", "443", "-j", "RETURN")
	host.waitFirstRule(t, "hibernaut")
	host.assertLayout(t, rule)
	if !strings.Contains(host.natSave(t), "-A PREROUTING -p tcp -m tcp --dport 443 -j RETURN") {
		t.Fatal("Keep deleted the foreign rule instead of moving the jump above it")
	}
}

func TestHostChainMigratesFromBareRule(t *testing.T) {
	host := newNetnsHost(t)
	if _, err := (Rule{ProxyPort: 5443, Iptables: host.iptables}).Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(host.natSave(t), "--dport 443 -m comment --comment hibernaut -j REDIRECT") {
		t.Fatalf("links-off Ensure did not install the bare 443 rule:\n%s", host.natSave(t))
	}
	rule := Rule{ProxyPort: 5443, LinkPort: 5444, LinkCIDR: "198.18.0.0/15", Iptables: host.iptables}
	if changed, err := rule.Ensure(context.Background()); err != nil || !changed {
		t.Fatalf("migrating Ensure = %v, %v", changed, err)
	}
	host.assertLayout(t, rule)

	// Turning links off again takes the chain out in one step.
	off := Rule{ProxyPort: 5443, Iptables: host.iptables}
	if changed, err := off.Ensure(context.Background()); err != nil || !changed {
		t.Fatalf("links-off Ensure = %v, %v", changed, err)
	}
	host.assertLayout(t, off)
	if strings.Contains(host.natSave(t), chainName) {
		t.Fatalf("links-off Ensure left the chain:\n%s", host.natSave(t))
	}
}

func TestHostChainRepairsInterruptedMigration(t *testing.T) {
	host := newNetnsHost(t)
	if _, err := (Rule{ProxyPort: 5443, Iptables: host.iptables}).Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Jump present, chain half-written, legacy rule still there.
	host.must(t, host.iptables, "-w", "5", "-t", "nat", "-N", chainName)
	host.must(t, host.iptables, "-w", "5", "-t", "nat", "-A", chainName, "-d", "198.18.0.0/15", "-p", "tcp", "-j", "REDIRECT", "--to-ports", "5444")
	host.must(t, host.iptables, "-w", "5", "-t", "nat", "-I", "PREROUTING", "1", "-i", "veth-+", "-m", "comment", "--comment", "hibernaut", "-j", chainName)

	rule := Rule{ProxyPort: 5443, LinkPort: 5444, LinkCIDR: "198.18.0.0/15", Iptables: host.iptables}
	if changed, err := rule.Ensure(context.Background()); err != nil || !changed {
		t.Fatalf("repairing Ensure = %v, %v", changed, err)
	}
	host.assertLayout(t, rule)
}

func TestHostKeepSilentWhenSaveRendersMatchTCP(t *testing.T) {
	host := newNetnsHost(t)
	rule := Rule{ProxyPort: 5443, LinkPort: 5444, LinkCIDR: "198.18.0.0/15", Iptables: host.iptables}
	if _, err := rule.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if save := host.natSave(t); !strings.Contains(save, "-m tcp --dport 443") {
		t.Logf("this iptables does not render -m tcp, so the test proves less:\n%s", save)
	}
	logs := startKeep(t, rule)
	time.Sleep(time.Second)
	if logs.String() != "" {
		t.Fatalf("Keep logged on an installed layout: %q", logs)
	}
}

func TestHostChainRemoveLeavesNothing(t *testing.T) {
	host := newNetnsHost(t)
	rule := Rule{ProxyPort: 5443, LinkPort: 5444, LinkCIDR: "198.18.0.0/15", Iptables: host.iptables}
	if _, err := rule.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	host.must(t, host.iptables, "-w", "5", "-t", "nat", "-A", "PREROUTING", "-i", "veth-+", "-p", "tcp", "--dport", "443", "-m", "comment", "--comment", "hibernaut", "-j", "REDIRECT", "--to-ports", "5443")
	if err := Remove(context.Background(), host.iptables); err != nil {
		t.Fatal(err)
	}
	host.assertNoHibernautRules(t)
	if save := host.natSave(t); strings.Contains(save, chainName) {
		t.Fatalf("Remove left the chain:\n%s", save)
	}
	if err := Remove(context.Background(), host.iptables); err != nil {
		t.Fatal(err)
	}
}

func TestHostHelper(t *testing.T) {
	if os.Getenv("HIBERNAUT_HOOK_HELPER") == "" {
		return
	}
	switch os.Args[len(os.Args)-3] {
	case "listen":
		helperListen(os.Args[len(os.Args)-2], os.Args[len(os.Args)-1])
	case "dial":
		helperDial(os.Args[len(os.Args)-2], os.Args[len(os.Args)-1])
	default:
		fmt.Fprintf(os.Stderr, "unknown helper mode %q\n", os.Args[len(os.Args)-3])
		os.Exit(2)
	}
	os.Exit(0)
}

type netnsHost struct {
	hostNS    string
	machineNS string
	iptables  string
}

func newNetnsHost(t *testing.T) *netnsHost {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Fatal("host tests must run as root")
	}
	requireCommand(t, "ip")
	requireCommand(t, "iptables")
	requireCommand(t, "iptables-restore")

	name := fmt.Sprintf("hib-hook-%d-%d", os.Getpid(), time.Now().UnixNano())
	host := &netnsHost{
		hostNS:    name + "-host",
		machineNS: name + "-machine",
	}
	t.Cleanup(func() {
		_ = exec.Command("ip", "netns", "delete", host.machineNS).Run()
		_ = exec.Command("ip", "netns", "delete", host.hostNS).Run()
	})

	host.must(t, "ip", "netns", "add", host.hostNS)
	host.must(t, "ip", "netns", "add", host.machineNS)
	host.must(t, "ip", "-n", host.hostNS, "link", "add", "veth-99", "type", "veth", "peer", "name", "vm-99", "netns", host.machineNS)
	host.must(t, "ip", "netns", "exec", host.hostNS, "ip", "addr", "add", "10.250.0.1/30", "dev", "veth-99")
	host.must(t, "ip", "netns", "exec", host.hostNS, "ip", "link", "set", "veth-99", "up")
	host.must(t, "ip", "netns", "exec", host.hostNS, "ip", "link", "set", "lo", "up")
	host.must(t, "ip", "netns", "exec", host.hostNS, "ip", "link", "add", "internet", "type", "dummy")
	host.must(t, "ip", "netns", "exec", host.hostNS, "ip", "addr", "add", "203.0.113.10/32", "dev", "internet")
	host.must(t, "ip", "netns", "exec", host.hostNS, "ip", "link", "set", "internet", "up")
	host.must(t, "ip", "netns", "exec", host.machineNS, "ip", "addr", "add", "10.250.0.2/30", "dev", "vm-99")
	host.must(t, "ip", "netns", "exec", host.machineNS, "ip", "link", "set", "vm-99", "up")
	host.must(t, "ip", "netns", "exec", host.machineNS, "ip", "link", "set", "lo", "up")
	host.must(t, "ip", "netns", "exec", host.machineNS, "ip", "route", "add", "default", "via", "10.250.0.1")

	wrapper := filepath.Join(t.TempDir(), "iptables")
	for _, name := range []string{"iptables", "iptables-restore"} {
		script := "#!/usr/bin/env bash\nexec ip netns exec " + host.hostNS + " " + name + " \"$@\"\n"
		if err := os.WriteFile(filepath.Join(filepath.Dir(wrapper), name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	host.iptables = wrapper
	return host
}

func (h *netnsHost) startListener(t *testing.T, name, addr string) *listenerProcess {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	result := filepath.Join(t.TempDir(), name+".result")
	args := []string{"netns", "exec", h.hostNS, exe, "-test.run", "TestHostHelper", "--", "listen", addr, result}
	cmd := exec.Command("ip", args...)
	cmd.Env = append(os.Environ(), "HIBERNAUT_HOOK_HELPER=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	ready := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(stdout).ReadString('\n')
		if err != nil {
			ready <- err
			return
		}
		if strings.TrimSpace(line) != "READY" {
			ready <- fmt.Errorf("helper readiness = %q", line)
			return
		}
		ready <- nil
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("listener helper did not become ready")
	}
	return &listenerProcess{cmd: cmd, resultPath: result}
}

func (h *netnsHost) machineDial(t *testing.T, addr string) {
	t.Helper()
	h.helperDial(t, h.machineNS, addr)
}

func (h *netnsHost) hostDial(t *testing.T, addr string) {
	t.Helper()
	h.helperDial(t, h.hostNS, addr)
}

func (h *netnsHost) helperDial(t *testing.T, ns, addr string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("ip", "netns", "exec", ns, exe, "-test.run", "TestHostHelper", "--", "dial", addr, "ping")
	cmd.Env = append(os.Environ(), "HIBERNAUT_HOOK_HELPER=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper dial failed: %v\n%s", err, out)
	}
}

func (h *netnsHost) waitFirstRule(t *testing.T, comment string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out, err := exec.Command(h.iptables, "-w", "5", "-t", "nat", "-S", "PREROUTING").Output()
		if err == nil {
			lines := strings.Split(strings.TrimSpace(string(out)), "\n")
			for _, line := range lines {
				if !strings.HasPrefix(line, "-A PREROUTING ") {
					continue
				}
				if strings.Contains(line, "--comment "+comment) {
					return
				}
				break
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("first PREROUTING rule did not become %q", comment)
}

func (h *netnsHost) assertNoHibernautRules(t *testing.T) {
	t.Helper()
	out, err := exec.Command(h.iptables, "-w", "5", "-t", "nat", "-S", "PREROUTING").CombinedOutput()
	if err != nil {
		t.Fatalf("listing iptables rules: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "--comment hibernaut") {
		t.Fatalf("hibernaut rules remain:\n%s", out)
	}
}

func (h *netnsHost) natSave(t *testing.T) string {
	t.Helper()
	out, err := exec.Command(h.iptables, "-w", "5", "-t", "nat", "-S").Output()
	if err != nil {
		t.Fatalf("listing the nat table: %v", err)
	}
	return string(out)
}

// assertLayout checks the table is exactly rule's layout: the hibernaut rule first in
// PREROUTING, nothing else of ours, and the chain's rules in order or no chain at all.
func (h *netnsHost) assertLayout(t *testing.T, rule Rule) {
	t.Helper()
	want, err := rule.layout()
	if err != nil {
		t.Fatal(err)
	}
	save := h.natSave(t)
	table := parseNat(save)
	if rest := plan(table, want, false); rest != nil {
		t.Fatalf("layout is not %+v, still to do %q:\n%s", rule, rest, save)
	}
	pre := table.chain(prerouting)
	if len(pre) == 0 || canonical(pre[0].fields) != canonical(want.top) {
		t.Fatalf("first PREROUTING rule is not the hook:\n%s", save)
	}
	if want.chain != nil && len(table.chain(chainName)) != len(want.chain) {
		t.Fatalf("chain has %d rules, want %d:\n%s", len(table.chain(chainName)), len(want.chain), save)
	}
}

func (h *netnsHost) must(t *testing.T, name string, args ...string) {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s failed: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}

type listenerProcess struct {
	cmd        *exec.Cmd
	resultPath string
}

type listenerResult struct {
	original string
	onVeth   string
}

func (p *listenerProcess) result(t *testing.T) listenerResult {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(p.resultPath)
		if err == nil {
			_ = p.cmd.Wait()
			parts := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(parts) != 2 {
				t.Fatalf("bad listener result %q", data)
			}
			return listenerResult{original: strings.TrimPrefix(parts[0], "original="), onVeth: strings.TrimPrefix(parts[1], "onveth=")}
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("listener did not write a result")
	return listenerResult{}
}

func helperListen(addr, result string) {
	ln, err := net.Listen("tcp4", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen: %v\n", err)
		os.Exit(2)
	}
	fmt.Println("READY")
	conn, err := ln.Accept()
	if err != nil {
		fmt.Fprintf(os.Stderr, "accept: %v\n", err)
		os.Exit(2)
	}
	_, _ = io.Copy(io.Discard, conn)
	original, err := OriginalDst(conn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "original dst: %v\n", err)
		os.Exit(2)
	}
	onVeth := "ok"
	if err := OnVeth(conn); err != nil {
		onVeth = err.Error()
	}
	tmp := result + ".tmp"
	if err := os.WriteFile(tmp, []byte(fmt.Sprintf("original=%s\nonveth=%s\n", original, onVeth)), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write result: %v\n", err)
		os.Exit(2)
	}
	if err := os.Rename(tmp, result); err != nil {
		fmt.Fprintf(os.Stderr, "rename result: %v\n", err)
		os.Exit(2)
	}
	_ = conn.Close()
	_ = ln.Close()
}

func helperDial(addr, msg string) {
	conn, err := net.DialTimeout("tcp4", addr, 5*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dial: %v\n", err)
		os.Exit(2)
	}
	if _, err := conn.Write([]byte(msg)); err != nil {
		fmt.Fprintf(os.Stderr, "write: %v\n", err)
		os.Exit(2)
	}
	_ = conn.Close()
}

func requireCommand(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Fatalf("%s is required: %v", name, err)
	}
}
