package hibd

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/task"
)

func TestTaskLogsHold(t *testing.T) {
	for _, event := range []string{"none", "bytes", "end"} {
		t.Run(event, func(t *testing.T) {
			a, out, stderr := newApp(t, fake.New())
			rec := submitReadTask(t, taskReadStore(t, a), "hold", time.Now())
			clock := followClock(t, a, func(tick int) {
				if tick == 2 && event == "bytes" {
					writeFollowLog(t, a, rec.Run, []byte("new bytes"))
				}
				if tick == 2 && event == "end" {
					endFollowRun(t, a, rec.Run)
				}
			})
			code, r := taskReadReply(t, a, out, "task", "logs", string(rec.Run.Task), "-hold", "2500ms")
			wantTime := 2500 * time.Millisecond
			if event != "none" {
				wantTime = 2 * time.Second
			}
			if code != 0 || r.TaskLog == nil || r.TaskLog.Run != 1 || r.TaskLog.Done != (event == "end") ||
				*clock != wantTime || stderr.Len() != 0 {
				t.Fatalf("F3 %s = exit %d, %+v, elapsed %v, stderr %q", event, code, r, *clock, stderr)
			}
			if event == "bytes" && (string(r.TaskLog.Data) != "new bytes" || r.TaskLog.Next != 9) {
				t.Fatalf("F3 bytes = %+v; want all newly copied bytes", r.TaskLog)
			}
			*clock = 0
			code, _ = taskReadReply(t, a, out, "task", "logs", string(rec.Run.Task))
			if code != 0 || *clock != 0 {
				t.Fatalf("F3 no hold = exit %d, elapsed %v; want immediate", code, *clock)
			}
			if event != "none" {
				code, _ = taskReadReply(t, a, out, "task", "logs", string(rec.Run.Task), "-hold", "60s")
				if code != 0 || *clock != 0 {
					t.Fatalf("F3 ready hold = exit %d, elapsed %v; want immediate", code, *clock)
				}
			}
		})
	}
}

func TestTaskWaitHold(t *testing.T) {
	for _, finish := range []bool{false, true} {
		t.Run(map[bool]string{false: "deadline", true: "outcome"}[finish], func(t *testing.T) {
			a, out, stderr := newApp(t, fake.New())
			rec := submitReadTask(t, taskReadStore(t, a), "wait", time.Now())
			clock := followClock(t, a, func(tick int) {
				if finish && tick == 2 {
					endFollowRun(t, a, rec.Run)
				}
			})
			code, r := taskReadReply(t, a, out, "task", "wait", string(rec.Run.Task), "-hold", "2500ms")
			wantTime := 2500 * time.Millisecond
			if finish {
				wantTime = 2 * time.Second
			}
			if code != 0 || r.Task == nil || r.Task.ID != rec.Run.Task || len(r.Task.Runs) != 1 ||
				r.Task.Runs[0].N != 1 || *clock != wantTime || stderr.Len() != 0 {
				t.Fatalf("F5 wait = exit %d, %+v, elapsed %v, stderr %q", code, r, *clock, stderr)
			}
			if finish && r.Task.Runs[0].Outcome != task.NoChanges ||
				!finish && r.Task.Runs[0].Phase != task.PhaseQueued {
				t.Fatalf("F5 waited run = %+v; want queued or no_changes", r.Task.Runs[0])
			}
			if finish {
				st := taskReadStore(t, a)
				_, err := st.Submit(t.Context(), task.Submission{RequestID: "next", Task: rec.Run.Task,
					At: time.Now(), Spec: task.Spec{Command: []string{"true"}, Image: "dev", Size: "small",
						Timeout: time.Hour, Publish: task.PublishNone},
					Actor: task.Actor{Owner: "martin", Name: "martin", Source: "cli"}}, nil)
				if err != nil {
					t.Fatal(err)
				}
				code, r = taskReadReply(t, a, out, "task", "wait", string(rec.Run.Task), "-run", "1")
				if code != 0 || len(r.Task.Runs) != 1 || r.Task.Runs[0].N != 1 ||
					r.Task.Runs[0].Outcome != task.NoChanges {
					t.Fatalf("F5 historical run = exit %d, %+v", code, r)
				}
			}
		})
	}
}

