package linkguard

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"syscall"

	"github.com/solev/hibernaut/internal/links"
)

// held is a gate the Keeper extends.
type held struct {
	id      string
	run     links.Run
	gate    links.Gate
	retired chan struct{}
}

// outcome is what a cycle found, kept so Gate can say why a run has no gate.
type outcome struct {
	// err applies to every machine, as when the live runs could not be read.
	err  error
	live map[string]links.Run
	// why is the reason each live run that has no gate was given none.
	why map[string]error
}

var closedChan = func() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}()

// extendTries bounds the ENOENT path: each try retires at least one vanished gate, and a
// batch that keeps failing after that is a failure of its own.
const extendTries = 3

// verdict is Gate's answer once a cycle that started after the call is over. k.mu is held.
func (k *Keeper) verdict(id string, run links.Run) (links.Gate, error) {
	if h := k.held[id]; h != nil && h.run == run {
		return h.gate, nil
	}
	switch {
	case k.down:
		return links.Gate{}, links.ErrGuardDown
	case k.last.err != nil:
		return links.Gate{}, k.last.err
	case k.last.live[id] != run:
		return links.Gate{}, links.ErrRunEnded
	case k.last.why[id] != nil:
		return links.Gate{}, k.last.why[id]
	}
	// Live and not refused, but no gate: a failure took the guard down and back up in the
	// cycle.
	return links.Gate{}, links.ErrGuardDown
}

// stop retires every gate and wakes every waiter for good.
func (k *Keeper) stop() {
	k.retireAll("the Keeper stopped")
	k.mu.Lock()
	k.over, k.down = true, true
	k.mu.Unlock()
	close(k.stopped)
}

func (k *Keeper) isDown() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.down
}

// cycle runs one refresh and publishes its outcome.
func (k *Keeper) cycle() {
	k.mu.Lock()
	k.started++
	n := k.started
	k.mu.Unlock()

	o := k.refresh()

	k.mu.Lock()
	k.done = n
	k.last = o
	close(k.cycled)
	k.cycled = make(chan struct{})
	k.mu.Unlock()
}

// refresh extends the gates that are still justified, retires the rest and opens the missing
// ones, in that order, so a slow open never delays an extension.
func (k *Keeper) refresh() outcome {
	if k.isDown() {
		return outcome{err: links.ErrGuardDown}
	}
	live, err := k.cfg.Runs.Live()
	if err != nil {
		k.retireAll(fmt.Sprintf("the live runs cannot be read: %v", err))
		return outcome{err: fmt.Errorf("linkguard: read the live runs: %w", err)}
	}
	fenced := k.fences()

	var keep []links.Gate
	k.mu.Lock()
	hs := slices.Collect(maps.Values(k.held))
	k.mu.Unlock()
	for _, h := range hs {
		switch {
		case live[h.id] != h.run:
			k.retire(h, "")
		case fenced(h.id):
			k.retire(h, "its machine is fenced")
		default:
			keep = append(keep, h.gate)
		}
	}
	slices.SortFunc(keep, func(a, b links.Gate) int { return a.Addr.Compare(b.Addr) })
	if !k.extend(keep) {
		return outcome{err: links.ErrGuardDown}
	}

	o := outcome{live: live, why: map[string]error{}}
	opened := map[string]*held{}
	for _, id := range slices.Sorted(maps.Keys(live)) {
		run := live[id]
		k.mu.Lock()
		h, spent := k.held[id], k.spent
		k.mu.Unlock()
		switch {
		case h != nil:
			continue
		case fenced(id):
			o.why[id] = links.ErrFenced
			continue
		case spent:
			o.why[id] = links.ErrTokensSpent
			continue
		}
		gate, err := k.open(id, run)
		if errors.Is(err, links.ErrTokensSpent) {
			k.mu.Lock()
			k.spent = true
			k.mu.Unlock()
			k.cfg.Logf("linkguard: out of link tokens; no new gate opens until the host reboots")
		}
		if err != nil {
			o.why[id] = err
			continue
		}
		opened[id] = &held{id: id, run: run, gate: gate, retired: make(chan struct{})}
	}
	k.recheck(opened, o.why)
	return o
}

// fences returns the machines whose gates must be shut: every fenced machine, whether its
// owner is live, orphaned or cannot be checked, and every machine at all when links.json
// cannot be read.
func (k *Keeper) fences() func(id string) bool {
	doc, err := k.cfg.Store.Read()
	if err != nil {
		return func(string) bool { return true }
	}
	fenced := map[string]bool{}
	for id, m := range doc.Machines {
		if state, _ := m.Fenced(k.cfg.Alive); state != links.NotFenced {
			fenced[id] = true
		}
	}
	return func(id string) bool { return fenced[id] }
}

// extend refreshes gates. On ENOENT it checks the table, retires the gates that have vanished
// and sends the rest again. It reports false when it has had to take the guard down.
func (k *Keeper) extend(gates []links.Gate) bool {
	for range extendTries {
		err := k.cfg.Guard.Extend(gates)
		if err == nil {
			return true
		}
		if !errors.Is(err, syscall.ENOENT) {
			k.fail(fmt.Sprintf("extending the gates failed: %v", err))
			return false
		}
		ok, err := k.cfg.Guard.Installed()
		if err != nil || !ok {
			k.fail(fmt.Sprintf("a gate refresh found the table changed (installed %t, %v)", ok, err))
			return false
		}
		var still []links.Gate
		for _, g := range gates {
			has, err := k.cfg.Guard.Has(g)
			if err != nil {
				k.fail(fmt.Sprintf("reading gate %s token %d failed: %v", g.Addr, g.Token, err))
				return false
			}
			if has {
				still = append(still, g)
				continue
			}
			k.mu.Lock()
			h := k.byGate[g]
			k.mu.Unlock()
			if h != nil {
				k.retire(h, "it vanished from the kernel")
			}
		}
		gates = still
	}
	k.fail(fmt.Sprintf("gate refreshes kept failing with ENOENT after %d tries", extendTries))
	return false
}

