package taskrun

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/taskguest"
	"github.com/solev/hibernaut/internal/taskguest/guestsim"
	"github.com/solev/hibernaut/internal/tasks/internal/taskstore"
)

type countingLogs struct {
	taskstore.Logs
	appends []int
}

func (l *countingLogs) Append(id task.RunID, s task.Stage, offset int64, data []byte) error {
	l.appends = append(l.appends, len(data))
	return l.Logs.Append(id, s, offset, data)
}

func TestLogPollAppends(t *testing.T) {
	for _, size := range []int{13, 3*taskguest.MaxLogChunk + 7} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			h := newHarness(t, func(task.RunID, task.Stage) guestsim.Behavior {
				return guestsim.Behavior{Output: bytes.Repeat([]byte("x"), size)}
			})
			logs := &countingLogs{Logs: taskstore.MemoryLogs{M: h.store}}
			h.configure = func(cfg *Config) { cfg.Logs = logs }
			h.start()
			rc, err := h.store.Submit(t.Context(), task.Submission{RequestID: "one-stage",
				Spec: task.Spec{Command: []string{"true"}, Image: "dev", Size: "dev",
					Timeout: time.Hour}, Actor: alice, At: h.clock.Now()}, nil)
			if err != nil {
				t.Fatal(err)
			}
			h.until(10, h.ended(rc.Run))
			h.stop()
			t.Logf("%d output bytes in one poll: %d appends, sizes %v", size, len(logs.appends), logs.appends)
			if len(logs.appends) != 1 || logs.appends[0] != size {
				t.Errorf("one poll appended %v, want one append of %d bytes", logs.appends, size)
			}
		})
	}
}

func TestRunLogBudget(t *testing.T) {
	for _, c := range []struct {
		name        string
		setup, cmd  int
		status      int
		wantOutcome task.Outcome
	}{
		{"success", 30 << 20, 25 << 20, 0, task.Succeeded},
		{"failure", 30 << 20, 25 << 20, 7, task.Failed},
		{"setup-spends-budget", task.MaxLogBytes + 1, 13, 0, task.Succeeded},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, func(id task.RunID, s task.Stage) guestsim.Behavior {
				b := script(0, c.status)(id, s)
				switch s {
				case task.StageSetup:
					b.Output = bytes.Repeat([]byte("s"), c.setup)
				case task.StageCommand:
					b.Output = bytes.Repeat([]byte("c"), c.cmd)
				}

				return b
			})
			var messages []string
			h.configure = func(cfg *Config) {
				cfg.Logf = func(format string, args ...any) {
					messages = append(messages, fmt.Sprintf(format, args...))
				}
			}
			h.start()
			id := h.submit(time.Hour)
			for pass := 0; pass < 10 && !h.ended(id)(); pass++ {
				h.tick()
			}
			h.stop()
			r := h.run(id)
			if !r.Ended() || outcome(r) != c.wantOutcome {
				t.Fatalf("want ended %s; %s; supervisor logged %s",
					c.wantOutcome, sweepResult(h, id, 10), strings.Join(messages, "; "))
			}
			var result *task.CollectResult
			for _, row := range r.Log {
				if got, ok := row.Body.(*task.CollectResult); ok {
					result = got
				}
			}
			written := int64(c.setup + c.cmd)
			if result == nil || result.LogBytes != task.MaxLogBytes ||
				result.Dropped != written-task.MaxLogBytes || result.LogBytes+result.Dropped != written {
				t.Errorf("collected %+v for %d guest bytes, want kept=%d dropped=%d",
					result, written, task.MaxLogBytes, written-task.MaxLogBytes)
			}
			logs := taskstore.MemoryLogs{M: h.store}
			var kept int64
			for _, stage := range task.Stages() {
				n, err := logs.Size(id, stage)
				if err != nil {
					t.Fatal(err)
				}
				want := int64(0)
				switch stage {
				case task.StageSetup:
					want = min(int64(c.setup), task.MaxLogBytes)
				case task.StageCommand:
					want = task.MaxLogBytes - min(int64(c.setup), task.MaxLogBytes)
				}
				if n != want {
					t.Errorf("%s kept %d bytes, want %d", stage, n, want)
				}
				rc, err := logs.Open(id, stage, 0)
				if err != nil {
					t.Fatal(err)
				}
				read, err := io.Copy(io.Discard, rc)
				rc.Close()
				if err != nil || read != n {
					t.Errorf("%s read %d bytes, size %d, error %v", stage, read, n, err)
				}
				kept += n
			}
			if kept != task.MaxLogBytes {
				t.Errorf("host kept %d bytes, want %d", kept, task.MaxLogBytes)
			}
			if len(messages) != 0 {
				t.Errorf("supervisor logged %s", strings.Join(messages, "; "))
			}
			h.checkLog(id)
		})
	}
}

func TestLogBatchSurvivesRestart(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 3*taskguest.MaxLogChunk+7)
	h := newHarness(t, func(id task.RunID, s task.Stage) guestsim.Behavior {
		b := script(0, 0)(id, s)
		if s == task.StageCommand {
			b.Output = data
		}
		return b
	})
	reads := 0
	h.sim.Lose = func(_ string, argv []string) bool {
		if len(argv) > 4 && argv[2] == "log" && argv[4] == string(task.StageCommand) {
			reads++
			if reads == 2 {
				h.stopSup()
				return true
			}
		}
		return false
	}
	h.start()
	id := h.submit(time.Hour)
	h.tick()
	h.stop()
	logs := taskstore.MemoryLogs{M: h.store}
	if n, err := logs.Size(id, task.StageCommand); err != nil || n != taskguest.MaxLogChunk {
		t.Fatalf("interrupted batch kept %d bytes, %v, want %d", n, err, taskguest.MaxLogChunk)
	}
	h.start()
	h.until(10, h.ended(id))
	h.stop()
	if outcome(h.run(id)) != task.Succeeded {
		t.Fatal("restart did not preserve the stage outcome")
	}
	rc, err := logs.Open(id, task.StageCommand, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("recovered log has %d bytes, %v, want %d exact bytes", len(got), err, len(data))
	}
}
