// Package hook owns the host egress interception hook.
package hook

// Rule is the iptables layout that sends machines' traffic to the host's listeners: port 443 to
// the egress proxy, and the link range to the link listener.
type Rule struct {
	// ProxyPort is the host port the egress proxy listens on.
	ProxyPort int
	// LinkPort is the host port the link listener listens on. Zero means links are off, which is
	// the default until every M12 slice has landed.
	LinkPort int
	// LinkCIDR is the virtual range machines use to reach each other. Empty means links are off.
	LinkCIDR string
	Iptables string
}
