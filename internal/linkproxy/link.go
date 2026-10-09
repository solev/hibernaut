package linkproxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/solev/hibernaut/internal/audit"
	"github.com/solev/hibernaut/internal/links"
)

var cidr = netip.MustParsePrefix(links.CIDR)

// errEnded is the cause of a link's context once the link has ended. Nothing reports it: a
// blocked call that sees it returns the link's own reason instead.
var errEnded = errors.New("linkproxy: link ended")

// link is one accepted connection, from the accept to the last line in the log.
type link struct {
	p    *Proxy
	src  net.Conn
	peer Peer

	ctx    context.Context
	cancel context.CancelCauseFunc

	// closed is closed once every socket the link holds or is still dialling is closed.
	closed chan struct{}
	// settled is closed once an ended link's reason has been decided.
	settled chan struct{}
	// watching is the goroutine that waits for the aborts armed before the first byte.
	watching sync.WaitGroup

	in, out atomic.Int64

	mu sync.Mutex
	// target and port are known once the destination has been resolved, and are what the
	// aborts match on.
	target string
	port   int
	dst    net.Conn
	// dialing is true while the target leg is being dialled, so an abort waits for the dial
	// to come back before it counts the link's sockets as closed.
	dialing bool
	ended   bool
	reason  error
	// open is true from the open line to the end, and is what Stat and the abort counts see.
	open     bool
	opened   time.Time
	admitted bool
	adm      links.Admission
}

// handle runs one accepted connection to its end.
func (p *Proxy) handle(ctx context.Context, conn net.Conn) {
	setLinger(conn)
	if p.cfg.Quiescing() {
		_ = conn.Close()
		p.refused(Peer{}, "", links.ErrQuiescing)
		return
	}
	peer, err := p.cfg.Ingress.Identify(conn)
	if err == nil && peer.Token == 0 {
		err = links.ErrUnmarked
	}
	if err != nil {
		_ = conn.Close()
		if reason := known(err); reason != nil {
			p.refused(peer, "", reason)
		}
		return
	}
	if !p.rate.allow(peer.Source, p.cfg.Now()) {
		_ = conn.Close()
		p.refused(peer, "", links.ErrRateLimited)
		return
	}

	l := &link{
		p:       p,
		src:     conn,
		peer:    peer,
		closed:  make(chan struct{}),
		settled: make(chan struct{}),
	}
	l.ctx, l.cancel = context.WithCancelCause(ctx)
	p.track(l)
	defer l.finish()
	if ctx.Err() != nil {
		l.end(links.ErrLinksOff)
		return
	}
	if err := l.connect(); err != nil {
		l.end(err)
		return
	}
	l.pipe()
}

// connect takes the link from an identified connection to an open one, in the order of the
// design's numbered steps, and returns the reason it was refused.
func (l *link) connect() error {
	p := l.p
	dst := l.peer.Dst
	if !cidr.Contains(dst.Addr()) {
		return links.ErrUnknownAddress
	}
	doc, err := p.cfg.Doc()
	if err != nil || doc == nil {
		return links.ErrStateUnreadable
	}
	target, m, ok := doc.Lookup(dst.Addr())
	if !ok {
		return links.ErrUnknownAddress
	}
	port := int(dst.Port())
	if !l.resolved(target, port) {
		return l.cause()
	}
	if target == l.peer.Source {
		return links.ErrSelfLink
	}
	if links.IsReserved(port) {
		return links.ErrReservedPort
	}
	gen, ok := m.Gen(port)
	if !ok {
		return links.ErrNotPublished
	}

	run, err := p.cfg.Runs.Current(l.peer.Source)
	if err != nil {
		return links.ErrWrongRun
	}
	gate, err := l.sourceGate(run)
	if err != nil {
		return err
	}
	if gate.Addr != l.peer.SourceAddr || gate.Token != l.peer.Token {
		return links.ErrWrongRun
	}

	adm, err := p.cfg.Admitter.Admit(l.ctx, links.AdmitRequest{
		Source:     l.peer.Source,
		SourceRun:  run,
		SourceGate: gate,
		Target:     target,
		Port:       port,
	})
	if err != nil {
		if l.ctx.Err() != nil {
			return l.cause()
		}
		if reason := known(err); reason != nil {
			return reason
		}
		return links.ErrNotRunning
	}
	if adm.Ctx == nil {
		adm.Ctx = context.Background()
	}
	l.mu.Lock()
	l.adm, l.admitted = adm, true
	l.mu.Unlock()
	if adm.Source != l.peer.Source || adm.SourceRun != run || adm.SourceGate != gate ||
		adm.Target != target || adm.Port != port {
		return links.ErrWrongRun
	}

	if err := l.dial(); err != nil {
		return err
	}
	if err := l.recheck(gen); err != nil {
		return err
	}
	if err := l.arm(); err != nil {
		return err
	}
	return l.markOpen()
}

