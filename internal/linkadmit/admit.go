package linkadmit

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/links"
	"github.com/solev/hibernaut/internal/machines"
)

const (
	// runPoll is how often an admission looks for the run a wake has just started. The cgroup
	// appears a moment after the resume returns.
	runPoll = 50 * time.Millisecond
	// callTimeout bounds one engine call made in the background.
	callTimeout = 10 * time.Second
)

// adm is one admission, from its reservation to its Release. The Admission's Ctx carries it,
// which is how Renew and Release find it again.
type adm struct {
	source, target       string
	sourceRun, targetRun links.Run
	port                 int
	gen                  uint64
	// cancel cancels the admission's current context: Admit's while it is pending, the
	// Admission's Ctx once it is established. Both are switched under the admitter's mutex.
	cancel      context.CancelCauseFunc
	established bool
	released    bool
}

type admKey struct{}

// wakeup is one wake in flight, shared by every admission waiting for the same target.
type wakeup struct {
	done chan struct{}
	err  error
	// waiters is how many pending admissions wait on it. When the last one gives up, the
	// wake is cancelled.
	waiters int
	cancel  context.CancelFunc
	// need is the room the wake holds, counted in default machines.
	need int
	// before is the rate history the wake replaced, put back if it fails.
	before    time.Time
	hadBefore bool
}

// Admit reserves room and caps, wakes the target if it is paused, and returns an Admission.
func (a *Admitter) Admit(ctx context.Context, req links.AdmitRequest) (links.Admission, error) {
	if !idRE.MatchString(req.Source) || !idRE.MatchString(req.Target) {
		return links.Admission{}, fmt.Errorf("bad machine ID in %q -> %q", req.Source, req.Target)
	}
	if req.Port < 1 || req.Port > 65535 {
		return links.Admission{}, fmt.Errorf("%w: port %d", links.ErrNotPublished, req.Port)
	}
	pctx, pcancel := context.WithCancelCause(ctx)
	defer pcancel(context.Canceled)

	p, w, woke, lease, err := a.reserve(req, pcancel)
	if err != nil {
		return links.Admission{}, err
	}
	ad, err := a.complete(pctx, p, w, woke, req)
	if err != nil {
		a.drop(p)
		return links.Admission{}, err
	}
	ad.LeaseUntil = lease
	return ad, nil
}

// reserve is the one atomic decision: everything that can refuse a link before any waiting,
// and the reservation of caps, rate and room, all under the mutex. It starts the wake, or joins
// the one already in flight for the same target.
func (a *Admitter) reserve(req links.AdmitRequest, cancel context.CancelCauseFunc) (*adm, *wakeup, bool, time.Time, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	fail := func(err error) (*adm, *wakeup, bool, time.Time, error) {
		return nil, nil, false, time.Time{}, err
	}
	if a.quiescing() {
		return fail(links.ErrQuiescing)
	}
	if req.Source == req.Target {
		return fail(links.ErrSelfLink)
	}
	doc, err := a.cfg.Store.Read()
	if err != nil {
		return fail(fmt.Errorf("%w: %w", links.ErrStateUnreadable, err))
	}
	gen, err := checkDoc(doc, req.Source, req.Target, req.Port)
	if err != nil {
		return fail(err)
	}
	if tm := doc.Machines[req.Target]; tm != nil && tm.PausedByHand {
		return fail(fmt.Errorf("%w: %s", links.ErrPausedByHand, req.Target))
	}
	now := a.now()
	if !links.Fresh(a.cfg.Clock, req.Source, now) {
		return fail(fmt.Errorf("%w: %s", links.ErrSourceIdle, req.Source))
	}
	lease := links.LeaseUntil(a.cfg.Clock, req.Source, now)
	if run, err := a.cfg.Runs.Current(req.Source); err != nil || run != req.SourceRun {
		return fail(fmt.Errorf("%w: source %s", links.ErrRunEnded, req.Source))
	}
	switch {
	case a.perSource[req.Source] >= links.MaxPerSource:
		return fail(fmt.Errorf("%w: %s has %d links open", links.ErrTooManyLinks, req.Source, links.MaxPerSource))
	case a.perTarget[req.Target] >= links.MaxPerTarget:
		return fail(fmt.Errorf("%w: %s has %d links open", links.ErrTooManyLinks, req.Target, links.MaxPerTarget))
	case a.total >= links.MaxLinks:
		return fail(fmt.Errorf("%w: the host has %d links open", links.ErrTooManyLinks, links.MaxLinks))
	}

	p := &adm{source: req.Source, target: req.Target, sourceRun: req.SourceRun, port: req.Port, gen: gen, cancel: cancel}
	w, woke := a.waking[req.Target], false
	if w == nil {
		run, err := a.cfg.Runs.Current(req.Target)
		switch {
		case err == nil:
			p.targetRun = run
		case !errors.Is(err, links.ErrNotRunning):
			return fail(fmt.Errorf("target %s: %w", req.Target, err))
		default:
			if w, err = a.startWake(req.Target, now, lease); err != nil {
				return fail(err)
			}
			woke = true
		}
	}
	if w != nil {
		w.waiters++
	}
	a.perSource[p.source]++
	a.perTarget[p.target]++
	a.total++
	a.adms[p] = struct{}{}
	return p, w, woke, lease, nil
}