func TestTaskFollowValidationAndCancellation(t *testing.T) {
	for _, cmd := range []string{"logs", "wait"} {
		a, out, _ := newApp(t, fake.New())
		rec := submitReadTask(t, taskReadStore(t, a), "validation", time.Now())
		for _, tc := range []struct {
			args []string
			code hibapi.ErrorCode
		}{
			{[]string{"t-aaaaaaaa", "-hold", "0s"}, hibapi.CodeNoTask},
			{[]string{string(rec.Run.Task), "-run", "2"}, hibapi.CodeBadRequest},
			{[]string{string(rec.Run.Task), "-run", "-1"}, hibapi.CodeBadRequest},
			{[]string{"invalid"}, hibapi.CodeBadRequest},
			{[]string{string(rec.Run.Task), "-hold", "61s"}, hibapi.CodeBadRequest},
			{[]string{string(rec.Run.Task), "-hold", "-1s"}, hibapi.CodeBadRequest},
		} {
			code, r := taskReadReply(t, a, out, append([]string{"task", cmd}, tc.args...)...)
			if code != 1 || r.Code != tc.code {
				t.Fatalf("%s %v = exit %d, %+v; want %s", cmd, tc.args, code, r, tc.code)
			}
		}
		polls := 0
		clock := followClock(t, a, func(int) { polls++ })
		code, _ := taskReadReply(t, a, out, "task", cmd, string(rec.Run.Task), "-hold", "60s")
		if code != 0 || *clock != 60*time.Second || polls != 60 {
			t.Fatalf("%s cap = exit %d, elapsed %v, %d polls; want 60s and 60 one-second sleeps", cmd, code, *clock, polls)
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		a.TaskHoldWait = func(context.Context, time.Duration) error {
			cancel()
			return context.Canceled
		}
		out.Reset()
		code = a.Run(ctx, []string{"task", cmd, string(rec.Run.Task), "-hold", "60s", "-json"})
		cancel()
		var r hibapi.Response
		if err := json.Unmarshal(out.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		if code != 1 || r.Error == "" {
			t.Fatalf("%s cancelled = exit %d, %+v; want explicit error", cmd, code, r)
		}
	}
}

func followClock(t *testing.T, a *App, event func(int)) *time.Duration {
	t.Helper()
	var elapsed time.Duration
	ticks := 0
	a.Now = func() time.Time { return time.Unix(0, 0).Add(elapsed) }
	a.TaskHoldWait = func(ctx context.Context, d time.Duration) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("hold has no deadline")
		}
		if d <= 0 || d > time.Second {
			t.Fatalf("poll duration %v; want 0 < D <= 1s", d)
		}
		elapsed += d
		ticks++
		if event != nil {
			event(ticks)
		}
		return nil
	}
	return &elapsed
}

func writeFollowLog(t *testing.T, a *App, id task.RunID, data []byte) {
	t.Helper()
	dir := filepath.Join(a.Getenv("HIBD_STATE_DIR"), "tasks", "logs", id.String())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "command"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func endFollowRun(t *testing.T, a *App, id task.RunID) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(a.Getenv("HIBD_STATE_DIR"), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, err = db.ExecContext(ctx, `INSERT INTO checkpoints
		(task, n, seq, step, kind, at, by, body) VALUES (?, ?, 1, ?, ?, 0, 'fixture', '{}')`,
		id.Task, id.N, task.StepKeep, task.Result)
	if err != nil {
		t.Fatal(err)
	}
	var busy, wal, copied int
	err = db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &wal, &copied)
	if err != nil || busy != 0 || wal != 0 {
		t.Fatalf("checkpoint during hold = %d, %d, %d, %v; want no held read transaction", busy, wal, copied, err)
	}
}

func TestTaskHoldContextStopsRead(t *testing.T) {
	a := &App{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := a.holdTask(ctx, 0, func(context.Context) (bool, error) {
		t.Fatal("cancelled hold read the store")
		return false, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled hold = %v", err)
	}

}

func TestTaskWaitHeldMachine(t *testing.T) {
	e := fake.New()
	a, out, _ := newApp(t, e)
	rec := submitReadTask(t, taskReadStore(t, a), "held", time.Now())
	e.Add(engine.Machine{ID: "held-machine", State: engine.Paused})
	db, err := sql.Open("sqlite", filepath.Join(a.Getenv("HIBD_STATE_DIR"), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, err = db.ExecContext(ctx, `INSERT INTO checkpoints
			(task,n,seq,step,kind,at,by,body) VALUES
			(?,1,1,'machine','intent',0,'fixture','{}'),
			(?,1,2,'machine','result',0,'fixture','{"ID":"held-machine"}'),
			(?,1,3,'command','intent',0,'fixture','{"Stage":"command"}'),
			(?,1,4,'command','result',0,'fixture','{"Stage":"command"}')`,
		rec.Run.Task, rec.Run.Task, rec.Run.Task, rec.Run.Task)
	if err != nil {
		t.Fatal(err)
	}
	code, r := taskReadReply(t, a, out, "task", "wait", string(rec.Run.Task), "-hold", "0s")
	if code != 0 || r.Task == nil || r.Task.Runs[0].Held != task.ErrHeld.Error() {
		t.Fatalf("F8 waited machine = exit %d, %+v; want the rescue reason", code, r.Task)
	}
}
