package hook

import (
	"fmt"
	"strconv"
	"strings"
)

type parsedRules struct {
	firstIndex int
	rules      []parsedRule
}

type parsedRule struct {
	port       int
	position   int
	deleteArgs []string
}

func (r parsedRule) hasPort(port int) bool {
	return r.port == port
}

func parseRules(output string) (parsedRules, error) {
	parsed := parsedRules{firstIndex: -1}
	ruleIndex := 0
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "-A PREROUTING ") {
			continue
		}
		if !hasHibernautComment(strings.Fields(line)) {
			ruleIndex++
			continue
		}
		rule, err := parseHibernautRule(line)
		if err != nil {
			return parsedRules{}, err
		}
		rule.position = ruleIndex + 1
		if parsed.firstIndex == -1 {
			parsed.firstIndex = ruleIndex
		}
		parsed.rules = append(parsed.rules, rule)
		ruleIndex++
	}
	return parsed, nil
}

func hasHibernautComment(fields []string) bool {
	for i := 0; i+1 < len(fields); i++ {
		if fields[i] == "--comment" && fields[i+1] == "hibernaut" {
			return true
		}
	}
	return false
}

func parseHibernautRule(line string) (parsedRule, error) {
	if strings.ContainsAny(line, "\"'`\\") {
		return parsedRule{}, fmt.Errorf("hook: refusing quoted iptables rule %q", line)
	}
	fields := strings.Fields(line)
	if len(fields) != 16 && len(fields) != 18 {
		return parsedRule{}, fmt.Errorf("hook: cannot parse hibernaut iptables rule %q", line)
	}
	i := 0
	expect := func(want string) error {
		if i >= len(fields) || fields[i] != want {
			return fmt.Errorf("hook: cannot parse hibernaut iptables rule %q", line)
		}
		i++
		return nil
	}
	for _, want := range []string{"-A", "PREROUTING", "-i", "veth-+", "-p", "tcp"} {
		if err := expect(want); err != nil {
			return parsedRule{}, err
		}
	}
	if i+1 < len(fields) && fields[i] == "-m" && fields[i+1] == "tcp" {
		i += 2
	}
	for _, want := range []string{"--dport", "443", "-m", "comment", "--comment", "hibernaut", "-j", "REDIRECT"} {
		if err := expect(want); err != nil {
			return parsedRule{}, err
		}
	}
	if i >= len(fields) || (fields[i] != "--to-ports" && fields[i] != "--to-port") {
		return parsedRule{}, fmt.Errorf("hook: cannot parse hibernaut iptables rule %q", line)
	}
	i++
	if i >= len(fields) {
		return parsedRule{}, fmt.Errorf("hook: cannot parse hibernaut iptables rule %q", line)
	}
	port, err := strconv.Atoi(fields[i])
	if err != nil || port <= 0 || port > 65535 {
		return parsedRule{}, fmt.Errorf("hook: cannot parse hibernaut iptables rule %q", line)
	}
	i++
	if i != len(fields) {
		return parsedRule{}, fmt.Errorf("hook: cannot parse hibernaut iptables rule %q", line)
	}
	deleteArgs := append([]string{"-D", "PREROUTING"}, fields[2:]...)
	return parsedRule{port: port, deleteArgs: deleteArgs}, nil
}
