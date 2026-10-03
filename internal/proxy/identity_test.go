package proxy_test

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/proxy"
)

type slotConn struct {
	net.Conn
}

func (slotConn) RemoteAddr() net.Addr {
	return net.TCPAddrFromAddrPort(netip.MustParseAddrPort("10.11.0.2:1234"))
}
func (slotConn) LocalAddr() net.Addr {
	return net.TCPAddrFromAddrPort(netip.MustParseAddrPort("10.12.0.4:5443"))
}

func TestIdentifierCachesNamesAndLimitsMisses(t *testing.T) {
	now := time.Unix(100, 0)
	var lists atomic.Int32
	id := &proxy.Identifier{
		Context: t.Context(), Now: func() time.Time { return now },
		Sandbox: func(src, local netip.Addr) (string, error) {
			if src.String() != "10.11.0.2" || local.String() != "10.12.0.4" {
				t.Error("slot lookup did not use the connection addresses")
			}
			return "sandbox1", nil
		},
		List: func(context.Context, engine.ListOptions) ([]engine.Machine, error) {
			lists.Add(1)
			return []engine.Machine{{ID: "sandbox1", Metadata: map[string]string{"hib.name": "named"}}}, nil
		},
	}
	name, machineID, err := id.Identify(slotConn{})
	if err != nil || name != "named" || machineID != "sandbox1" {
		t.Fatalf("identity: %q, %q, %v", name, machineID, err)
	}
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() {
			name, _, err := id.Identify(slotConn{})
			if err != nil || name != "named" {
				t.Error("cached lookup failed")
			}
		})
	}
	wg.Wait()
	id.Sandbox = func(netip.Addr, netip.Addr) (string, error) { return "sandbox2", nil }
	for range 100 {
		name, machineID, err := id.Identify(slotConn{})
		if err != nil || name != "sandbox2" || machineID != "sandbox2" {
			t.Fatalf("missing name did not preserve ID: %q, %q, %v", name, machineID, err)
		}
	}
	if lists.Load() != 1 {
		t.Fatalf("API requests during cache hits and misses: %d", lists.Load())
	}
	now = now.Add(5 * time.Second)
	id.List = func(context.Context, engine.ListOptions) ([]engine.Machine, error) {
		lists.Add(1)
		return []engine.Machine{{ID: "sandbox2", Metadata: map[string]string{"hib.name": "new-name"}}}, nil
	}
	name, _, err = id.Identify(slotConn{})
	if err != nil || name != "new-name" || lists.Load() != 2 {
		t.Fatalf("miss was not refreshed: %q, %v, lists=%d", name, err, lists.Load())
	}
}

func TestIdentifierFallsBackOnAPIErrorButReturnsSlotError(t *testing.T) {
	var logs []string
	var lists atomic.Int32
	slotErr := errors.New("slot unavailable")
	id := &proxy.Identifier{
		Context: t.Context(),
		Sandbox: func(netip.Addr, netip.Addr) (string, error) { return "sandbox3", nil },
		List: func(context.Context, engine.ListOptions) ([]engine.Machine, error) {
			lists.Add(1)
			return nil, errors.New("API unavailable")
		},
		Logf: func(format string, args ...any) { logs = append(logs, format) },
	}
	for range 100 {
		name, machineID, err := id.Identify(slotConn{})
		if name != "sandbox3" || machineID != "sandbox3" || err != nil {
			t.Fatalf("API error lost sandbox ID: %q, %q, %v", name, machineID, err)
		}
	}
	if lists.Load() != 1 || len(logs) != 1 || !strings.Contains(logs[0], "name") {
		t.Fatalf("API error was not rate limited and logged: lists=%d, logs=%v", lists.Load(), logs)
	}
	id.Sandbox = func(netip.Addr, netip.Addr) (string, error) { return "", slotErr }
	if _, _, err := id.Identify(slotConn{}); !errors.Is(err, slotErr) {
		t.Fatalf("slot error: %v", err)
	}
}