// sourceGate asks the Keeper for the gate of the source's run, within AdmitTimeout.
func (l *link) sourceGate(run links.Run) (links.Gate, error) {
	ctx, cancel := context.WithTimeout(l.ctx, l.p.admitTimeout)
	defer cancel()
	gate, err := l.p.cfg.Keeper.Gate(ctx, l.peer.Source, run)
	switch {
	case err == nil:
		return gate, nil
	case l.ctx.Err() != nil:
		return links.Gate{}, l.cause()
	case errors.Is(err, links.ErrRunEnded), errors.Is(err, links.ErrNotRunning):
		return links.Gate{}, links.ErrWrongRun
	}
	if reason := known(err); reason != nil {
		return links.Gate{}, reason
	}
	return links.Gate{}, links.ErrWrongRun
}

// dial opens the target leg, within DialTimeout.
func (l *link) dial() error {
	l.mu.Lock()
	if l.ended {
		l.mu.Unlock()
		return l.cause()
	}
	l.dialing = true
	adm := l.adm
	l.mu.Unlock()

	ctx, cancel := context.WithTimeout(l.ctx, l.p.dialTimeout)
	conn, err := l.p.cfg.Dialer.Dial(ctx, adm)
	cancel()

	l.mu.Lock()
	l.dialing = false
	if l.ended {
		if conn != nil {
			_ = conn.Close()
		}
		close(l.closed)
		l.mu.Unlock()
		return l.cause()
	}
	if err != nil {
		l.mu.Unlock()
		if l.ctx.Err() != nil {
			return l.cause()
		}
		return links.ErrDialFailed
	}
	l.dst = conn
	l.mu.Unlock()
	setLinger(conn)
	keepalive(conn)
	keepalive(l.src)
	return nil
}

// recheck is step 10: both runs still the ones admitted, the port's generation unchanged and
// neither gate retired, all read after the dial and before the first byte.
func (l *link) recheck(gen uint64) error {
	adm := l.admission()
	for _, end := range []struct {
		id  string
		run links.Run
	}{{adm.Source, adm.SourceRun}, {adm.Target, adm.TargetRun}} {
		run, err := l.p.cfg.Runs.Current(end.id)
		if err != nil || run != end.run {
			return links.ErrRunEnded
		}
	}
	doc, err := l.p.cfg.Doc()
	if err != nil || doc == nil {
		return links.ErrStateUnreadable
	}
	now, ok := doc.Machines[adm.Target].Gen(adm.Port)
	if !ok || now != gen || now != adm.PortGen {
		return links.ErrRevoked
	}
	for _, g := range []links.Gate{adm.SourceGate, adm.TargetGate} {
		select {
		case <-l.p.cfg.Keeper.Retired(g):
			return l.retiredReason()
		default:
		}
	}
	select {
	case <-adm.Ctx.Done():
		return l.ctxReason()
	default:
	}
	return nil
}

