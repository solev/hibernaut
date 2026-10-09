//go:build !linux && !darwin

package linkproxy

import "net"

// unsent cannot be read here, so a graceful close does not wait.
func unsent(net.Conn) int { return 0 }
