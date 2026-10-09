package linkproxy

import (
	"fmt"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// socketMark is the mark the kernel copied onto an accepted socket from its SYN.
func socketMark(c net.Conn) (uint32, error) {
	var mark int
	var serr error
	if !control(c, func(fd uintptr) {
		mark, serr = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_MARK)
	}) {
		return 0, fmt.Errorf("linkproxy: %T has no socket to read a mark from", c)
	}
	if serr != nil {
		return 0, fmt.Errorf("linkproxy: read SO_MARK: %w", serr)
	}
	return uint32(mark), nil
}

// dialControl sets the target leg's mark and SO_LINGER 0 before it connects.
func dialControl(mark uint32) func(network, address string, c syscall.RawConn) error {
	return func(_, _ string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) {
			if err := unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_MARK, int(mark)); err != nil {
				serr = fmt.Errorf("set SO_MARK: %w", err)
				return
			}
			if err := unix.SetsockoptLinger(int(fd), unix.SOL_SOCKET, unix.SO_LINGER, &unix.Linger{Onoff: 1}); err != nil {
				serr = fmt.Errorf("set SO_LINGER: %w", err)
			}
		}); err != nil {
			return err
		}
		return serr
	}
}

// unsent is what c has sent and not yet had acknowledged, its FIN included. A socket the
// kernel has already closed has nothing left to send.
func unsent(c net.Conn) int {
	n := 0
	control(c, func(fd uintptr) {
		info, err := unix.GetsockoptTCPInfo(int(fd), unix.IPPROTO_TCP, unix.TCP_INFO)
		if err == nil && info.State == tcpClose {
			return
		}
		if v, err := unix.IoctlGetInt(int(fd), unix.SIOCOUTQ); err == nil {
			n = v
		}
	})
	return n
}

// tcpClose is TCP_CLOSE in the kernel's TCP state numbering.
const tcpClose = 7
