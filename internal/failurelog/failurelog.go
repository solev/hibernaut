// Package failurelog bounds repeated failures to one failure line per 10 minutes per thing.
// A success permits one recovery line per emitted failure line, including suppressed failures
// since that line, without resetting the interval. Calls on a Limiter must not overlap.
package failurelog

import (
	"fmt"
	"time"
)

const interval = 10 * time.Minute

// Limiter tracks the failure lines and recovery allowance of one failing thing.
// Its zero value is ready to use. The caller serializes calls and owns its lifetime.
type Limiter struct {
	last    *time.Time
	more    uint64
	recover bool
}

// Failure returns the latest text with a count suffix when at least 10 minutes have passed
// since the last failure line. Otherwise it counts the failure and returns no line.
func (l *Limiter) Failure(now time.Time, text string) (line string, log bool) {
	if l.last != nil && now.Sub(*l.last) < interval {
		l.more++
		return "", false
	}
	line = counted(text, l.more)
	l.last, l.more = &now, 0
	l.recover = true
	return line, true
}

// Success returns the recovery text with the count since the last line, once per emitted
// failure line. It consumes that count and allowance but preserves the failure interval.
// A success without an allowance emits nothing and preserves any suppressed failures.
func (l *Limiter) Success(text string) (line string, log bool) {
	if !l.recover {
		return "", false
	}
	line = counted(text, l.more)
	l.more, l.recover = 0, false
	return line, true
}

func counted(text string, more uint64) string {
	if more == 0 {
		return text
	}
	return fmt.Sprintf("%s (%d more since the last line)", text, more)
}
