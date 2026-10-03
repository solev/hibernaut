// Package hook owns the host egress interception hook.
package hook

// Rule is the iptables rule that sends machines' port-443 traffic to the proxy.
type Rule struct {
	Port     int
	Iptables string
}
