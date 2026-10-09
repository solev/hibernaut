//go:build !linux

package linkproxy

import (
	"errors"
	"net"
	"syscall"
)

var errLinuxOnly = errors.New("linkproxy: socket marks are Linux only")

func socketMark(net.Conn) (uint32, error) {
	return 0, errLinuxOnly
}

func dialControl(uint32) func(network, address string, c syscall.RawConn) error {
	return func(string, string, syscall.RawConn) error { return errLinuxOnly }
}
