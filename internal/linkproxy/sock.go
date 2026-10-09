package linkproxy

import (
	"net"
	"syscall"

	"github.com/solev/hibernaut/internal/links"
)

// control runs f on c's descriptor, and reports false when c has none to give.
func control(c net.Conn, f func(fd uintptr)) bool {
	sc, ok := c.(syscall.Conn)
	if !ok {
		return false
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return false
	}
	return raw.Control(f) == nil
}

// setLinger makes closing c discard its queues and send a reset, which is what an abort needs.
// It is set when the socket is first in hand, so an abort never has to set it first.
func setLinger(c net.Conn) {
	if t, ok := c.(interface{ SetLinger(int) error }); ok {
		_ = t.SetLinger(0)
	}
}

// keepalive is the only idle handling a link has.
func keepalive(c net.Conn) {
	if t, ok := c.(*net.TCPConn); ok {
		_ = t.SetKeepAliveConfig(net.KeepAliveConfig{Enable: true, Idle: links.Keepalive, Interval: links.Keepalive})
	}
}

func closeWrite(c net.Conn) {
	if t, ok := c.(interface{ CloseWrite() error }); ok {
		_ = t.CloseWrite()
	}
}
