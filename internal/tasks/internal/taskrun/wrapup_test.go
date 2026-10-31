package taskrun

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/taskguest"
	"github.com/solev/hibernaut/internal/tasks/internal/taskstore"
)

type wrapGuest struct {
	Engine
	h    *harness
	mode string
}

func (e wrapGuest) Exec(ctx context.Context, mid string, req engine.ExecRequest) (engine.ExecResult, error) {
	argv := append([]string{req.Cmd}, req.Args...)
	if e.mode == "outlive-term" && req.Cmd == "systemctl" && req.Args[0] == "stop" {
		return engine.ExecResult{}, nil
	}
	r := e.h.run(e.h.testRun())
	last, _ := r.Last()
	wrap := last.Step() == task.StepStop || last.Step() == task.StepCollect
	look := len(req.Args) > 1 && req.Args[1] == "look"
	mode := e.mode
	if strings.HasPrefix(mode, "collect-") {
		mode = strings.TrimPrefix(mode, "collect-")
		wrap = last.Step() == task.StepCollect
	}
	if wrap && look {
		switch mode {
		case "garbage":
			return engine.ExecResult{Stdout: []byte("a lie\n")}, nil
		case "nonzero":
			return engine.ExecResult{ExitCode: 9, Stderr: []byte("refused")}, nil
		case "hang":
			<-ctx.Done()
			return engine.ExecResult{}, context.Cause(ctx)
		case "slow-tail":
			return engine.ExecResult{Stdout: []byte("marker=yes\nlog=52428800\n")}, nil
		}
	}
	if wrap && len(req.Args) > 1 && req.Args[1] == "log" {
		switch e.mode {
		case "long":
			n, _ := strconv.Atoi(req.Args[len(req.Args)-1])
			return engine.ExecResult{Stdout: []byte("size=52428800\n" +
				base64.StdEncoding.EncodeToString(make([]byte, n+1)))}, nil
		case "empty":
			return engine.ExecResult{Stdout: []byte("size=13\n\n")}, nil
		case "bad-log":
			return engine.ExecResult{Stdout: []byte("not a chunk")}, nil
		case "slow-tail":
			select {
			case <-ctx.Done():
				return engine.ExecResult{}, context.Cause(ctx)
			case <-time.After(time.Second):
				return engine.ExecResult{Stdout: []byte("size=52428800\neA==")}, nil
			}
		}
	}
	if e.mode == "publication" && req.Cmd == "cat" {
		return engine.ExecResult{Stdout: []byte("not a publication\n")}, nil
	}
	if e.mode == "hang" && slices.Equal(argv, taskguest.Kill(r.ID, task.StageCommand)) {
		<-ctx.Done()
		return engine.ExecResult{}, context.Cause(ctx)
	}
	return e.Engine.Exec(ctx, mid, req)
}

func (h *harness) testRun() task.RunID {
	runs, err := h.store.Unfinished(context.Background())
	if err != nil || len(runs) != 1 {
		h.t.Fatalf("test needs one unfinished run: %v, %d", err, len(runs))
	}
	return runs[0].ID
}

func bubbleHarness(t *testing.T, mode string) *harness {
	t.Helper()
	h := newHarness(t, script(time.Hour, 0))
	h.clock.now = time.Now()
	h.configure = func(cfg *Config) {
		cfg.Clock = RealClock{}
		cfg.Engine = wrapGuest{Engine: cfg.Engine, h: h, mode: mode}
	}
	h.start()
	return h
}

func bubbleUntil(t *testing.T, h *harness, limit time.Duration, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for !done() && time.Now().Before(deadline) {
		if err := h.sup.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		time.Sleep(time.Second)
		h.clock.Advance(time.Second)
		synctest.Wait()
	}
	if !done() {
		t.Fatalf("run did not end within %s", limit)
	}
}

func stopResult(t *testing.T, r task.Run) *task.StopResult {
	t.Helper()
	for _, c := range r.Log {
		if b, ok := c.Body.(*task.StopResult); ok {
			return b
		}
	}
	t.Fatal("no stop result")
	return nil
}