// arm is step 11: from here on, either run ending, either gate being retired or the admission
// being cancelled aborts both halves.
func (l *link) arm() error {
	adm := l.admission()
	srcRun, err := l.p.cfg.Runs.Watch(adm.Source, adm.SourceRun)
	if err != nil {
		return links.ErrRunEnded
	}
	dstRun, err := l.p.cfg.Runs.Watch(adm.Target, adm.TargetRun)
	if err != nil {
		return links.ErrRunEnded
	}
	srcGate := l.p.cfg.Keeper.Retired(adm.SourceGate)
	dstGate := l.p.cfg.Keeper.Retired(adm.TargetGate)
	select {
	case <-srcRun:
		return links.ErrRunEnded
	case <-dstRun:
		return links.ErrRunEnded
	case <-srcGate:
		return l.retiredReason()
	case <-dstGate:
		return l.retiredReason()
	case <-adm.Ctx.Done():
		return l.ctxReason()
	default:
	}
	l.watching.Add(1)
	go func() {
		defer l.watching.Done()
		select {
		case <-l.ctx.Done():
			l.end(links.ErrLinksOff)
		case <-srcRun:
			l.end(links.ErrRunEnded)
		case <-dstRun:
			l.end(links.ErrRunEnded)
		case <-srcGate:
			l.endWith(l.retiredReason)
		case <-dstGate:
			l.endWith(l.retiredReason)
		case <-adm.Ctx.Done():
			l.endWith(l.ctxReason)
		}
	}()
	return nil
}

// retiredReason says why the Keeper retired a gate. It retires one when its run stops being
// live, when its machine is fenced and, since it cannot tell which machines are fenced
// without it, when links.json cannot be read.
func (l *link) retiredReason() error {
	doc, err := l.p.cfg.Doc()
	if err != nil || doc == nil {
		return links.ErrStateUnreadable
	}
	adm := l.admission()
	if doc.Machines[adm.Source] != nil && doc.Machines[adm.Source].Fence != nil ||
		doc.Machines[adm.Target] != nil && doc.Machines[adm.Target].Fence != nil {
		return links.ErrFenced
	}
	return links.ErrRunEnded
}

// ctxReason says why the admission was cancelled: its cause when that is one of the shared
// errors, and otherwise whatever in the world has changed since it was granted.
func (l *link) ctxReason() error {
	adm := l.admission()
	if reason := known(context.Cause(adm.Ctx)); reason != nil {
		return reason
	}
	doc, err := l.p.cfg.Doc()
	if err != nil || doc == nil {
		return links.ErrStateUnreadable
	}
	if gen, ok := doc.Machines[adm.Target].Gen(adm.Port); !ok || gen != adm.PortGen {
		return links.ErrRevoked
	}
	for _, id := range []string{adm.Source, adm.Target} {
		if m := doc.Machines[id]; m != nil && m.Fence != nil {
			return links.ErrFenced
		}
	}
	for _, end := range []struct {
		id  string
		run links.Run
	}{{adm.Source, adm.SourceRun}, {adm.Target, adm.TargetRun}} {
		if run, err := l.p.cfg.Runs.Current(end.id); err != nil || run != end.run {
			return links.ErrRunEnded
		}
	}
	return links.ErrRevoked
}

// markOpen is the last step before the first byte, and writes the open line.
func (l *link) markOpen() error {
	l.mu.Lock()
	if l.ended || l.ctx.Err() != nil {
		l.mu.Unlock()
		return l.cause()
	}
	l.open = true
	l.opened = l.p.cfg.Now()
	adm := l.adm
	l.mu.Unlock()
	l.p.write(l.entry("open", nil, adm.Woke))
	return nil
}

// pipe copies both ways until both halves have closed, then lets the last bytes drain.
func (l *link) pipe() {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		l.copy(l.dst, l.src, &l.in)
	}()
	go func() {
		defer wg.Done()
		l.copy(l.src, l.dst, &l.out)
	}()
	wg.Wait()
	l.drain()
}

// copy moves bytes from one leg to the other. An EOF is passed on as a half-close and anything
// else, a reset most often, ends the whole link.
func (l *link) copy(to, from net.Conn, n *atomic.Int64) {
	buf := make([]byte, 32<<10)
	for {
		r, err := from.Read(buf)
		if r > 0 {
			l.p.renew(l.admission(), int64(r))
			w, werr := to.Write(buf[:r])
			n.Add(int64(w))
			if werr != nil {
				l.end(nil)
				return
			}
		}
		if errors.Is(err, io.EOF) {
			closeWrite(to)
			return
		}
		if err != nil {
			l.end(nil)
			return
		}
	}
}

