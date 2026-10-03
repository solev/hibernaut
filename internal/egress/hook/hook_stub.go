//go:build !linux

package hook

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"time"
)

var errLinuxOnly = errors.New("hook: Linux only")

func (r Rule) Ensure(context.Context) (bool, error) {
	return false, errLinuxOnly
}

func (r Rule) Keep(context.Context, time.Duration, func(string, ...any)) error {
	return errLinuxOnly
}

func Remove(context.Context, string) error {
	return errLinuxOnly
}

func OriginalDst(net.Conn) (netip.AddrPort, error) {
	return netip.AddrPort{}, errLinuxOnly
}

func OnVeth(net.Conn) error {
	return errLinuxOnly
}
