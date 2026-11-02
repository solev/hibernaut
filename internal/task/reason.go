package task

import (
	"cmp"
	"fmt"
	"slices"
	"time"
)

type Queue struct {
	ahead map[RunID]int
}

func QueueOf(unfinished []Run, now time.Time) Queue {
	var queued []Run
	for _, r := range unfinished {
		if !r.Queued() || r.Ended() || !now.Before(r.QueueLimit()) {
			continue
		}
		if _, stopping := StopFor(r, now); stopping {
			continue
		}
		queued = append(queued, r)
	}
	slices.SortStableFunc(queued, func(a, b Run) int {
		return cmp.Or(a.Submitted.Compare(b.Submitted), cmp.Compare(a.ID.N, b.ID.N))
	})
	q := Queue{ahead: make(map[RunID]int, len(queued))}
	for i, r := range queued {
		q.ahead[r.ID] = i
	}
	return q
}

func WaitReason(r Run, q Queue, now time.Time) string {
	if r.Ended() {
		return ""
	}
	if _, stopped := r.Stop(); stopped {
		return ""
	}
	if r.Queued() {
		if stop, stopping := StopFor(r, now); stopping {
			if stop.Outcome == Expired {
				return "queue limit passed at " + r.QueueLimit().UTC().Format(time.RFC3339) +
					"; waiting for hibd to record the stop"
			}
			return ""
		}
		position := "queue position unavailable"
		if ahead, ok := q.ahead[r.ID]; ok {
			word := "runs"
			if ahead == 1 {
				word = "run"
			}
			position = fmt.Sprintf("%d %s ahead", ahead, word)
		}
		return fmt.Sprintf("waiting for room or its turn; %s; expires at %s",
			position, r.QueueLimit().UTC().Format(time.RFC3339))
	}
	if r.Machine == MachinePaused {
		if _, live := r.LiveStage(); !live {
			proof := "without recorded freeze proof"
			if r.Frozen() {
				proof = "with recorded freeze proof"
			}
			return "machine paused " + proof + "; no live stage is recorded"
		}
		if !r.Frozen() {
			return ErrHeld.Error()
		}
		return "machine paused with recorded freeze proof; waiting for hibd to resume or stop the run"
	}
	return ""
}