// startWake charges the rate limit and the room target takes, and resumes target in the
// background. The caller holds mu.
func (a *Admitter) startWake(target string, now, lease time.Time) (*wakeup, error) {
	before, hadBefore := a.lastWake[target]
	if hadBefore && now.Sub(before) < links.WakeEvery {
		return nil, fmt.Errorf("%w: %s woke %v ago", links.ErrRateLimited, target, now.Sub(before).Round(time.Second))
	}
	if a.cfg.Room == nil {
		return nil, fmt.Errorf("%w: nothing reports the host's room", links.ErrNoRoom)
	}
	free, need, err := a.cfg.Room(target)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", links.ErrNoRoom, err)
	}
	need = max(need, 1)
	if free-a.pendingWakes < need {
		return nil, fmt.Errorf("%w: waking %s would leave less than the reserve", links.ErrNoRoom, target)
	}
	wctx, cancel := context.WithTimeout(context.Background(), links.WakeTimeout)
	w := &wakeup{done: make(chan struct{}), cancel: cancel, need: need, before: before, hadBefore: hadBefore}
	a.lastWake[target] = now
	a.pendingWakes += need
	a.waking[target] = w
	go a.runWake(wctx, w, target, lease)
	return w, nil
}

func (a *Admitter) runWake(ctx context.Context, w *wakeup, target string, lease time.Time) {
	_, err := a.wake(ctx, target, lease)
	w.cancel()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pendingWakes -= w.need
	if a.waking[target] == w {
		delete(a.waking, target)
	}
	if err != nil {
		if w.hadBefore {
			a.lastWake[target] = w.before
		} else {
			delete(a.lastWake, target)
		}
	}
	w.err = err
	close(w.done)
}

// await waits for w on behalf of one admission. The wake is cancelled once nobody waits on it.
func (a *Admitter) await(ctx context.Context, w *wakeup) error {
	defer func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		if w.waiters--; w.waiters == 0 {
			w.cancel()
		}
	}()
	select {
	case <-w.done:
	case <-ctx.Done():
	}
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	return w.err
}

// complete waits for the wake, finds the target's run and gate, checks again what may have
// changed meanwhile and turns the reservation into an established admission.
func (a *Admitter) complete(ctx context.Context, p *adm, w *wakeup, woke bool, req links.AdmitRequest) (links.Admission, error) {
	if w != nil {
		if err := a.await(ctx, w); err != nil {
			return links.Admission{}, err
		}
	}
	bctx, cancel := context.WithTimeout(ctx, links.AdmitTimeout)
	defer cancel()
	fail := func(err error) (links.Admission, error) {
		if cause := context.Cause(ctx); cause != nil {
			return links.Admission{}, cause
		}
		return links.Admission{}, err
	}
	if woke || p.targetRun == "" {
		run, err := a.waitRun(bctx, p.target)
		if err != nil {
			return fail(err)
		}
		p.targetRun = run
		if !links.Fresh(a.cfg.Clock, p.source, a.now()) {
			return fail(fmt.Errorf("%w: %s's lease passed during the wake", links.ErrLeaseExpired, p.source))
		}
	}
	doc, err := a.cfg.Store.Read()
	if err != nil {
		return fail(fmt.Errorf("%w: %w", links.ErrStateUnreadable, err))
	}
	if gen, err := checkDoc(doc, p.source, p.target, p.port); err != nil {
		return fail(err)
	} else if gen != p.gen {
		return fail(fmt.Errorf("%w: %s:%d was republished", links.ErrRevoked, p.target, p.port))
	}
	gate, err := a.cfg.Keeper.Gate(bctx, p.target, p.targetRun)
	if err != nil {
		return fail(fmt.Errorf("target %s's gate: %w", p.target, err))
	}
	sourceEnded, err := a.cfg.Runs.Watch(p.source, p.sourceRun)
	if err != nil {
		return fail(fmt.Errorf("watching %s: %w", p.source, err))
	}
	targetEnded, err := a.cfg.Runs.Watch(p.target, p.targetRun)
	if err != nil {
		return fail(fmt.Errorf("watching %s: %w", p.target, err))
	}

	lctx, lcancel := context.WithCancelCause(context.WithValue(context.Background(), admKey{}, p))
	a.mu.Lock()
	if cause := context.Cause(ctx); cause != nil {
		a.mu.Unlock()
		lcancel(cause)
		return links.Admission{}, cause
	}
	p.cancel = lcancel
	p.established = true
	a.mu.Unlock()
	go func() {
		select {
		case <-sourceEnded:
			lcancel(fmt.Errorf("%w: source %s", links.ErrRunEnded, p.source))
		case <-targetEnded:
			lcancel(fmt.Errorf("%w: target %s", links.ErrRunEnded, p.target))
		case <-lctx.Done():
		}
	}()
	return links.Admission{
		Source: p.source, Target: p.target,
		SourceRun: p.sourceRun, TargetRun: p.targetRun,
		SourceGate: req.SourceGate, TargetGate: gate,
		Port: p.port, PortGen: p.gen,
		Woke: woke,
		Ctx:  lctx,
	}, nil
}

