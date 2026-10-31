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
		line string
	}{
		{0, true, "latest"},
		{5 * time.Second, false, ""},
		{10*time.Minute - time.Nanosecond, false, ""},
		{10 * time.Minute, true, "latest (2 more since the last line)"},
		{20*time.Minute + time.Second, true, "latest"},
	} {
		line, log := l.Failure(base.Add(tc.at), "latest")
		if log != tc.log || line != tc.line {
			t.Errorf("failure at %s = log %v, line %q, want log %v, line %q",
				tc.at, log, line, tc.log, tc.line)
		}
	}
}

func TestFailuresEveryPollForADay(t *testing.T) {
	var l failurelog.Limiter
	base := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	var lines []string
	for attempt := range 17280 {
		text := fmt.Sprintf("connection reset on local port %d", 40000+attempt)
		line, log := l.Failure(base.Add(time.Duration(attempt)*5*time.Second), text)
		if log != (attempt%120 == 0) {
			t.Fatalf("attempt %d logged %v, want one line per 120 attempts", attempt, log)
		}
		if log {
			lines = append(lines, line)
		}
	}
	if len(lines) != 144 {
		t.Fatalf("a day of failures logged %d lines, want 144", len(lines))
	}
	if want := "connection reset on local port 57160 (119 more since the last line)"; lines[143] != want {
		t.Fatalf("last line = %q, want the latest text %q", lines[143], want)
	}
}

func TestSuccessLogsCountOnceWithoutResettingInterval(t *testing.T) {
	var l failurelog.Limiter
	base := time.Time{}
	if line, log := l.Success("recovered"); log || line != "" {
		t.Fatalf("initial success logged %q", line)
	}
	l.Failure(base, "failed")
	l.Failure(base.Add(time.Second), "latest")
	if line, log := l.Success("recovered"); !log || line != "recovered (1 more since the last line)" {
		t.Fatalf("recovery = %q, logged %v, want recovery and count", line, log)
	}
	if line, log := l.Success("recovered"); log || line != "" {
		t.Fatalf("second success logged %q", line)
	}
	if line, log := l.Failure(base.Add(2*time.Second), "failed again"); log || line != "" {
		t.Fatalf("failure after success reset the interval and logged %q", line)
	}
	if line, log := l.Success("recovered"); log || line != "" {
		t.Fatalf("a suppressed failure authorized recovery %q", line)
	}
	if line, log := l.Failure(base.Add(10*time.Minute), "latest"); !log ||
		line != "latest (1 more since the last line)" {
		t.Fatalf("next failure = %q, logged %v, want latest text and retained count", line, log)
	}
}

func TestAlternatingFailuresAndSuccessesForADay(t *testing.T) {
	var l failurelog.Limiter
	base := time.Time{}
	var lines []string
	for attempt := range 17280 {
		at := base.Add(time.Duration(attempt) * 5 * time.Second)
		var line string
		var log bool
		if attempt%2 == 0 {
			line, log = l.Failure(at, fmt.Sprintf("failure %d", attempt))
		} else {
			line, log = l.Success("recovered")
		}
		if log {
			lines = append(lines, line)
		}
	}
	if len(lines) != 288 {
		t.Fatalf("an alternating day logged %d lines, want 288", len(lines))
	}
	if lines[0] != "failure 0" || lines[1] != "recovered" ||
		lines[286] != "failure 17160 (59 more since the last line)" || lines[287] != "recovered" {
		t.Fatalf("alternating day first and last lines = %q, %q", lines[:2], lines[286:])
	}
}
