package linkproxy

import (
	"errors"
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

// renewals keeps renewal to at most one per target per LinkRenew, however many links it has and
// however much payload they move.
type renewals struct {
	mu      sync.Mutex
	targets map[string]*renewal
}

type renewal struct {
	at      time.Time
	pending int64
}

// renew reports n payload bytes on the link admitted as a. It renews at once when the target
// has not been renewed within LinkRenew, carrying the bytes that moved since the last renewal,
// and never while the host is quiescing.
func (p *Proxy) renew(a links.Admission, n int64) {
	now := p.cfg.Now()
	r := &p.renewals
	r.mu.Lock()
	if r.targets == nil {
		r.targets = map[string]*renewal{}
	}
	t := r.targets[a.Target]
	if t == nil {
		if len(r.targets) >= 64 {
			for id, old := range r.targets {
				if now.Sub(old.at) >= links.LinkRenew {
					delete(r.targets, id)
				}
			}
		}
		t = &renewal{}
		r.targets[a.Target] = t
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
