//go:build linux || darwin

package linkproxy

import (
	"errors"
	"io"
	"net"
	"syscall"
	"testing"
	"time"
)

func TestCloseDeliversWhatIsStillQueued(t *testing.T) {
	const payload = 256 << 10
	h := newHarness(t)
	// Nothing here comes near this, so only a link that does not wait for its queues loses bytes.
	h.p.drainTimeout = time.Hour
	h.dialer.set(func(d *fakeDialer) { d.control = queueing(payload) })
	targets := make(chan net.Conn, 1)
	h.route(tgtGate, 8080, func(_ *backend, c net.Conn) {
		_ = c.(countingConn).Conn.(*net.TCPConn).SetReadBuffer(64 << 10)
		targets <- c
		<-t.Context().Done()
	})
	h.serve()
	c := h.connect(h.peer())
	var tgt net.Conn
	select {
	case tgt = <-targets:
	case <-time.After(patience):
		t.Fatal("the link never dialled the target")
	}
	_ = tgt.SetDeadline(time.Now().Add(patience))

	if _, err := c.Write(make([]byte, payload)); err != nil {
		t.Fatal(err)
	}
	_ = c.(*net.TCPConn).CloseWrite()
	eventually(t, "the payload queued for the target", func() bool {
		h.p.mu.Lock()
		defer h.p.mu.Unlock()
		for l := range h.p.links {
			return l.in.Load() == payload
		}
		return false
	})
	_ = tgt.(interface{ CloseWrite() error }).CloseWrite()
	if _, err := io.Copy(io.Discard, c); err != nil && !isReset(err) {
		t.Fatalf("the source read %v, want the target's half-close", err)
	}

	// Both sides have half-closed, so the link is ending. The target stays busy a little longer: a
	// link that waits for its queues is still waiting then, and one that does not has reset the
	// target leg already.
	time.Sleep(100 * time.Millisecond)
	n, err := io.Copy(io.Discard, tgt)
	if err != nil && !isReset(err) {
		t.Fatalf("the target read %v after %d bytes", err, n)
	}
	if n != payload {
		t.Fatalf("the target received %d of the %d bytes sent before the source closed", n, payload)
	}
}

// queueing is a dial control for a target leg that can queue sndbuf bytes, and whose target
// keeps a small receive window, so most of a larger payload waits on the leg.
func queueing(sndbuf int) func(network, address string, c syscall.RawConn) error {
	return func(_, _ string, c syscall.RawConn) error {
		var err error
		if cerr := c.Control(func(fd uintptr) {
			err = errors.Join(
				syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_SNDBUF, sndbuf),
				unscaled(fd),
			)
		}); cerr != nil {
			return cerr
		}
		return err
	}
}
