package linkproxy

import (
	"context"
	"fmt"
	"net"
	"net/netip"

	"github.com/solev/hibernaut/internal/egress/hook"
	"github.com/solev/hibernaut/internal/links"
	"github.com/solev/hibernaut/internal/slots"
)

// NewIngress returns the real Ingress, which works only on a Linux host: hook.OnVeth refuses a
// connection that did not arrive on a sandbox veth, hook.OriginalDst recovers where it was
// going, finder names the machine it came from and the accepted socket's mark carries the
// token of that machine's run.
func NewIngress(finder slots.Finder) Ingress {
	return ingress{finder: finder}
}

type ingress struct {
	finder slots.Finder
}

// Identify returns ErrUnmarked with the rest of the Peer filled in when the socket carries no
// link mark, so the refusal can still say who it was.
func (in ingress) Identify(c net.Conn) (Peer, error) {
	if err := hook.OnVeth(c); err != nil {
		return Peer{}, fmt.Errorf("linkproxy: %w", err)
	}
	dst, err := hook.OriginalDst(c)
	if err != nil {
		return Peer{}, fmt.Errorf("linkproxy: original destination: %w", err)
	}
	remote, err := tcpAddr(c.RemoteAddr())
	if err != nil {
		return Peer{}, err
	}
	local, err := tcpAddr(c.LocalAddr())
	if err != nil {
		return Peer{}, err
	}
	id, err := in.finder.Sandbox(remote.Addr(), local.Addr())
	if err != nil {
		return Peer{}, fmt.Errorf("linkproxy: source %s: %w", remote.Addr(), err)
	}
	peer := Peer{Source: id, SourceAddr: remote.Addr(), Dst: dst}
	mark, err := socketMark(c)
	if err != nil {
		return peer, err
	}
	token, ok := links.Token(mark)
	if !ok {
		return peer, links.ErrUnmarked
	}
	peer.Token = token
	return peer, nil
}

func tcpAddr(addr net.Addr) (netip.AddrPort, error) {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok {
		return netip.AddrPort{}, fmt.Errorf("linkproxy: expected a TCP address, got %T", addr)
	}
	ap := tcp.AddrPort()
	return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port()), nil
}

// NewDialer returns the real Dialer, which works only on a Linux host. Each leg it dials carries
// the target gate's mark and SO_LINGER 0 from before its SYN.
func NewDialer() Dialer {
	return dialer{}
}

type dialer struct{}

func (dialer) Dial(ctx context.Context, a links.Admission) (net.Conn, error) {
	g := a.TargetGate
	if !g.Addr.Is4() || g.Token == 0 || g.Token > links.GuardMaxToken || a.Port < 1 || a.Port > 65535 {
		return nil, fmt.Errorf("linkproxy: cannot dial gate %s token %d port %d", g.Addr, g.Token, a.Port)
	}
	d := net.Dialer{Control: dialControl(links.Mark(g.Token)), KeepAlive: links.Keepalive}
	return d.DialContext(ctx, "tcp4", netip.AddrPortFrom(g.Addr, uint16(a.Port)).String())
}
