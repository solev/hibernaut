package linkproxy

import (
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/solev/hibernaut/internal/links"
)

// limiter is the accept rate, a token bucket per source machine.
type limiter struct {
	rate  float64
	burst float64

	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	at     time.Time
}

// allow takes one token from id's bucket, and reports false when it is empty.
func (r *limiter) allow(id string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.buckets == nil {
		r.buckets = map[string]*bucket{}
	}
	b := r.buckets[id]
	if b == nil {
		if len(r.buckets) >= 64 {
			r.prune(now)
		}
		b = &bucket{tokens: r.burst, at: now}
		r.buckets[id] = b
	}
	r.fill(b, now)
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (r *limiter) fill(b *bucket, now time.Time) {
	if gap := now.Sub(b.at); gap > 0 {
		b.tokens = min(r.burst, b.tokens+gap.Seconds()*r.rate)
		b.at = now
	}
}

// prune forgets the buckets that have filled up again, which are the same as no bucket.
func (r *limiter) prune(now time.Time) {
	for id, b := range r.buckets {
		r.fill(b, now)
		if b.tokens >= r.burst {
			delete(r.buckets, id)
		}
	}
}

// renewals keeps renewal to at most one per source and target per LinkRenew, however many
// links the pair has and however much payload they move. The window is the pair's rather than
// the target's because the admitter refuses a stale source without spending the target's own
// window: a stale source chatting constantly would otherwise open every window first and hold
// a fresh source's payload back until the target paused.
type renewals struct {
	mu    sync.Mutex
	pairs map[pair]*renewal
}

type pair struct {
	source, target string
}

type renewal struct {
	at      time.Time
	pending int64
}

// renew reports n payload bytes on the link admitted as a. It renews at once when the pair has
// not been renewed within LinkRenew, carrying the bytes that moved since its last renewal, and
// never while the host is quiescing.
func (p *Proxy) renew(a links.Admission, n int64) {
	now := p.cfg.Now()
	r := &p.renewals
	key := pair{a.Source, a.Target}
	r.mu.Lock()
	if r.pairs == nil {
		r.pairs = map[pair]*renewal{}
	}
	t := r.pairs[key]
	if t == nil {
		if len(r.pairs) >= 64 {
			for k, old := range r.pairs {
				if now.Sub(old.at) >= links.LinkRenew {
					delete(r.pairs, k)
				}
			}
		}
		t = &renewal{}
		r.pairs[key] = t
	}
	t.pending += n
	if !t.at.IsZero() && now.Sub(t.at) < links.LinkRenew {
		r.mu.Unlock()
		return
	}
	payload := t.pending
	t.pending, t.at = 0, now
	r.mu.Unlock()
	if p.cfg.Quiescing() {
		return
	}
	p.cfg.Admitter.Renew(a, payload)
}

// maxDiagnostics bounds the Logf lines remembered for folding, and with them the lines one
// FoldWindow can write.
const maxDiagnostics = 64

// diagnostics folds identical Logf lines, so a source in a retry loop or a scan of the port from
// outside any machine writes one line per FoldWindow rather than one per connection.
type diagnostics struct {
	mu   sync.Mutex
	seen map[diagKey]time.Time
}

type diagKey struct {
	step, source, err string
}

// allow reports whether the line for k may be written at now, and remembers it if so. Once
// maxDiagnostics different lines have been written within one FoldWindow, the rest are dropped
// until the oldest of them expire.
func (d *diagnostics) allow(k diagKey, now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.seen == nil {
		d.seen = map[diagKey]time.Time{}
	}
	at, ok := d.seen[k]
	if ok && now.Sub(at) < links.FoldWindow {
		return false
	}
	if !ok && len(d.seen) >= maxDiagnostics {
		for old, at := range d.seen {
			if now.Sub(at) >= links.FoldWindow {
				delete(d.seen, old)
			}
		}
		if len(d.seen) >= maxDiagnostics {
			return false
		}
	}
	d.seen[k] = now
	return true
}

// diagnose writes err, the real error behind a refusal or an abort at step, through Logf.
func (p *Proxy) diagnose(step string, peer Peer, target string, open bool, err error) {
	if p.cfg.Logf == nil || !p.diags.allow(diagKey{step, peer.Source, err.Error()}, p.cfg.Now()) {
		return
	}
	what := "refused a connection"
	if open {
		what = "ended a link"
	}
	from := name(peer.Source)
	if peer.SourceAddr.IsValid() {
		from += " (" + peer.SourceAddr.String() + ")"
	}
	to := target
	if to == "" && peer.Dst.IsValid() {
		to = peer.Dst.Addr().String()
	}
	to = name(to)
	if peer.Dst.Port() != 0 {
		to += ":" + strconv.Itoa(int(peer.Dst.Port()))
	}
	p.cfg.Logf("links: %s from %s to %s at %s: %v", what, from, to, step, err)
}

// reasons are the errors a refusal or an abort can name in the link log.
var reasons = []error{
	links.ErrLinksOff, links.ErrNotPublished, links.ErrReservedPort, links.ErrUnknownAddress,
	links.ErrSelfLink, links.ErrSourceIdle, links.ErrPausedByHand, links.ErrFenced,
	links.ErrQuiescing, links.ErrNoRoom, links.ErrRateLimited, links.ErrTooManyLinks,
	links.ErrRevoked, links.ErrRunEnded, links.ErrLeaseExpired, links.ErrNotRunning,
	links.ErrDialFailed, links.ErrGuardDown, links.ErrTokensSpent, links.ErrUnmarked,
	links.ErrWrongRun, links.ErrStateUnreadable, links.ErrFull,
}

// known returns the shared error err is, or nil.
func known(err error) error {
	if err == nil {
		return nil
	}
	for _, r := range reasons {
		if errors.Is(err, r) {
			return r
		}
	}
	return nil
}

// reasonFor turns an abort's reason, which arrives as text over the control socket, back into
// the error it names. Empty text is fallback.
func reasonFor(text string, fallback error) error {
	if text == "" {
		return fallback
	}
	for _, r := range reasons {
		if r.Error() == text {
			return r
		}
	}
	return errors.New(text)
}
