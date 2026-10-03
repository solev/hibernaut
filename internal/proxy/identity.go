package proxy

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/machines"
)

// Identifier maps connection addresses to sandbox IDs and cached machine names.
// Configure it before the first call to Identify.
type Identifier struct {
	Context context.Context
	Sandbox func(src, local netip.Addr) (string, error)
	List    func(context.Context, engine.ListOptions) ([]engine.Machine, error)
	Now     func() time.Time
	Logf    func(string, ...any)

	mu          sync.Mutex
	names       map[string]string
	nextRefresh time.Time
}

// Identify returns a name and ID. Name lookup failures fall back to the ID without an error.
// Slot lookup failures return an error. Calls may run concurrently.
func (i *Identifier) Identify(conn net.Conn) (string, string, error) {
	remote, err := tcpIP(conn.RemoteAddr())
	if err != nil {
		return "", "", err
	}
	local, err := tcpIP(conn.LocalAddr())
	if err != nil {
		return "", "", err
	}
	id, err := i.Sandbox(remote, local)
	if err != nil {
		return "", "", err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if name := i.names[id]; name != "" {
		return name, id, nil
	}
	now := time.Now()
	if i.Now != nil {
		now = i.Now()
	}
	if !now.Before(i.nextRefresh) {
		i.nextRefresh = now.Add(5 * time.Second)
		ctx := i.Context
		if ctx == nil {
			ctx = context.Background()
		}
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		list, err := i.List(ctx, engine.ListOptions{})
		if err != nil {
			if i.Logf != nil {
				i.Logf("proxy: cannot refresh machine names. Auditing sandbox IDs")
			}
		} else {
			i.names = make(map[string]string, len(list))
			for _, machine := range list {
				if name := machines.Name(machine); name != "" {
					i.names[machine.ID] = name
				}
			}
			if i.names[id] == "" && i.Logf != nil {
				i.Logf("proxy: no machine name for sandbox %s. Auditing its ID", id)
			}
		}
	}
	if name := i.names[id]; name != "" {
		return name, id, nil
	}
	return id, id, nil
}

func tcpIP(addr net.Addr) (netip.Addr, error) {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok {
		return netip.Addr{}, fmt.Errorf("proxy: expected TCP address, got %T", addr)
	}
	ip, ok := netip.AddrFromSlice(tcp.IP)
	if !ok {
		return netip.Addr{}, fmt.Errorf("proxy: invalid TCP address")
	}
	return ip.Unmap(), nil
}