// open gives a live, unfenced run of id a new gate: it resolves the run's address, checks it
// back, drops whatever is left there, takes a token and opens.
func (k *Keeper) open(id string, run links.Run) (links.Gate, error) {
	addr, at, err := k.cfg.Resolver.Addr(id)
	if errors.Is(err, links.ErrNotRunning) {
		return links.Gate{}, links.ErrRunEnded
	}
	if err != nil {
		return links.Gate{}, fmt.Errorf("linkguard: find the slot of %s: %w", id, err)
	}
	if at != run {
		return links.Gate{}, links.ErrRunEnded
	}
	slot, err := k.cfg.Resolver.At(addr)
	if err != nil {
		return links.Gate{}, fmt.Errorf("linkguard: check the slot at %s: %w", addr, err)
	}
	if slot.ID != id || slot.Run != run {
		return links.Gate{}, fmt.Errorf("linkguard: %s run %s resolved to %s, which holds %s run %s",
			id, run, addr, slot.ID, slot.Run)
	}
	if err := k.cfg.Guard.Drop(addr); err != nil {
		return links.Gate{}, err
	}
	token, err := k.cfg.Tokens.Next()
	if err != nil {
		return links.Gate{}, err
	}
	gate := links.Gate{Addr: addr, Token: token}
	if err := k.cfg.Guard.Open(gate); err != nil {
		return links.Gate{}, err
	}
	return gate, nil
}

// recheck reads the runs and fences again after opening and shuts any new gate whose run has
// ended or been fenced in the meantime. The rest become held.
func (k *Keeper) recheck(opened map[string]*held, why map[string]error) {
	if len(opened) == 0 {
		return
	}
	live, err := k.cfg.Runs.Live()
	fenced := k.fences()
	for _, id := range slices.Sorted(maps.Keys(opened)) {
		h := opened[id]
		switch {
		case err != nil:
			why[id] = fmt.Errorf("linkguard: read the live runs: %w", err)
		case live[id] != h.run:
			why[id] = links.ErrRunEnded
		case fenced(id):
			why[id] = links.ErrFenced
		default:
			k.mu.Lock()
			k.held[id] = h
			k.byGate[h.gate] = h
			k.mu.Unlock()
			continue
		}
		if derr := k.cfg.Guard.Drop(h.gate.Addr); derr != nil {
			k.cfg.Logf("linkguard: shut the new gate of %s at %s, which changed while it opened: %v; it expires within %v",
				id, h.gate.Addr, derr, links.GuardTTL)
		}
	}
}

// retire stops extending h and closes its Retired channel. why is logged, and an empty why is
// a run that ended, which is routine.
func (k *Keeper) retire(h *held, why string) {
	k.mu.Lock()
	if k.held[h.id] != h {
		k.mu.Unlock()
		return
	}
	delete(k.held, h.id)
	delete(k.byGate, h.gate)
	close(h.retired)
	k.mu.Unlock()
	if why != "" {
		k.cfg.Logf("linkguard: retired the gate of %s at %s: %s", h.id, h.gate.Addr, why)
	}
}

func (k *Keeper) retireAll(why string) {
	k.mu.Lock()
	hs := slices.Collect(maps.Values(k.held))
	k.mu.Unlock()
	if len(hs) == 0 {
		return
	}
	ids := make([]string, 0, len(hs))
	for _, h := range hs {
		k.retire(h, "")
		ids = append(ids, h.id)
	}
	slices.Sort(ids)
	k.cfg.Logf("linkguard: retired every gate (%s): %s", strings.Join(ids, ", "), why)
}

// fail takes the guard down: every gate retired and Gate refusing, then an immediate
// reinstall. A reinstall that fails is retried every SelfCheck.
func (k *Keeper) fail(why string) {
	k.retireAll(why)
	k.mu.Lock()
	k.down = true
	k.mu.Unlock()
	k.cfg.Logf("linkguard: the guard is down: %s", why)
	k.reinstall()
}

func (k *Keeper) reinstall() {
	if err := k.cfg.Guard.Install(); err != nil {
		k.cfg.Logf("linkguard: reinstalling the guard failed, retrying in %v: %v", k.cfg.SelfCheck, err)
		return
	}
	k.mu.Lock()
	k.down = false
	k.mu.Unlock()
	k.cfg.Logf("linkguard: reinstalled the guard")
}

// selfCheck reinstalls a guard that is down and takes down one that is no longer installed.
func (k *Keeper) selfCheck() {
	if k.isDown() {
		k.reinstall()
		return
	}
	ok, err := k.cfg.Guard.Installed()
	switch {
	case err != nil:
		k.fail(fmt.Sprintf("the self-check failed: %v", err))
	case !ok:
		k.fail("the self-check found the table or net.ipv4.tcp_fwmark_accept changed")
	}
}