// waitRun waits for target's run to show in its cgroups, which happens a moment after a resume.
func (a *Admitter) waitRun(ctx context.Context, target string) (links.Run, error) {
	t := time.NewTicker(runPoll)
	defer t.Stop()
	for {
		run, err := a.cfg.Runs.Current(target)
		if err == nil {
			return run, nil
		}
		if !errors.Is(err, links.ErrNotRunning) {
			return "", fmt.Errorf("target %s: %w", target, err)
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("target %s has no run: %w", target, links.ErrNotRunning)
		case <-t.C:
		}
	}
}

// checkDoc refuses a link whose port is not published or either of whose ends is fenced, and
// returns the port's generation. Every fence refuses, live or orphaned.
func checkDoc(doc links.Doc, source, target string, port int) (uint64, error) {
	tm := doc.Machines[target]
	gen, ok := tm.Gen(port)
	if !ok {
		return 0, fmt.Errorf("%w: %s:%d", links.ErrNotPublished, target, port)
	}
	for _, id := range []string{source, target} {
		if m := doc.Machines[id]; m != nil && m.Fence != nil {
			return 0, fmt.Errorf("%w: %s (%s)", links.ErrFenced, id, m.Fence.Why)
		}
	}
	return gen, nil
}

// drop gives back p's caps and forgets it. It is idempotent.
func (a *Admitter) drop(p *adm) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if p.released {
		return
	}
	p.released = true
	delete(a.adms, p)
	uncount(a.perSource, p.source)
	uncount(a.perTarget, p.target)
	a.total--
}

func uncount(m map[string]int, id string) {
	if m[id]--; m[id] <= 0 {
		delete(m, id)
	}
}

func admOf(ad links.Admission) *adm {
	if ad.Ctx == nil {
		return nil
	}
	p, _ := ad.Ctx.Value(admKey{}).(*adm)
	return p
}

// Release gives back the caps. A wake gives back its room when it ends, not here. Release keeps
// the rate history of a successful wake, so a link opened and closed in a loop cannot wake a
// machine every second.
func (a *Admitter) Release(ad links.Admission) {
	p := admOf(ad)
	if p == nil {
		return
	}
	a.drop(p)
	a.mu.Lock()
	cancel := p.cancel
	a.mu.Unlock()
	cancel(context.Canceled)
}

// Renew pushes the target's idle timeout back for payload bytes. It re-derives the lease from
// the activity clock on every call rather than reading ad.LeaseUntil, so a source still being
// worked on keeps renewing, and a source whose own non-link activity has gone stale stops, after
// which the target pauses on its own idle timeout. It never cuts a link, and link bytes never
// feed the clock.
func (a *Admitter) Renew(ad links.Admission, payload int64) {
	p := admOf(ad)
	if payload <= 0 || p == nil || a.quiescing() {
		return
	}
	now := a.now()
	a.mu.Lock()
	last, ok := a.lastRenew[p.target]
	a.mu.Unlock()
	if ok && now.Sub(last) < links.LinkRenew {
		return
	}
	if !links.Fresh(a.cfg.Clock, p.source, now) {
		return
	}
	a.mu.Lock()
	if last, ok := a.lastRenew[p.target]; ok && now.Sub(last) < links.LinkRenew {
		a.mu.Unlock()
		return
	}
	a.lastRenew[p.target] = now
	a.mu.Unlock()
	go a.extend(p.target, links.LeaseUntil(a.cfg.Clock, p.source, now), now)
}

// extend pushes a running target's timeout to its idle time from now. A target that never
// pauses has nothing to push, unless a link woke it under a lease: then it is held to the
// lease, which is re-derived each time.
func (a *Admitter) extend(target string, lease, now time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	em, err := a.cfg.Engine.Get(ctx, target)
	if err != nil || em.State != engine.Running {
		return
	}
	idle, ok := machines.Idle(em)
	if !ok {
		return
	}
	if idle == 0 {
		idle = lease.Sub(now)
		if idle <= 0 {
			return
		}
	}
	_ = machines.Renew(ctx, a.cfg.Engine, em, idle, now)
}
