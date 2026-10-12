package linkadmit

import (
	"context"
	"errors"
	"fmt"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/links"
)

// verdict is what the sweep found about one machine.
type verdict struct {
	run links.Run
	// end is why its links must end, if they must.
	end error
	// unknown is set when the engine could not be read, which leaves its links alone until
	// the next sweep.
	unknown bool
}

// sweep is the backstop behind the run watcher and the control socket. Every SweepEvery it
// cancels each established link one of whose ends is gone, is not Running, has another run,
// is fenced, or whose port has been unpublished or republished since it was admitted. It asks
// for Running rather than comparing StartedAt, because a pause leaves StartedAt alone.
func (a *Admitter) sweep(ctx context.Context) {
	a.mu.Lock()
	var held []*adm
	for p := range a.adms {
		if p.established {
			held = append(held, p)
		}
	}
	now := a.now()
	for id, t := range a.lastWake {
		if now.Sub(t) >= links.WakeEvery {
			delete(a.lastWake, id)
		}
	}
	for id, t := range a.lastRenew {
		if now.Sub(t) >= links.LinkRenew {
			delete(a.lastRenew, id)
		}
	}
	a.mu.Unlock()
	if len(held) == 0 {
		return
	}

	seen := map[string]verdict{}
	check := func(id string) verdict {
		v, ok := seen[id]
		if !ok {
			v = a.checkMachine(ctx, id)
			seen[id] = v
		}
		return v
	}
	ends := make(map[*adm]error, len(held))
	for _, p := range held {
		if err := checkEnds(p, check(p.source), check(p.target)); err != nil {
			ends[p] = err
		}
	}
	if doc, err := a.cfg.Store.Read(); err == nil {
		for _, p := range held {
			if ends[p] != nil {
				continue
			}
			if gen, err := checkDoc(doc, p.source, p.target, p.port); err != nil {
				if errors.Is(err, links.ErrNotPublished) {
					err = fmt.Errorf("%w: %w", links.ErrRevoked, err)
				}
				ends[p] = err
			} else if gen != p.gen {
				ends[p] = fmt.Errorf("%w: %s:%d was republished", links.ErrRevoked, p.target, p.port)
			}
		}
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	for p, err := range ends {
		if !p.released {
			p.cancel(err)
		}
	}
}

func checkEnds(p *adm, source, target verdict) error {
	for _, e := range []struct {
		v   verdict
		id  string
		run links.Run
	}{{source, p.source, p.sourceRun}, {target, p.target, p.targetRun}} {
		switch {
		case e.v.unknown:
		case e.v.end != nil:
			return e.v.end
		case e.v.run != e.run:
			return fmt.Errorf("%w: %s has another run", links.ErrRunEnded, e.id)
		}
	}
	return nil
}

// checkMachine reads id's engine state and its run now. A run it cannot read counts as ended.
func (a *Admitter) checkMachine(ctx context.Context, id string) verdict {
	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	m, err := a.cfg.Engine.Get(cctx, id)
	switch {
	case errors.Is(err, engine.ErrNotFound):
		return verdict{end: fmt.Errorf("%w: %s is gone", links.ErrRunEnded, id)}
	case err != nil:
		return verdict{unknown: true}
	case m.State != engine.Running:
		return verdict{end: fmt.Errorf("%w: %s is %s", links.ErrRunEnded, id, m.State)}
	}
	run, err := a.cfg.Runs.Current(id)
	if err != nil {
		return verdict{end: fmt.Errorf("%w: %s: %w", links.ErrRunEnded, id, err)}
	}
	return verdict{run: run}
}
