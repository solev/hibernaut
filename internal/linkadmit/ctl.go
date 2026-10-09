package linkadmit

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/solev/hibernaut/internal/linkctl"
	"github.com/solev/hibernaut/internal/links"
)

const (
	// maxRequest bounds one control-socket line.
	maxRequest = 64 << 10
	// staleDial is how long Serve tries a socket it found at its path before deciding the
	// proxy that bound it is gone.
	staleDial = time.Second
)

func (a *Admitter) serve(ctx context.Context) error {
	path := cmp.Or(a.cfg.Socket, links.CtlSocket)
	ln, err := a.listen(path)
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					return
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(10 * time.Millisecond):
				}
				continue
			}
			wg.Go(func() { a.serveConn(ctx, c) })
		}
	})
	t := time.NewTicker(links.SweepEvery)
	for done := false; !done; {
		select {
		case <-ctx.Done():
			done = true
		case <-t.C:
			a.sweep(ctx)
		}
	}
	t.Stop()
	ln.Close()
	wg.Wait()
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing the link control socket: %w", err)
	}
	return nil
}

// listen binds the control socket at path. It binds a private name first and renames it into
// place once its mode and owner are right, so nobody can connect to it before they are.
func (a *Admitter) listen(path string) (*net.UnixListener, error) {
	if err := removeStale(path); err != nil {
		return nil, err
	}
	tmp := fmt.Sprintf("%s.%d", path, os.Getpid())
	if err := os.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: tmp, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("binding the link control socket: %w", err)
	}
	ln.SetUnlinkOnClose(false)
	fail := func(err error) (*net.UnixListener, error) {
		ln.Close()
		os.Remove(tmp)
		return nil, fmt.Errorf("binding the link control socket: %w", err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return fail(err)
	}
	if err := a.chownToStateOwner(tmp); err != nil {
		return fail(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fail(err)
	}
	return ln, nil
}

// removeStale removes a socket a killed proxy left at path, and refuses to start beside a
// proxy that still answers on it or to remove anything that is not a socket.
func removeStale(path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Mode().Type() != fs.ModeSocket {
		return fmt.Errorf("%s is in the way of the link control socket: it is not a socket", path)
	}
	if c, err := net.DialTimeout("unix", path, staleDial); err == nil {
		c.Close()
		return fmt.Errorf("another hibd proxy is serving links at %s", path)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing a stale link control socket: %w", err)
	}
	return nil
}

// chownToStateOwner gives the socket to the owner of the state directory links.json is in,
// so the login user's hibd can reach it and nobody else can. Only root needs to: anyone else
// already owns what they bind.
func (a *Admitter) chownToStateOwner(path string) error {
	if os.Geteuid() != 0 {
		return nil
	}
	s, ok := a.cfg.Store.(interface{ Path() string })
	if !ok {
		return nil
	}
	fi, err := os.Stat(filepath.Dir(s.Path()))
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	return os.Chown(path, int(st.Uid), int(st.Gid))
}

func (a *Admitter) serveConn(ctx context.Context, c net.Conn) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(links.CtlTimeout))
	resp := func() linkctl.Response {
		line, err := bufio.NewReader(io.LimitReader(c, maxRequest)).ReadBytes('\n')
		if err != nil && (!errors.Is(err, io.EOF) || len(line) == 0) {
			return linkctl.Response{Err: fmt.Sprintf("reading the request: %v", err)}
		}
		var req linkctl.Request
		if err := json.Unmarshal(line, &req); err != nil {
			return linkctl.Response{Err: fmt.Sprintf("bad request: %v", err)}
		}
		hctx, cancel := context.WithTimeout(ctx, links.CtlTimeout)
		defer cancel()
		return a.handle(hctx, req)
	}()
	json.NewEncoder(c).Encode(resp)
}

