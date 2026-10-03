package hook

import (
	"testing"
)

func TestParseRulesFindsFirstMatchingRuleAndDuplicates(t *testing.T) {
	const rules = `-P PREROUTING ACCEPT
-A PREROUTING -i veth-+ -p tcp -m tcp --dport 443 -m comment --comment other -j REDIRECT --to-ports 1111
-A PREROUTING -m comment --comment "x" -j hibernaut-chain
-A PREROUTING -i veth-+ -p tcp -m tcp --dport 443 -m comment --comment hibernaut -j REDIRECT --to-ports 5443
-A PREROUTING -i veth-+ -p tcp --dport 443 -m comment --comment hibernaut -j REDIRECT --to-ports 5444
`
	got, err := parseRules(rules)
	if err != nil {
		t.Fatal(err)
	}
	if got.firstIndex != 2 {
		t.Fatalf("firstIndex = %d, want 2", got.firstIndex)
	}
	if len(got.rules) != 2 {
		t.Fatalf("len(rules) = %d, want 2", len(got.rules))
	}
	if !got.rules[0].hasPort(5443) {
		t.Errorf("first hibernaut rule port mismatch: %#v", got.rules[0])
	}
	if got.rules[1].deleteArgs[0] != "-D" || got.rules[1].deleteArgs[1] != "PREROUTING" {
		t.Errorf("deleteArgs prefix = %v, want -D PREROUTING", got.rules[1].deleteArgs[:2])
	}
}

func TestParseRulesIgnoresQuotedHibernautComment(t *testing.T) {
	const rules = `-A PREROUTING -i veth-+ -p tcp -m tcp --dport 443 -m comment --comment "hibernaut" -j REDIRECT --to-ports 5443`
	got, err := parseRules(rules)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.rules) != 0 {
		t.Fatalf("len(rules) = %d, want 0", len(got.rules))
	}
}

func TestParseRulesRejectsQuotedOwnedLine(t *testing.T) {
	const rules = `-A PREROUTING -i veth-+ -p tcp -m tcp --dport 443 -m comment --comment hibernaut -j REDIRECT --to-ports 5443 "extra"`
	if _, err := parseRules(rules); err == nil {
		t.Fatal("parseRules accepted a quoted owned rule")
	}
}

func TestParseRulesRejectsUnexpectedHibernautShape(t *testing.T) {
	const rules = `-A PREROUTING -p udp -m comment --comment hibernaut -j REDIRECT --to-ports 5443`
	if _, err := parseRules(rules); err == nil {
		t.Fatal("parseRules accepted an unexpected hibernaut rule")
	}
}
