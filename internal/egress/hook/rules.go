package hook

import (
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// The nat layout Hibernaut owns is one jump at the top of PREROUTING and one chain:
//
//	-A PREROUTING -i veth-+ -m comment --comment hibernaut -j HIBERNAUT
//	-A HIBERNAUT -d 198.18.0.0/15 -p tcp -j REDIRECT --to-ports <LinkPort>
//	-A HIBERNAUT -p tcp --dport 443 -j REDIRECT --to-ports <ProxyPort>
//
// While links are off the chain is not installed and the hook stays the single rule earlier
// versions kept:
//
//	-A PREROUTING -i veth-+ -p tcp --dport 443 -m comment --comment hibernaut -j REDIRECT --to-ports <ProxyPort>
//
// Rules are matched on the hibernaut comment and the chain, never by a strict parse, and compared
// in a canonical form. A hibernaut rule that is not the one we want is deleted. If iptables will
// not delete one, the rest is done without it and the error wraps ErrStrayRule, a warning, so one
// stray rule cannot stop the hook from being repaired or the proxy from serving.

const (
	chainName    = "HIBERNAUT"
	ownerComment = "hibernaut"
	prerouting   = "PREROUTING"
)

// natRule is one "-A" line of iptables -S.
type natRule struct {
	chain string
	// spec is the rule after "-A <chain> ", exactly as iptables printed it, quoting included, so
	// iptables-restore can delete it by specification.
	spec   string
	fields []string
}

// natTable is the part of iptables -t nat -S the hook reads.
type natTable struct {
	rules  []natRule
	chains map[string]bool
}

// layout is the state Ensure moves the nat table to.
type layout struct {
	// top is the one hibernaut rule, at PREROUTING position 1.
	top []string
	// chain is HIBERNAUT's rules in order, or nil when the chain must not exist.
	chain [][]string
}

func (r Rule) layout() (layout, error) {
	if r.ProxyPort <= 0 || r.ProxyPort > 65535 {
		return layout{}, fmt.Errorf("hook: invalid proxy port %d", r.ProxyPort)
	}
	proxy := strconv.Itoa(r.ProxyPort)
	if r.LinkPort == 0 && r.LinkCIDR == "" {
		return layout{top: []string{"-i", "veth-+", "-p", "tcp", "--dport", "443", "-m", "comment", "--comment", ownerComment, "-j", "REDIRECT", "--to-ports", proxy}}, nil
	}
	if r.LinkPort <= 0 || r.LinkPort > 65535 {
		return layout{}, fmt.Errorf("hook: invalid link port %d", r.LinkPort)
	}
	prefix, err := netip.ParsePrefix(r.LinkCIDR)
	if err != nil || !prefix.Addr().Is4() {
		return layout{}, fmt.Errorf("hook: invalid link range %q", r.LinkCIDR)
	}
	return layout{
		top: []string{"-i", "veth-+", "-m", "comment", "--comment", ownerComment, "-j", chainName},
		chain: [][]string{
			{"-d", prefix.Masked().String(), "-p", "tcp", "-j", "REDIRECT", "--to-ports", strconv.Itoa(r.LinkPort)},
			{"-p", "tcp", "--dport", "443", "-j", "REDIRECT", "--to-ports", proxy},
		},
	}, nil
}

// parseNat reads iptables -t nat -S. Lines it does not need are ignored.
func parseNat(out string) natTable {
	t := natTable{chains: map[string]bool{}}
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "-N "):
			t.chains[strings.TrimSpace(strings.TrimPrefix(line, "-N "))] = true
		case strings.HasPrefix(line, "-A "):
			chain, spec, _ := strings.Cut(strings.TrimPrefix(line, "-A "), " ")
			spec = strings.TrimSpace(spec)
			t.rules = append(t.rules, natRule{chain: chain, spec: spec, fields: splitFields(spec)})
		}
	}
	return t
}

// splitFields splits a rule the way iptables-restore does: on blanks, with double quotes
// grouping and a backslash escaping a quote or a backslash inside them.
func splitFields(spec string) []string {
	var fields []string
	var cur strings.Builder
	inQuote, inField := false, false
	for i := 0; i < len(spec); i++ {
		c := spec[i]
		switch {
		case inQuote && c == '\\' && i+1 < len(spec) && (spec[i+1] == '"' || spec[i+1] == '\\'):
			i++
			cur.WriteByte(spec[i])
		case c == '"':
			inQuote = !inQuote
			inField = true
		case !inQuote && (c == ' ' || c == '\t'):
			if inField {
				fields = append(fields, cur.String())
				cur.Reset()
				inField = false
			}
		default:
			cur.WriteByte(c)
			inField = true
		}
	}
	if inField {
		fields = append(fields, cur.String())
	}
	return fields
}

// canonical is the form two rules are compared in: iptables-save adds "-m tcp" that the rule as
// written does not need, and older versions print --to-port for --to-ports. Nothing is sorted.
func canonical(fields []string) string {
	out := make([]string, 0, len(fields))
	for i := 0; i < len(fields); i++ {
		if fields[i] == "-m" && i+1 < len(fields) && fields[i+1] == "tcp" {
			i++
			continue
		}
		f := fields[i]
		if f == "--to-port" {
			f = "--to-ports"
		}
		out = append(out, f)
	}
	return strings.Join(out, "\x00")
}