// handle answers one control-socket request. It cancels the admitter's own pending admissions
// and then always asks the closer, which answers for its sockets. It never answers for the
// listener, and a closer's error is the reply's err, never a count of 0.
func (a *Admitter) handle(ctx context.Context, req linkctl.Request) linkctl.Response {
	closer := a.closerOrNil()
	if closer == nil {
		return linkctl.Response{Err: links.ErrNoCloser.Error()}
	}
	switch req.Op {
	case "revoke":
		if !idRE.MatchString(req.ID) || len(req.Ports) == 0 || slices.ContainsFunc(req.Ports, func(p int) bool { return p < 1 || p > 65535 }) {
			return linkctl.Response{Err: fmt.Sprintf("bad revoke of %q ports %v", req.ID, req.Ports)}
		}
		return a.revoke(ctx, closer, req.ID, req.Ports)
	case "close":
		if !idRE.MatchString(req.ID) {
			return linkctl.Response{Err: fmt.Sprintf("bad machine ID %q", req.ID)}
		}
		return a.closeMachine(ctx, closer, req.ID)
	case "stat":
		return stat(closer.Stat())
	}
	return linkctl.Response{Err: fmt.Sprintf("unknown op %q", req.Op)}
}

// cancel cancels the admissions match picks, pending or established, with cause. The caller
// holds mu.
func (a *Admitter) cancelWhere(match func(*adm) bool, established bool, cause error) {
	for p := range a.adms {
		if p.established == established && match(p) {
			p.cancel(cause)
		}
	}
}

func (a *Admitter) revoke(ctx context.Context, closer links.Closer, id string, ports []int) linkctl.Response {
	match := func(p *adm) bool { return p.target == id && slices.Contains(ports, p.port) }
	a.mu.Lock()
	a.cancelWhere(match, false, links.ErrRevoked)
	a.mu.Unlock()
	n, err := closer.AbortPorts(ctx, id, ports, links.ErrRevoked.Error())
	a.mu.Lock()
	a.cancelWhere(match, true, links.ErrRevoked)
	a.mu.Unlock()
	if err != nil {
		return linkctl.Response{Err: err.Error()}
	}
	return linkctl.Response{Closed: n}
}

// closeMachine closes every link with id at either end. When the listener does not answer
// within LinkCloseGrace, it drops id's gates from the kernel guard itself, which stops every
// packet of every link id had whatever the listener is doing, and answers with the links it
// had admitted.
func (a *Admitter) closeMachine(ctx context.Context, closer links.Closer, id string) linkctl.Response {
	match := func(p *adm) bool { return p.source == id || p.target == id }
	a.mu.Lock()
	a.cancelWhere(match, false, links.ErrFenced)
	if w := a.waking[id]; w != nil {
		w.cancel()
	}
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.cancelWhere(match, true, links.ErrFenced)
		a.mu.Unlock()
	}()

	type result struct {
		n   int
		err error
	}
	acked := make(chan result, 1)
	go func() {
		n, err := closer.AbortMachine(ctx, id, links.ErrFenced.Error())
		acked <- result{n, err}
	}()
	grace := time.NewTimer(links.LinkCloseGrace)
	defer grace.Stop()
	select {
	case r := <-acked:
		if r.err != nil {
			return linkctl.Response{Err: r.err.Error()}
		}
		return linkctl.Response{Closed: r.n}
	case <-grace.C:
	}
	if a.cfg.Resolver == nil || a.cfg.Dropper == nil {
		return linkctl.Response{Err: fmt.Sprintf("the link listener did not close %s's links within %v, and there is no guard to drop them from", id, links.LinkCloseGrace)}
	}
	addr, _, err := a.cfg.Resolver.Addr(id)
	if err != nil {
		return linkctl.Response{Err: fmt.Sprintf("the link listener did not close %s's links within %v, and finding its slot: %v", id, links.LinkCloseGrace, err)}
	}
	if err := a.cfg.Dropper.Drop(addr); err != nil {
		return linkctl.Response{Err: fmt.Sprintf("the link listener did not close %s's links within %v, and dropping its gates: %v", id, links.LinkCloseGrace, err)}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for p := range a.adms {
		if p.established && match(p) {
			n++
		}
	}
	return linkctl.Response{Closed: n}
}

func stat(s links.Stats) linkctl.Response {
	var resp linkctl.Response
	for o, n := range s.Ports {
		resp.Ports = append(resp.Ports, linkctl.OpenPort{Target: o.Target, Port: o.Port, Open: n})
	}
	slices.SortFunc(resp.Ports, func(x, y linkctl.OpenPort) int {
		return cmp.Or(strings.Compare(x.Target, y.Target), cmp.Compare(x.Port, y.Port))
	})
	if len(s.Machines) > 0 {
		resp.Machines = s.Machines
	}
	return resp
}
