//go:build !e2e

package e2e

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestLatestDurableBuildFollowsAssignmentOrder(t *testing.T) {
	for _, tc := range []struct {
		name, out, previous, id string
		ready                   bool
	}{
		{"no assignment", "", "", "", false},
		{"pending", chainA + "|pending", "", chainA, false},
		{"in progress", chainA + "|in_progress", "", chainA, false},
		{"ready", chainA + "|ready", "", chainA, true},
		{"old assignment", chainA + "|ready", chainA, chainA, false},
		{"new assignment", chainC + "|ready\n" + chainA + "|ready", chainC, chainA, true},
		{"older pending", chainC + "|in_progress\n" + chainA + "|ready", chainC, chainA, true},
		{"older failed", chainC + "|failed\n" + chainA + "|ready", chainC, chainA, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, ready, err := latestDurableBuild(tc.out, tc.previous)
			if err != nil || id != tc.id || ready != tc.ready {
				t.Errorf("latest = %s, ready %v, error %v, want %s, %v", id, ready, err, tc.id, tc.ready)
			}
		})
	}
}

func TestLatestDurableBuildRefusesFailedAndMalformedStatus(t *testing.T) {
	for _, out := range []string{
		chainC + "|ready\n" + chainA + "|failed",
		chainA + "|unknown", "../bad|ready", chainA + "|ready\n" + chainA + "|ready",
		chainA + "|ready|extra", "\x1b[31m",
	} {
		if _, ready, err := latestDurableBuild(out, ""); err == nil || ready {
			t.Fatal("accepted a failed or malformed newest assignment")
		} else if strings.ContainsAny(err.Error(), "\n\x1b") {
			t.Errorf("error contains raw host output: %q", err)
		}
	}
	for _, previous := range []string{"", chainA} {
		_, _, err := latestDurableBuild(chainA+"|failed", previous)
		if err == nil || !strings.Contains(err.Error(), "latest snapshot build "+chainA+" failed") {
			t.Errorf("failed build error = %v, want a clear failure with its checked UUID", err)
		}
	}
}

func TestDurableFilesCommandChecksUUIDAndAllSidecars(t *testing.T) {
	command, err := durableFilesCommand(chainA)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"/var/lib/e2b/local-template-storage/" + chainA,
		"memfile.header rootfs.ext4.header snapfile metadata.json",
		`[ ! -f "$d/$f" ]`, `printf "pending\n"`, `printf "ready\n"`,
	} {
		if !strings.Contains(command, want) {
			t.Errorf("readiness command doesn't contain %q", want)
		}
	}
	for _, invalid := range []string{"", chainA + ";echo bad", chainA + "\n", "../bad", "$(echo bad)"} {
		if command, err := durableFilesCommand(invalid); err == nil || command != "" {
			t.Fatal("readiness command accepted an unsafe UUID")
		}
	}
}

func TestParseDurableFiles(t *testing.T) {
	for out, want := range map[string]bool{"ready": true, "ready\n": true, "pending": false, "pending\n": false} {
		if got, err := parseDurableFiles(out); err != nil || got != want {
			t.Errorf("ready = %v, %v, want %v", got, err, want)
		}
	}
	for _, out := range []string{"", " ready", "ready\npending", "ready\tbad", "\x1b[31mready"} {
		if _, err := parseDurableFiles(out); err == nil {
			t.Fatal("accepted malformed file readiness")
		}
	}
}

func TestPollDurableRetriesUntilReady(t *testing.T) {
	checks := 0
	id, err := pollDurable(t.Context(), time.Second, time.Millisecond, func(ctx context.Context) (string, bool, error) {
		if _, hasDeadline := ctx.Deadline(); !hasDeadline {
			t.Fatal("readiness checks aren't bounded")
		}
		checks++
		return chainA, checks == 2, nil
	})
	if err != nil || id != chainA || checks != 2 {
		t.Errorf("id = %s, checks %d, error %v, want ready after two checks", id, checks, err)
	}
}

func TestPollDurableStopsOnCheckFailure(t *testing.T) {
	want := errors.New("latest snapshot failed")
	checks := 0
	_, err := pollDurable(t.Context(), time.Second, time.Millisecond, func(context.Context) (string, bool, error) {
		checks++
		return "", false, want
	})
	if !errors.Is(err, want) || checks != 1 {
		t.Errorf("error = %v, checks %d, want the first check's failure", err, checks)
	}
}

func TestPollDurableHonorsContextAndLimit(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		parentLimit, pollLimit   time.Duration
		block, lateReady, cancel bool
		want                     error
		message                  string
	}{
		{"between checks", time.Second, 10 * time.Millisecond, false, false, false, context.DeadlineExceeded, "within 10ms"},
		{"during check", time.Second, 10 * time.Millisecond, true, false, false, context.DeadlineExceeded, "within 10ms"},
		{"late result", time.Second, 10 * time.Millisecond, true, true, false, context.DeadlineExceeded, "within 10ms"},
		{"parent deadline", 10 * time.Millisecond, time.Second, true, false, false, context.DeadlineExceeded, "waiting for durable snapshot"},
		{"already cancelled", time.Second, time.Second, false, false, true, context.Canceled, "waiting for durable snapshot"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), tc.parentLimit)
			defer cancel()
			if tc.cancel {
				cancel()
			}
			checks := 0
			_, err := pollDurable(ctx, tc.pollLimit, time.Second, func(ctx context.Context) (string, bool, error) {
				checks++
				if tc.block {
					<-ctx.Done()
					if tc.lateReady {
						return chainA, true, nil
					}
					return "", false, ctx.Err()
				}
				return "", false, nil
			})
			if !errors.Is(err, tc.want) || !strings.Contains(err.Error(), tc.message) {
				t.Errorf("error = %v, want %v and %q", err, tc.want, tc.message)
			}
			if tc.cancel && checks != 0 {
				t.Error("polled after cancellation")
			}
		})
	}
}