func unreadResult(t *testing.T, r task.Run) []string {
	t.Helper()
	for _, c := range r.Log {
		if c.Step() == task.StepCollect && c.Kind() == task.Result {
			data, err := json.Marshal(c.Body)
			if err != nil {
				t.Fatal(err)
			}
			var res struct{ Unread []string }
			if err := json.Unmarshal(data, &res); err != nil {
				t.Fatal(err)
			}
			return res.Unread
		}
	}
	t.Fatal("no collect result")
	return nil
}

func TestStopUntrustedGuest(t *testing.T) {
	for _, mode := range []string{"garbage", "nonzero", "hang"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := bubbleHarness(t, mode)
				id := h.submit(2 * time.Hour)
				bubbleUntil(t, h, 30*time.Second, h.launched(id, task.StageCommand))
				h.request(id, task.RequestCancel)
				bubbleUntil(t, h, 75*time.Second, h.ended(id))
				h.stop()
				r := h.run(id)
				if outcome(r) != task.Cancelled || stopResult(t, r).How != "unproven" ||
					r.Machine != task.MachinePaused {
					t.Fatalf("cancel did not finish unproven and paused: %+v", r.Status(""))
				}
				if len(unreadResult(t, r)) == 0 {
					t.Fatal("unproven stop did not name its unread tail")
				}
				var at time.Time
				for _, c := range r.Log {
					if c.Step() == task.StepStop && c.Kind() == task.Intent {
						at = c.At
					}
					if c.Step() == task.StepStop && c.Kind() == task.Result &&
						c.At.Sub(at) > 70*time.Second {
						t.Fatalf("stop result took %s, want at most 60s plus final KILL's 10s", c.At.Sub(at))
					}
				}
				h.checkLog(id)
			})
		})
	}
}

func TestStopPausedWithoutRoom(t *testing.T) {
	for _, frozen := range []bool{true, false} {
		t.Run(fmt.Sprint("frozen=", frozen), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := bubbleHarness(t, "")
				id := h.submit(2 * time.Hour)
				bubbleUntil(t, h, 30*time.Second, h.launched(id, task.StageCommand))
				h.stop()
				mid := h.machineID(id)
				if frozen {
					if err := h.owner.Pause(context.Background(), mid); err != nil {
						t.Fatal(err)
					}
				} else if err := h.sim.Pause(context.Background(), mid); err != nil {
					t.Fatal(err)
				}
				calls := len(h.sim.Calls)
				h.m.Room = 0
				h.request(id, task.RequestCancel)
				h.start()
				bubbleUntil(t, h, 65*time.Second, h.ended(id))
				h.stop()
				r := h.run(id)
				if outcome(r) != task.Cancelled || stopResult(t, r).How != "unproven" ||
					r.Machine != task.MachinePaused {
					t.Fatalf("paused stop did not end unproven: %+v", r.Status(""))
				}
				if len(h.sim.Calls) != calls {
					t.Fatal("stop or collect executed in a paused machine")
				}
			})
		})
	}
}

func TestStopBudgetSurvivesRestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := bubbleHarness(t, "garbage")
		id := h.submit(2 * time.Hour)
		bubbleUntil(t, h, 30*time.Second, h.launched(id, task.StageCommand))
		h.request(id, task.RequestCancel)
		bubbleUntil(t, h, 5*time.Second, func() bool { _, ok := h.run(id).Stop(); return ok })
		h.stop()
		time.Sleep(time.Minute)
		h.clock.Advance(time.Minute)
		h.start()
		bubbleUntil(t, h, 5*time.Second, h.ended(id))
		h.stop()
		if stopResult(t, h.run(id)).How != "unproven" {
			t.Fatal("restart reset the durable stop budget")
		}
	})
}

