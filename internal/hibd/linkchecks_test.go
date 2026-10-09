package hibd

import (
	"os"
	"strings"
	"testing"
)

func TestFirewallDropsLinksReadsTheTable(t *testing.T) {
	file, err := os.ReadFile("../../vm/e2b/sandbox-firewall.nft")
	if err != nil {
		t.Fatal(err)
	}
	chain := func(hook string, rules ...string) string {
		return "table inet hibernaut-sandboxes {\n\tchain c {\n\t\ttype filter hook " + hook + " priority filter - 10; policy accept;\n\t\t" +
			strings.Join(rules, "\n\t\t") + "\n\t}\n}\n"
	}
	const drop = `iifname "veth-*" ip daddr 198.18.0.0/15 `
	for _, tc := range []struct {
		name, listing, want string
	}{
		{name: "sandbox-firewall.nft", listing: string(file)},
		{name: "as nft lists it", listing: chain("forward",
			`iifname "veth-*" ip daddr { 168.63.129.16, 169.254.169.254 } counter packets 4 bytes 240 drop`,
			drop+`meta l4proto { icmp, udp } counter packets 3 bytes 252 drop`)},
		{name: "rule by rule", listing: chain("forward", drop+"ip protocol udp drop", drop+"meta l4proto icmp counter packets 0 bytes 0 drop")},
		{name: "by number", listing: chain("forward", drop+"meta l4proto { 1, 17 } drop")},
		{name: "everything", listing: chain("forward", drop+"counter packets 0 bytes 0 drop")},
		{name: "commented", listing: chain("forward", drop+`meta l4proto { icmp, udp } drop comment "machine links"`)},
		{name: "no ICMP", listing: chain("forward", drop+"meta l4proto udp counter drop"), want: "does not drop icmp from veth-* to 198.18.0.0/15"},
		{name: "accepted", listing: chain("forward", drop+"meta l4proto { icmp, udp } accept"), want: "does not drop udp or icmp"},
		{name: "part of the range", listing: chain("forward", `iifname "veth-*" ip daddr 198.18.0.0/16 drop`), want: "does not drop udp or icmp"},
		{name: "not from machines", listing: chain("forward", `iifname "eth0" ip daddr 198.18.0.0/15 drop`), want: "does not drop udp or icmp"},
		{name: "not forwarded", listing: chain("input", drop+"drop"), want: "does not drop udp or icmp"},
		{name: "after a forward chain", listing: chain("forward") + chain("output", drop+"drop"), want: "does not drop udp or icmp"},
		{name: "no table", want: "table inet hibernaut-sandboxes does not drop udp or icmp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := firewallDropsLinks([]byte(tc.listing))
			if tc.want == "" && err != nil {
				t.Fatal(err)
			}
			if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestOrchestratorDeniesLinksReadsSystemctl(t *testing.T) {
	unit, err := os.ReadFile("../../vm/systemd/e2b-orchestrator.service")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, show, want string
	}{
		{name: "e2b-orchestrator.service", show: string(unit)},
		{name: "as systemctl shows it", show: "IPAddressDeny=168.63.129.16/32 169.254.169.254/32 198.18.0.0/15\nIPAddressAllow=\n"},
		{name: "a wider range", show: "IPAddressAllow=10.0.0.0/8\nIPAddressDeny=198.16.0.0/12\n"},
		{name: "everything", show: "IPAddressDeny=0.0.0.0/0 ::/0\n"},
		{name: "part of the range", show: "IPAddressDeny=198.18.0.0/16 198.19.0.0/16\n", want: "e2b-orchestrator.service does not deny 198.18.0.0/15: IPAddressDeny=198.18.0.0/16 198.19.0.0/16"},
		{name: "only IPv6", show: "IPAddressDeny=::/0\n", want: "does not deny 198.18.0.0/15"},
		{name: "nothing", show: "IPAddressDeny=\nIPAddressAllow=\n", want: "does not deny 198.18.0.0/15"},
		{name: "no output", want: "does not deny 198.18.0.0/15"},
		{name: "allowed back", show: "IPAddressDeny=198.18.0.0/15\nIPAddressAllow=198.19.1.2/32\n", want: "e2b-orchestrator.service allows 198.19.1.2/32, which wins over its IPAddressDeny"},
		{name: "all allowed", show: "IPAddressDeny=198.18.0.0/15\nIPAddressAllow=0.0.0.0/0 ::/0\n", want: "allows 0.0.0.0/0"},
		{name: "garbage", show: "IPAddressDeny=198.18.0.0/15\nIPAddressAllow=machines\n", want: "IPAddressAllow: \"machines\" is not an address or a prefix"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := orchestratorDeniesLinks([]byte(tc.show))
			if tc.want == "" && err != nil {
				t.Fatal(err)
			}
			if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestRunCheckReportsWhatTheCommandSaid(t *testing.T) {
	_, err := runCheck(t.Context(), "sh", "-c", "echo 'no such table' >&2; exit 1")
	if want := "sh: exit status 1: no such table"; err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	out, err := runCheck(t.Context(), "sh", "-c", "echo ok")
	if err != nil || string(out) != "ok\n" {
		t.Fatalf("runCheck = %q, %v", out, err)
	}
}