// owned reports whether rule is one of ours in PREROUTING: it carries the hibernaut comment.
func (r natRule) owned() bool {
	if r.chain != prerouting {
		return false
	}
	for i := 0; i+1 < len(r.fields); i++ {
		if r.fields[i] == "--comment" && r.fields[i+1] == ownerComment {
			return true
		}
	}
	return false
}

// refersToChain reports whether rule jumps or goes to HIBERNAUT.
func (r natRule) refersToChain() bool {
	for i := 0; i+1 < len(r.fields); i++ {
		if (r.fields[i] == "-j" || r.fields[i] == "-g") && r.fields[i+1] == chainName {
			return true
		}
	}
	return false
}

// known reports whether an owned rule has a shape some version of the hook writes: the jump or
// the bare 443 rule. Anything else is deleted too, but a failure to delete it is not allowed to
// stop the repair of the rest.
func (r natRule) known() bool {
	if canonical(r.fields) == canonical([]string{"-i", "veth-+", "-m", "comment", "--comment", ownerComment, "-j", chainName}) {
		return true
	}
	f := strings.Split(canonical(r.fields), "\x00")
	legacy := []string{"-i", "veth-+", "-p", "tcp", "--dport", "443", "-m", "comment", "--comment", ownerComment, "-j", "REDIRECT", "--to-ports"}
	if len(f) != len(legacy)+1 || !slices.Equal(f[:len(legacy)], legacy) {
		return false
	}
	port, err := strconv.Atoi(f[len(legacy)])
	return err == nil && port > 0 && port <= 65535
}

func (t natTable) chain(name string) []natRule {
	var rules []natRule
	for _, r := range t.rules {
		if r.chain == name {
			rules = append(rules, r)
		}
	}
	return rules
}

// plan returns the iptables-restore commands that move t to want, in the order the transaction
// must run them, or nil when t is already there. When skipUnknown is set it leaves owned rules
// of an unknown shape alone, which is the retry after a transaction that could not delete one.
//
// The chain is filled before the jump is inserted, and the old rules are deleted last, so even
// read as a sequence the steps never show a jump to an empty chain or no hook at all. The whole
// plan is one iptables-restore --noflush commit, so nothing in between is ever visible anyway.
func plan(t natTable, want layout, skipUnknown bool) []string {
	pre := t.chain(prerouting)
	top := canonical(want.top)
	topOK := len(pre) > 0 && canonical(pre[0].fields) == top

	var stale []natRule
	for i, r := range pre {
		if i == 0 && topOK {
			continue
		}
		if r.owned() || (want.chain == nil && t.chains[chainName] && r.refersToChain()) {
			stale = append(stale, r)
		}
	}
	if want.chain == nil && t.chains[chainName] {
		for _, r := range t.rules {
			if r.chain != prerouting && r.chain != chainName && r.refersToChain() {
				stale = append(stale, r)
			}
		}
	}
	if skipUnknown {
		stale = slices.DeleteFunc(stale, func(r natRule) bool { return r.owned() && !r.known() })
	}
	// A delete by specification removes the first match, so a second copy of the wanted rule
	// cannot be deleted while the first is kept. Delete every copy and insert it again.
	if topOK && slices.ContainsFunc(stale, func(r natRule) bool { return r.chain == prerouting && canonical(r.fields) == top }) {
		topOK = false
		stale = append([]natRule{pre[0]}, stale...)
	}

	chainOK := !t.chains[chainName]
	if want.chain != nil {
		chainOK = t.chains[chainName] && slices.EqualFunc(t.chain(chainName), want.chain, func(r natRule, w []string) bool {
			return canonical(r.fields) == canonical(w)
		})
	}
	if topOK && len(stale) == 0 && chainOK {
		return nil
	}

	var lines []string
	if want.chain != nil && !chainOK {
		lines = append(lines, ":"+chainName+" - [0:0]")
		for _, w := range want.chain {
			lines = append(lines, "-A "+chainName+" "+strings.Join(w, " "))
		}
	}
	if !topOK {
		// Copies of the wanted rule are deleted before the new one goes in, because a delete by
		// specification removes the first match, which would be the new one.
		for _, r := range stale {
			if r.chain == prerouting && canonical(r.fields) == top {
				lines = append(lines, "-D "+r.chain+" "+r.spec)
			}
		}
		lines = append(lines, "-I "+prerouting+" 1 "+strings.Join(want.top, " "))
	}
	for _, r := range stale {
		if topOK || r.chain != prerouting || canonical(r.fields) != top {
			lines = append(lines, "-D "+r.chain+" "+r.spec)
		}
	}
	if want.chain == nil && t.chains[chainName] {
		lines = append(lines, "-F "+chainName, "-X "+chainName)
	}
	return lines
}

// planRemove returns the commands that delete every owned rule, every rule that refers to the
// chain and the chain itself, or nil when there is nothing of ours left. When skipUnknown is set
// it leaves owned rules of an unknown shape alone, and the chain too, emptied, if one of them
// refers to it.
func planRemove(t natTable, skipUnknown bool) []string {
	var lines []string
	keepChain := false
	for _, r := range t.rules {
		if r.chain == chainName {
			continue
		}
		if skipUnknown && r.owned() && !r.known() {
			keepChain = keepChain || r.refersToChain()
			continue
		}
		if r.owned() || (t.chains[chainName] && r.refersToChain()) {
			lines = append(lines, "-D "+r.chain+" "+r.spec)
		}
	}
	if t.chains[chainName] {
		lines = append(lines, "-F "+chainName)
		if !keepChain {
			lines = append(lines, "-X "+chainName)
		}
	}
	return lines
}
