package linkproxy

import "golang.org/x/sys/unix"

// unscaled does nothing on Linux, where a receive buffer set by hand does not grow, so it keeps
// the receive window small on its own.
func unscaled(uintptr) error { return nil }

// tcpKeepIdle is the TCP option that holds how long a connection idles before its first
// keepalive probe.
const tcpKeepIdle = unix.TCP_KEEPIDLE
