package linkproxy

import (
	"net"

	"golang.org/x/sys/unix"
)

// unsent is what c has sent and not yet had acknowledged, counting an unacknowledged FIN as one.
func unsent(c net.Conn) int {
	n := 0
	control(c, func(fd uintptr) {
		info, err := unix.GetsockoptTCPConnectionInfo(int(fd), unix.IPPROTO_TCP, unix.TCP_CONNECTION_INFO)
		if err != nil {
			return
		}
		n = int(info.Snd_sbbytes)
		switch info.State {
		case tcpsFinWait1, tcpsClosing, tcpsLastAck:
			n++
		}
	})
	return n
}

// The BSD TCP states in which a FIN has been sent and not yet acknowledged.
const (
	tcpsFinWait1 = 6
	tcpsClosing  = 7
	tcpsLastAck  = 8
)