func TestCollectUntrustedGuest(t *testing.T) {
	for _, mode := range []string{"long", "empty", "bad-log", "publication", "slow-tail"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := bubbleHarness(t, mode)
				if mode == "publication" {
					h.sim.Script = script(0, 0)
				}
				id := h.submit(2 * time.Hour)
				if mode != "publication" {
					bubbleUntil(t, h, 30*time.Second, h.launched(id, task.StageCommand))
					h.request(id, task.RequestCancel)
				}
				bubbleUntil(t, h, 75*time.Second, h.ended(id))
				h.stop()
				r := h.run(id)
				unread := strings.Join(unreadResult(t, r), "\n")
				want := map[string]string{"long": "chunk exceeds requested bytes",
					"empty": "empty chunk", "bad-log": "log:", "publication": "publish:",
					"slow-tail": "deadline exceeded"}[mode]
				if !strings.Contains(unread, want) {
					t.Fatalf("unread tail=%q, want %q", unread, want)
				}
				if r.Machine != task.MachinePaused {
					t.Fatal("collect did not keep the machine paused")
				}
				if mode == "slow-tail" {
					n, err := (taskstore.MemoryLogs{M: h.store}).Size(id, task.StageCommand)
					if err != nil || n == 0 || n > 60 {
						t.Fatalf("bounded partial log size=%d, err=%v, want 1..60 bytes", n, err)
					}
				}
				h.checkLog(id)
			})
		})
	}
}

func TestCollectLookFailure(t *testing.T) {
	for _, mode := range []string{"garbage", "nonzero", "hang"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := bubbleHarness(t, "collect-"+mode)
				id := h.submit(2 * time.Hour)
				bubbleUntil(t, h, 30*time.Second, h.launched(id, task.StageCommand))
				h.request(id, task.RequestCancel)
				bubbleUntil(t, h, 40*time.Second, h.ended(id))
				h.stop()
				if r := h.run(id); outcome(r) != task.Cancelled || len(unreadResult(t, r)) == 0 ||
					r.Machine != task.MachinePaused {
					t.Fatalf("collect look failure did not finish cancelled with an unread tail: %+v", r.Status(""))
				}
			})
		})
	}
}

func TestUnprovenStopKeepsFailedFreezePaused(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := bubbleHarness(t, "garbage")
		h.count.Engine = &failedKeepFreeze{Engine: h.sim}
		id := h.submit(2 * time.Hour)
		bubbleUntil(t, h, 30*time.Second, h.launched(id, task.StageCommand))
		h.request(id, task.RequestCancel)
		bubbleUntil(t, h, 75*time.Second, h.ended(id))
		h.stop()
		r := h.run(id)
		if r.Machine != task.MachinePaused || r.Frozen() ||
			!strings.Contains(r.Freeze.Detail, "freeze failed") {
			t.Fatalf("failed keep freeze did not pause and record unfrozen: %+v", r.Freeze)
		}
	})
}

type failLogAppend struct {
	taskstore.Logs
	err error
}

func (l failLogAppend) Append(task.RunID, task.Stage, int64, []byte) error { return l.err }

func TestCollectDoesNotHideHostLogFailure(t *testing.T) {
	h := newHarness(t, script(time.Hour, 0))
	h.start()
	id := h.submit(2 * time.Hour)
	h.until(30, h.launched(id, task.StageCommand))
	h.stop()
	h.request(id, task.RequestCancel)
	r := h.run(id)
	argv := taskguest.Stop(id, task.StageCommand)
	if _, err := h.sim.Exec(context.Background(), r.MachineID(),
		engine.ExecRequest{Cmd: argv[0], Args: argv[1:], User: "root"}); err != nil {
		t.Fatal(err)
	}
	for _, body := range []task.Body{&task.StopIntent{Outcome: task.Cancelled}, &task.StopResult{How: "term"}} {
		c := task.Checkpoint{Run: id, Seq: len(r.Log) + 1, At: h.clock.Now(), Body: body}
		if err := h.store.Append(context.Background(), c); err != nil {
			t.Fatal(err)
		}
		r.Log = append(r.Log, c)
	}
	want := errors.New("host log append failed")
	x := executor{id: id, r: r, cfg: Config{Store: h.store, Engine: h.owner, Clock: h.clock,
		Logs: failLogAppend{Logs: taskstore.MemoryLogs{M: h.store}, err: want}}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := x.collect(ctx); !errors.Is(err, want) {
		t.Fatalf("collect error=%v, want host log failure", err)
	}
	if h.run(id).Ended() {
		t.Fatal("host log failure became a completed run")
	}
}
