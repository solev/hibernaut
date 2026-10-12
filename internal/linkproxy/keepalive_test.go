//go:build linux || darwin

package linkproxy

import (
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/solev/hibernaut/internal/links"
)

// Both legs of an open link wait links.Keepalive before their first keepalive probe and between
// the probes after it.
func TestBothLegsUseTheLinkKeepalive(t *testing.T) {
	h := start(t)
	h.link(h.peer())
	var open []*link
	h.p.mu.Lock()
	for l := range h.p.links {
		open = append(open, l)
	}
	h.p.mu.Unlock()
	if len(open) != 1 {
		t.Fatalf("%d links open, want 1", len(open))
	}
	l := open[0]
	l.mu.Lock()
	legs := []struct {
		name string
		conn net.Conn
	}{{"source", l.src}, {"target", l.dst}}
	l.mu.Unlock()
	for _, leg := range legs {
		on, idle, interval, err := keepaliveOf(leg.conn)
		if err != nil {
			t.Fatalf("the %s leg: %v", leg.name, err)
		}
		if !on || idle != links.Keepalive || interval != links.Keepalive {
			t.Errorf("the %s leg has keepalive %t after %v every %v, want true after %v every %v",
				leg.name, on, idle, interval, links.Keepalive, links.Keepalive)
		}
	}
}

// keepaliveOf reads whether c sends keepalive probes, how long it idles before the first and
// how long it waits between them.
func keepaliveOf(c net.Conn) (on bool, idle, interval time.Duration, err error) {
	var enabled, idleSecs, intervalSecs int
	if !control(c, func(fd uintptr) {
		var errs [3]error
		enabled, errs[0] = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_KEEPALIVE)
		idleSecs, errs[1] = unix.GetsockoptInt(int(fd), unix.IPPROTO_TCP, tcpKeepIdle)
		intervalSecs, errs[2] = unix.GetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_KEEPINTVL)
		err = errors.Join(errs[:]...)
	}) {
		return false, 0, 0, fmt.Errorf("%T has no socket", c)
	}
	return enabled != 0, time.Duration(idleSecs) * time.Second, time.Duration(intervalSecs) * time.Second, err
}
