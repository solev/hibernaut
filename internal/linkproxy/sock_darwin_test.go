package linkproxy

import "golang.org/x/sys/unix"

// unscaled turns off TCP options on a socket before it connects. Without window scaling the
// peer can advertise at most 64 KiB, which keeps its receive window small on a macOS loopback,
// where the kernel grows a receive buffer past what was set on it.
func unscaled(fd uintptr) error {
	return unix.SetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_NOOPT, 1)
}

// tcpKeepIdle is the TCP option that holds how long a connection idles before its first
// keepalive probe.
const tcpKeepIdle = unix.TCP_KEEPALIVE