// drain waits, within DrainTimeout, for what is still queued on either leg to be
// acknowledged, so the close that follows does not throw it away.
func (l *link) drain() {
	timer := time.NewTimer(l.p.drainTimeout)
	defer timer.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for unsent(l.src)+unsent(l.dst) > 0 {
		select {
		case <-timer.C:
			return
		case <-l.ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// end ends the link with reason, which is nil for a link that closed on its own. The first end
// wins, and it returns whether this was it.
func (l *link) end(reason error) bool {
	return l.endWith(func() error { return reason })
}

// endWith closes every socket first and only then works out the reason, so an abort is never
// held up by the question of why.
func (l *link) endWith(reason func() error) bool {
	l.mu.Lock()
	if l.ended {
		l.mu.Unlock()
		return false
	}
	l.ended = true
	_ = l.src.Close()
	if l.dst != nil {
		_ = l.dst.Close()
	}
	if !l.dialing {
		close(l.closed)
	}
	l.mu.Unlock()

	r := reason()
	l.mu.Lock()
	l.reason = r
	l.mu.Unlock()
	close(l.settled)
	l.cancel(errEnded)
	return true
}

// cause is the reason a link whose context is done was ended, or ErrLinksOff when it is done
// because the listener is shutting down.
func (l *link) cause() error {
	l.mu.Lock()
	ended := l.ended
	l.mu.Unlock()
	if !ended {
		return links.ErrLinksOff
	}
	<-l.settled
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.reason
}

// finish closes what is left, gives the admission back and writes the last line.
func (l *link) finish() {
	l.end(nil)
	<-l.settled
	l.cancel(errEnded)
	l.watching.Wait()
	l.p.untrack(l)

	l.mu.Lock()
	admitted, adm, open, reason := l.admitted, l.adm, l.open, l.reason
	l.mu.Unlock()
	if admitted {
		l.p.cfg.Admitter.Release(adm)
	}
	if open {
		l.p.write(l.entry("close", reason, false))
		return
	}
	if reason == nil {
		reason = links.ErrLinksOff
	}
	l.p.write(l.entry("refused", reason, false))
}

// resolved records the destination, unless the link has already been aborted.
func (l *link) resolved(target string, port int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ended {
		return false
	}
	l.target, l.port = target, port
	return true
}

func (l *link) admission() links.Admission {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.adm
}

func (l *link) entry(event string, reason error, woke bool) audit.LinkEntry {
	l.mu.Lock()
	target, port, opened := l.target, l.port, l.opened
	l.mu.Unlock()
	if port == 0 {
		port = int(l.peer.Dst.Port())
	}
	now := l.p.cfg.Now()
	e := audit.LinkEntry{
		Time:   now.UTC(),
		Event:  event,
		From:   name(l.peer.Source),
		FromID: l.peer.Source,
		To:     name(target),
		ToID:   target,
		Port:   port,
		Woke:   woke,
	}
	if reason != nil {
		e.Reason = reason.Error()
	}
	switch event {
	case "refused":
		e.Count = 1
	case "close":
		e.BytesIn = l.in.Load()
		e.BytesOut = l.out.Load()
		e.SecondsOpen = int64(now.Sub(opened) / time.Second)
	}
	return e
}

// refused writes the line for a connection refused before it became a link.
func (p *Proxy) refused(peer Peer, target string, reason error) {
	p.write(audit.LinkEntry{
		Time:   p.cfg.Now().UTC(),
		Event:  "refused",
		From:   name(peer.Source),
		FromID: peer.Source,
		To:     name(target),
		ToID:   target,
		Port:   int(peer.Dst.Port()),
		Reason: reason.Error(),
		Count:  1,
	})
}

// name is what the log shows for an endpoint. The listener knows machines only by sandbox ID.
func name(id string) string {
	if id == "" {
		return "?"
	}
	return id
}
