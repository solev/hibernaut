package hibd

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/solev/hibernaut/internal/links"
)

const (
	// sandboxFirewall is the nftables table vm/e2b/sandbox-firewall.nft loads.
	sandboxFirewall = "hibernaut-sandboxes"
	// orchestratorUnit is E2B's orchestrator, whose egress proxy dials machines' TCP.
	orchestratorUnit = "e2b-orchestrator.service"
	// checkTimeout bounds one run of nft or systemctl.
	checkTimeout = 10 * time.Second
)

var (
	nftChainRE   = regexp.MustCompile(`^chain \S+ \{$`)
	nftCommentRE = regexp.MustCompile(` comment "[^"]*"$`)
	// linkDropRE is a rule dropping what machines send to the link range, of some protocols or
	// of all, as nft lists it and as sandbox-firewall.nft writes it.
	linkDropRE = regexp.MustCompile(`^iifname "veth-\*" ip daddr ` + regexp.QuoteMeta(links.CIDR) +
		` (?:(?:meta l4proto|ip protocol) (\{ [a-z0-9, ]+ \}|[a-z0-9]+) )?(?:counter (?:packets \d+ bytes \d+ )?)?drop$`)
)

// checkSandboxFirewall checks the live ruleset for sandbox-firewall.nft's drops. hibd proxy's
// rule takes machines' link TCP before routing, and the firewall drops everything else they send
// to the link range.
func checkSandboxFirewall(ctx context.Context) error {
	out, err := runCheck(ctx, "nft", "list", "table", "inet", sandboxFirewall)
	if err != nil {
		return err
	}
	return firewallDropsLinks(out)
}

// checkOrchestratorDeny checks that E2B's orchestrator may not reach the link range, so a link
// that missed hibd proxy's rule fails rather than being dialled from the host.
func checkOrchestratorDeny(ctx context.Context) error {
	out, err := runCheck(ctx, "systemctl", "show", "--property=IPAddressDeny", "--property=IPAddressAllow", orchestratorUnit)
	if err != nil {
		return err
	}
	return orchestratorDeniesLinks(out)
}

func runCheck(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(bytes.TrimSpace(exit.Stderr)) > 0 {
			return nil, fmt.Errorf("%s: %w: %s", name, err, bytes.TrimSpace(exit.Stderr))
		}
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return out, nil
}

// firewallDropsLinks reads a listing of the sandbox firewall and reports whether its forward
// chains drop what machines' veths send to the link range: everything, as sandbox-firewall.nft
// does, or at least UDP and ICMP.
func firewallDropsLinks(listing []byte) error {
	dropped := map[string]bool{}
	inChain, forward := false, false
	lines := bufio.NewScanner(bytes.NewReader(listing))
	for lines.Scan() {
		line := strings.TrimSpace(lines.Text())
		switch {
		case nftChainRE.MatchString(line):
			inChain, forward = true, false
		case !inChain:
		case line == "}":
			inChain = false
		case strings.HasPrefix(line, "type ") && strings.Contains(line, " hook forward "):
			forward = true
		case forward:
			m := linkDropRE.FindStringSubmatch(nftCommentRE.ReplaceAllString(line, ""))
			if m == nil {
				continue
			}
			if m[1] == "" {
				return nil
			}
			for p := range strings.SplitSeq(strings.Trim(m[1], "{ }"), ",") {
				dropped[strings.TrimSpace(p)] = true
			}
		}
	}
	if err := lines.Err(); err != nil {
		return err
	}
	var missing []string
	for _, p := range []struct{ name, number string }{{"udp", "17"}, {"icmp", "1"}} {
		if !dropped[p.name] && !dropped[p.number] {
			missing = append(missing, p.name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("table inet %s does not drop %s from veth-* to %s", sandboxFirewall, strings.Join(missing, " or "), links.CIDR)
	}
	return nil
}

// orchestratorDeniesLinks reads systemctl show's IPAddressDeny and IPAddressAllow, and reports
// whether the deny covers the whole link range and the allow, which wins over it, has none of
// it.
func orchestratorDeniesLinks(show []byte) error {
	want := netip.MustParsePrefix(links.CIDR)
	props := map[string]string{}
	for line := range strings.Lines(string(show)) {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			props[k] = v
		}
	}
	deny, err := addressList(props["IPAddressDeny"])
	if err != nil {
		return fmt.Errorf("%s's IPAddressDeny: %w", orchestratorUnit, err)
	}
	allow, err := addressList(props["IPAddressAllow"])
	if err != nil {
		return fmt.Errorf("%s's IPAddressAllow: %w", orchestratorUnit, err)
	}
	if !slices.ContainsFunc(deny, func(p netip.Prefix) bool { return p.Bits() <= want.Bits() && p.Contains(want.Addr()) }) {
		return fmt.Errorf("%s does not deny %s: IPAddressDeny=%s", orchestratorUnit, links.CIDR, props["IPAddressDeny"])
	}
	if i := slices.IndexFunc(allow, want.Overlaps); i >= 0 {
		return fmt.Errorf("%s allows %s, which wins over its IPAddressDeny", orchestratorUnit, allow[i])
	}
	return nil
}

// addressList parses a systemd address list. A bare address is a prefix of its full length.
func addressList(v string) ([]netip.Prefix, error) {
	var list []netip.Prefix
	for _, f := range strings.Fields(v) {
		p, err := netip.ParsePrefix(f)
		if err != nil {
			addr, aerr := netip.ParseAddr(f)
			if aerr != nil {
				return nil, fmt.Errorf("%q is not an address or a prefix", f)
			}
			p = netip.PrefixFrom(addr, addr.BitLen())
		}
		list = append(list, p.Masked())
	}
	return list, nil
}
