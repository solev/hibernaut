//go:build linux

package hook

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Ensure moves the nat table to the hook's layout (see rules.go) and reports whether it changed
// anything. Every change is one iptables-restore --noflush transaction that touches only the
// hibernaut rules in PREROUTING and the HIBERNAUT chain, so there is never a moment with a jump
// to an empty chain or no hook at all, and every other chain, E2B's included, is left alone.
//
// A hibernaut rule of a shape no version writes is deleted too. When iptables will not delete
// one, the rest is done without it and the error wraps ErrStrayRule, which is only a warning.
//
// iptables-restore is found beside Iptables: "iptables" gives "iptables-restore",
// "/usr/sbin/iptables-nft" gives "/usr/sbin/iptables-nft-restore".
func (r Rule) Ensure(ctx context.Context) (bool, error) {
	want, err := r.layout()
	if err != nil {
		return false, err
	}
	return ensure(ctx, natCommands(r.iptables()), want)
}

// Keep calls Ensure now, then periodically until ctx ends. It logs only when Ensure fails or had
// to change something, so a healthy table is silent. An error wrapping ErrStrayRule is logged
// once, and again only when it changes, and never ends Keep; any other error from the first
// call is returned.
func (r Rule) Keep(ctx context.Context, every time.Duration, logf func(string, ...any)) error {
	return keep(ctx, every, logf, r.Ensure)
}

// Remove deletes the jump and every other PREROUTING rule commented "hibernaut", flushes the
// HIBERNAUT chain and deletes it, in one iptables-restore --noflush transaction. It is a no-op
// when none of them is there. When iptables will not delete a hibernaut rule of a shape no
// version writes, the rest still goes and the error wraps ErrStrayRule.
func Remove(ctx context.Context, iptables string) error {
	return remove(ctx, natCommands(defaultIptables(iptables)))
}

func natCommands(iptables string) natIO {
	return natIO{
		list:    func(ctx context.Context) (natTable, error) { return listNat(ctx, iptables) },
		restore: func(ctx context.Context, lines []string) error { return restore(ctx, iptables, lines) },
	}
}

// OriginalDst returns the destination a redirected connection was sent to.
func OriginalDst(c net.Conn) (netip.AddrPort, error) {
	sysconn, ok := c.(interface {
		SyscallConn() (syscall.RawConn, error)
	})
	if !ok {
		return netip.AddrPort{}, fmt.Errorf("hook: %T does not expose SyscallConn", c)
	}
	raw, err := sysconn.SyscallConn()
	if err != nil {
		return netip.AddrPort{}, err
	}
	var addr unix.RawSockaddrInet4
	size := uint32(unsafe.Sizeof(addr))
	var sockErr error
	if err := raw.Control(func(fd uintptr) {
		_, _, errno := syscall.Syscall6(
			syscall.SYS_GETSOCKOPT,
			fd,
			uintptr(unix.SOL_IP),
			uintptr(unix.SO_ORIGINAL_DST),
			uintptr(unsafe.Pointer(&addr)),
			uintptr(unsafe.Pointer(&size)),
			0,
		)
		if errno != 0 {
			sockErr = errno
		}
	}); err != nil {
		return netip.AddrPort{}, err
	}
	if sockErr != nil {
		if errors.Is(sockErr, unix.ENOENT) || errors.Is(sockErr, unix.EINVAL) || errors.Is(sockErr, unix.ENOPROTOOPT) {
			return localAddr(c)
		}
		return netip.AddrPort{}, sockErr
	}
	if addr.Family != unix.AF_INET {
		return netip.AddrPort{}, fmt.Errorf("hook: SO_ORIGINAL_DST returned address family %d", addr.Family)
	}
	port := uint16(addr.Port>>8) | uint16(addr.Port&0xff)<<8
	return netip.AddrPortFrom(netip.AddrFrom4(addr.Addr), port), nil
}

// OnVeth returns nil when c's local address belongs to an interface named veth-*.
func OnVeth(c net.Conn) error {
	local, err := localAddr(c)
	if err != nil {
		return err
	}
	iface, err := interfaceByAddr(local.Addr())
	if err != nil {
		return err
	}
	if strings.HasPrefix(iface.Name, "veth-") {
		return nil
	}
	return fmt.Errorf("hook: local address %s belongs to interface %s, not veth-*", local.Addr(), iface.Name)
}

func listNat(ctx context.Context, iptables string) (natTable, error) {
	cmd := exec.CommandContext(ctx, iptables, "-w", "5", "-t", "nat", "-S")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return natTable{}, fmt.Errorf("hook: %s -w 5 -t nat -S: %w: %s", iptables, err, bytes.TrimSpace(stderr.Bytes()))
	}
	return parseNat(string(out)), nil
}

// restore runs lines as one iptables-restore --noflush commit on the nat table.
func restore(ctx context.Context, iptables string, lines []string) error {
	input := "*nat\n" + strings.Join(lines, "\n") + "\nCOMMIT\n"
	cmd := exec.CommandContext(ctx, iptables+"-restore", "--wait=5", "--noflush")
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("hook: %s-restore --noflush: %w: %s\ninput:\n%s", iptables, err, bytes.TrimSpace(out), input)
	}
	return nil
}

func (r Rule) iptables() string {
	return defaultIptables(r.Iptables)
}

func defaultIptables(iptables string) string {
	if iptables == "" {
		return "iptables"
	}
	return iptables
}

func localAddr(c net.Conn) (netip.AddrPort, error) {
	addr, ok := c.LocalAddr().(*net.TCPAddr)
	if !ok {
		return netip.AddrPort{}, fmt.Errorf("hook: local address %T is not TCP", c.LocalAddr())
	}
	ip, ok := netip.AddrFromSlice(addr.IP)
	if !ok {
		return netip.AddrPort{}, fmt.Errorf("hook: cannot parse local address %s", addr.IP)
	}
	return netip.AddrPortFrom(ip.Unmap(), uint16(addr.Port)), nil
}

func interfaceByAddr(addr netip.Addr) (net.Interface, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return net.Interface{}, err
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			return net.Interface{}, err
		}
		for _, ifaceAddr := range addrs {
			ip, ok := addrFromInterfaceAddr(ifaceAddr)
			if ok && ip.Unmap() == addr.Unmap() {
				return iface, nil
			}
		}
	}
	return net.Interface{}, fmt.Errorf("hook: no interface owns local address %s", addr)
}

func addrFromInterfaceAddr(addr net.Addr) (netip.Addr, bool) {
	switch a := addr.(type) {
	case *net.IPNet:
		ip, ok := netip.AddrFromSlice(a.IP)
		return ip, ok
	case *net.IPAddr:
		ip, ok := netip.AddrFromSlice(a.IP)
		return ip, ok
	default:
		return netip.Addr{}, false
	}
}
