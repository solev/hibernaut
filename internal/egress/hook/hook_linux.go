//go:build linux

package hook

import (
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

// Ensure makes our rule the first rule of nat PREROUTING.
func (r Rule) Ensure(ctx context.Context) (bool, error) {
	if r.ProxyPort <= 0 || r.ProxyPort > 65535 {
		return false, fmt.Errorf("hook: invalid proxy port %d", r.ProxyPort)
	}
	iptables := r.iptables()
	parsed, err := listRules(ctx, iptables)
	if err != nil {
		return false, err
	}
	if parsed.firstIndex == 0 && len(parsed.rules) > 0 && parsed.rules[0].hasPort(r.ProxyPort) {
		return false, nil
	}
	insertArgs := []string{"-w", "5", "-t", "nat", "-I", "PREROUTING", "1", "-i", "veth-+", "-p", "tcp", "--dport", "443", "-m", "comment", "--comment", "hibernaut", "-j", "REDIRECT", "--to-ports", fmt.Sprint(r.ProxyPort)}
	if err := run(ctx, iptables, insertArgs...); err != nil {
		return false, err
	}
	parsed, err = listRules(ctx, iptables)
	if err != nil {
		return true, err
	}
	if len(parsed.rules) == 0 {
		return true, errors.New("hook: inserted hibernaut iptables rule was not listed")
	}
	keep := parsed.rules[0]
	for i := len(parsed.rules) - 1; i >= 0; i-- {
		rule := parsed.rules[i]
		if i == 0 && rule.hasPort(r.ProxyPort) {
			continue
		}
		deleteArgs := rule.deleteArgs
		if strings.Join(rule.deleteArgs, "\x00") == strings.Join(keep.deleteArgs, "\x00") {
			deleteArgs = []string{"-D", "PREROUTING", fmt.Sprint(rule.position)}
		}
		if err := run(ctx, iptables, append([]string{"-w", "5", "-t", "nat"}, deleteArgs...)...); err != nil {
			return true, err
		}
	}
	return true, nil
}

// Keep calls Ensure now, then periodically until ctx ends.
func (r Rule) Keep(ctx context.Context, every time.Duration, logf func(string, ...any)) error {
	if _, err := r.Ensure(ctx); err != nil {
		return err
	}
	if every <= 0 {
		every = time.Second
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			changed, err := r.Ensure(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				if logf != nil {
					logf("Ensure failed: %v", err)
				}
				continue
			}
			if changed && logf != nil {
				logf("re-added hibernaut iptables hook")
			}
		}
	}
}

// Remove deletes every nat PREROUTING rule commented "hibernaut".
func Remove(ctx context.Context, iptables string) error {
	parsed, err := listRules(ctx, defaultIptables(iptables))
	if err != nil {
		return err
	}
	for _, rule := range parsed.rules {
		if err := run(ctx, defaultIptables(iptables), append([]string{"-w", "5", "-t", "nat"}, rule.deleteArgs...)...); err != nil {
			return err
		}
	}
	return nil
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

func listRules(ctx context.Context, iptables string) (parsedRules, error) {
	cmd := exec.CommandContext(ctx, iptables, "-w", "5", "-t", "nat", "-S", "PREROUTING")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return parsedRules{}, fmt.Errorf("hook: %s -w 5 -t nat -S PREROUTING: %w: %s", iptables, err, out)
	}
	return parseRules(string(out))
}

func run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("hook: %s %s: %w: %s", name, strings.Join(args, " "), err, out)
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
