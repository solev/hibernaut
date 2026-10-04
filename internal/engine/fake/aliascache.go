package fake

import "time"

// E2B's alias cache keeps an entry for 5 minutes. A lookup of one more than a minute old returns
// it, then refreshes it from the database, which stamps a miss anew.
const (
	aliasCacheTTL     = 5 * time.Minute
	aliasCacheRefresh = time.Minute
)

// WithAliasCache makes alias lookups miss the way E2B's do. Once a lookup of an alias finds
// nothing, Template, Create and SaveTemplate miss it for 5 minutes, even if it appears. A lookup
// more than a minute after the miss still misses, but looks again, so the next one finds the
// alias, or misses it for 5 more minutes. A successful SaveTemplate or BuildTemplate of the
// alias, or DeleteTemplate of its template, ends that at once. The fake's clock times it.
func WithAliasCache() Option {
	return func(e *Engine) { e.aliasCache = true }
}

// HasAlias reports whether alias exists, whatever the alias cache hides. It's for tests.
func (e *Engine) HasAlias(alias string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.aliases[alias]
	return ok
}

// lookupAlias finds alias as E2B's alias cache does, under e.mu.
func (e *Engine) lookupAlias(alias string) *template {
	t := e.aliases[alias]
	if !e.aliasCache {
		return t
	}
	now := e.now()
	if missed, ok := e.missedAt[alias]; ok && now.Sub(missed) < aliasCacheTTL {
		if now.Sub(missed) > aliasCacheRefresh {
			e.rememberLookup(alias, t, now)
		}
		return nil
	}
	e.rememberLookup(alias, t, now)
	return t
}

func (e *Engine) rememberLookup(alias string, found *template, now time.Time) {
	if found != nil {
		delete(e.missedAt, alias)
		return
	}
	e.missedAt[alias] = now
}
