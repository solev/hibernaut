package failurelog

import "time"

const interval = 10 * time.Minute

type Limiter struct {
	last *time.Time
	more uint64
}

func (l *Limiter) Failure(now time.Time) (more uint64, log bool) {
	if l.last != nil && now.Sub(*l.last) < interval {
		l.more++
		return 0, false
	}
	more = l.more
	l.last, l.more = &now, 0
	return more, true
}
