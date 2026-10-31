package failurelog_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/failurelog"
)

func TestFailureLogsAtInterval(t *testing.T) {
	var l failurelog.Limiter
	base := time.Time{}
	for _, tc := range []struct {
		at   time.Duration
		log  bool
		more uint64
	}{
		{0, true, 0},
		{5 * time.Second, false, 0},
		{10*time.Minute - time.Nanosecond, false, 0},
		{10 * time.Minute, true, 2},
		{20*time.Minute + time.Second, true, 0},
	} {
		more, log := l.Failure(base.Add(tc.at))
		if log != tc.log || more != tc.more {
			t.Errorf("failure at %s = log %v, count %d, want log %v, count %d",
				tc.at, log, more, tc.log, tc.more)
		}
	}
}

func TestFailuresEveryPollForADay(t *testing.T) {
	var l failurelog.Limiter
	base := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	var lines []string
	for attempt := 0; attempt < 17280; attempt++ {
		text := fmt.Sprintf("connection reset on local port %d", 40000+attempt)
		more, log := l.Failure(base.Add(time.Duration(attempt) * 5 * time.Second))
		if log != (attempt%120 == 0) {
			t.Fatalf("attempt %d logged %v, want one line per 120 attempts", attempt, log)
		}
		if !log {
			continue
		}
		wantMore := uint64(119)
		if attempt == 0 {
			wantMore = 0
		}
		if more != wantMore {
			t.Fatalf("attempt %d reports %d suppressed failures, want %d", attempt, more, wantMore)
		}
		lines = append(lines, fmt.Sprintf("%s (%d more since the last line)", text, more))
	}
	if len(lines) != 144 {
		t.Fatalf("a day of failures logged %d lines, want 144", len(lines))
	}
	if want := "connection reset on local port 57160 (119 more since the last line)"; lines[143] != want {
		t.Fatalf("last line = %q, want the latest text %q", lines[143], want)
	}
}
