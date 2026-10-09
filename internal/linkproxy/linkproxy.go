// Package linkproxy carries the bytes of a machine-to-machine link: it accepts the connections
// nat redirects into it, asks internal/linkadmit whether each one may become a link, dials the
// target's slot and copies both ways.
//
// The shape here is the contract M12's slices are built against, frozen before any of them
// starts so they build against each other without waiting. The bodies are the listener slice's
// to write; until then every entry point reports that links are off, which is also what the
// default HIBD_LINK_PORT of 0 means.
package linkproxy

import (
	"context"
	"net"
	"net/netip"
	"time"

	"github.com/solev/hibernaut/internal/audit"
	"github.com/solev/hibernaut/internal/links"
)

// Peer is who is calling and what they asked for, recovered from a redirected connection.
type Peer struct {
	// Source is the calling machine's sandbox ID.
	Source string
	// SourceAddr is the slot address the connection came from, in 10.11.0.0/16.
	SourceAddr netip.Addr
	// Dst is the address and port the caller dialled, before nat rewrote it.
	Dst netip.AddrPort
}

// Ingress recovers a Peer from an accepted connection.
//
// It is an interface because everything it does is Linux-only and host-only: the real one uses
// hook.OnVeth to refuse anything that did not arrive on a sandbox veth, hook.OriginalDst to
// recover the pre-redirect destination, and slots.Finder to map the source address to a sandbox.
// On macOS the whole hook package is a stub whose functions all fail, and slots.Finder is a
// concrete struct that cannot be faked, so without this seam none of the listener's tests could
// run where its slice is written.
type Ingress interface {
	Identify(net.Conn) (Peer, error)
}

// Dialer opens the target leg of a link.
//
// The real one dials the target's current slot address with SO_LINGER 0 and the link packet mark
// set before connect; the test one dials a loopback listener. SO_LINGER is set at creation
// rather than at abort because an abort that has to set an option first is an abort that can
// fail halfway, and a lingering socket keeps delivering queued bytes to whatever inherits the
// address.
type Dialer interface {
	Dial(ctx context.Context, a links.Admission) (net.Conn, error)
}

// Config is everything the listener needs. Every dependency is an interface or a function value,
// so the listener runs over a loopback pair with no host, no E2B and no root.
type Config struct {
	// Addr is the listen address, or "" when links are off.
	Addr string
	// Admitter decides whether each connection may become a link.
	Admitter links.Admitter
	// Runs says which run owns a machine and closes a channel when that run ends.
	Runs links.Runs
	// Guard is the kernel fence. The listener refreshes an element for each endpoint of each
	// live link, every GuardKeep, and only after rechecking with Runs that the run behind it
	// is still live.
	Guard links.Guard
	// Ingress and Dialer are the two legs.
	Ingress Ingress
	Dialer  Dialer
	// Doc reads the current links.json. It is read per connection rather than cached, so a
	// port published or unpublished a moment ago is already true.
	Doc func() (*links.Doc, error)
	// Log is the link log. A nil Log drops entries rather than failing.
	Log *audit.LinkLog
	// Quiescing reports whether the host is winding down for the night. While it is, no new
	// link is admitted at all, before any other check.
	Quiescing func() bool
	// Now is the clock, so tests do not wait.
	Now func() time.Time
}

// Proxy is the listener. It implements links.Closer.
type Proxy struct {
	cfg Config
}

var _ links.Closer = (*Proxy)(nil)

// New checks the configuration and returns a listener. It does not open a socket.
func New(cfg Config) (*Proxy, error) {
	return &Proxy{cfg: cfg}, nil
}

// Serve accepts on l until ctx is done.
//
// When it returns, closing is done: every link socket has been closed, every Guard element the
// listener held has been dropped, and every pending audit line has been flushed. Callers rely on
// that, because a pause that proceeded while a socket was still open could reach a machine that
// had already been given the slot.
func (p *Proxy) Serve(ctx context.Context, l net.Listener) error {
	if p.cfg.Addr == "" {
		return links.ErrLinksOff
	}
	return links.ErrLinksOff
}

// AbortMachine aborts every link with id at either end and returns once their sockets are
// closed.
func (p *Proxy) AbortMachine(ctx context.Context, id string, reason string) (int, error) {
	return 0, links.ErrLinksOff
}

// AbortPorts aborts the links using id's listed published ports, and leaves its other ports'
// links open.
func (p *Proxy) AbortPorts(ctx context.Context, id string, ports []int, reason string) (int, error) {
	return 0, links.ErrLinksOff
}

// Stat reports open links by sandbox ID of either endpoint.
func (p *Proxy) Stat() links.Stats {
	return links.Stats{}
}
