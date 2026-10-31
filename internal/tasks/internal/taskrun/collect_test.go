package taskrun

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/taskguest"
	"github.com/solev/hibernaut/internal/taskguest/guestsim"
	"github.com/solev/hibernaut/internal/tasks/internal/taskstore"
)

type emptyLogEngine struct{ Engine }

func (e emptyLogEngine) Exec(ctx context.Context, id string, req engine.ExecRequest) (engine.ExecResult, error) {
	if len(req.Args) > 1 && req.Args[1] == "log" {
		return engine.ExecResult{Stdout: []byte("size=13\n\n")}, nil
	}
	return e.Engine.Exec(ctx, id, req)
}

func TestEmptyLogChunkDoesNotRecordExit(t *testing.T) {
	h := newHarness(t, script(0, 0))
	var logs failureLogs
	h.configure = func(cfg *Config) {
		cfg.Engine = emptyLogEngine{Engine: cfg.Engine}
		cfg.Logf = logs.logf
	}
	h.start()
	id := h.submit(time.Hour)
	for range 4 {
		h.tick()
	}
	h.stop()
	r := h.run(id)
	for _, c := range r.Log {
		if exit, ok := c.Body.(*task.StageExit); ok && exit.Stage == task.StageCommand {
			t.Fatal("recorded command exit over an empty host log copy")
		}
	}
	n, err := (taskstore.MemoryLogs{M: h.store}).Size(id, task.StageCommand)
	if err != nil || n != 0 {
		t.Fatalf("host log size = %d, %v, want 0", n, err)
	}
	logs.mu.Lock()
	defer logs.mu.Unlock()
	if len(logs.lines) == 0 || !strings.Contains(logs.lines[0], "empty chunk before reported size 13") {
		t.Fatalf("short copy was not reported: %q", logs.lines)
	}
}

type growExitedLogs struct {
	taskstore.Store
	h      *harness
	atExit map[task.Stage][]byte
	late   int64
}

func (s *growExitedLogs) Append(ctx context.Context, c task.Checkpoint) error {
	if err := s.Store.Append(ctx, c); err != nil {
		return err
	}
	exit, ok := c.Body.(*task.StageExit)
	if !ok {
		return nil
	}
	logs := taskstore.MemoryLogs{M: s.h.store}
	rc, err := logs.Open(c.Run, exit.Stage, 0)
	if err != nil {
		return err
	}
	before, err := io.ReadAll(rc)
	closeErr := rc.Close()
	if err != nil || closeErr != nil {
		return fmt.Errorf("read exit log: %v, close: %v", err, closeErr)
	}
	s.atExit[exit.Stage] = before
	r, err := s.Store.Run(ctx, c.Run)
	if err != nil {
		return err
	}
	extra := []byte("late " + string(exit.Stage) + "\n")
	data := append(bytes.Clone(before), extra...)
	s.late += int64(len(extra))
	return s.h.sim.Upload(ctx, r.MachineID(), "root", []engine.File{{
		Path: path.Join(taskguest.RunDir(c.Run), string(exit.Stage)+".log"), Size: int64(len(data)),
		Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil },
	}})
}

func TestCollectLeavesExitedLogsFinal(t *testing.T) {
	h := newHarness(t, func(id task.RunID, stage task.Stage) guestsim.Behavior {
		b := script(0, 0)(id, stage)
		b.Output = []byte(string(stage) + " at exit\n")
		return b
	})
	growth := &growExitedLogs{Store: h.store, h: h, atExit: map[task.Stage][]byte{}}
	h.configure = func(cfg *Config) { cfg.Store = growth }
	h.start()
	id := h.submit(time.Hour)
	h.until(30, h.ended(id))
	h.stop()
	var kept int64
	logs := taskstore.MemoryLogs{M: h.store}
	for _, stage := range task.Stages() {
		want := growth.atExit[stage]
		if len(want) == 0 {
			t.Fatalf("stage %s had no exit snapshot", stage)
		}
		rc, err := logs.Open(id, stage, 0)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(rc)
		rc.Close()
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s host log changed after exit: got %q, want %q, %v", stage, got, want, err)
		}
		kept += int64(len(want))
	}
	for _, c := range h.run(id).Log {
		if result, ok := c.Body.(*task.CollectResult); ok {
			if result.LogBytes != kept || result.Dropped != growth.late || growth.late == 0 {
				t.Fatalf("CollectResult = %+v, want kept=%d dropped=%d", result, kept, growth.late)
			}
			return
		}
	}
	t.Fatal("no collect result")
}

type growStoppedLog struct {
	taskstore.Store
	h *harness
}

func (s growStoppedLog) Append(ctx context.Context, c task.Checkpoint) error {
	if err := s.Store.Append(ctx, c); err != nil {
		return err
	}
	if _, stopped := c.Body.(*task.StopResult); !stopped {
		return nil
	}
	r, err := s.Store.Run(ctx, c.Run)
	if err != nil {
		return err
	}
	data := []byte("did the work\nlate stopped\n")
	return s.h.sim.Upload(ctx, r.MachineID(), "root", []engine.File{{
		Path: path.Join(taskguest.RunDir(c.Run), "command.log"), Size: int64(len(data)),
		Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil },
	}})
}

func TestCollectCopiesStoppedStageWithoutExit(t *testing.T) {
	h := newHarness(t, script(time.Hour, 0))
	h.configure = func(cfg *Config) { cfg.Store = growStoppedLog{Store: h.store, h: h} }
	h.start()
	id := h.submit(2 * time.Hour)
	h.until(30, h.launched(id, task.StageCommand))
	h.request(id, task.RequestCancel)
	h.until(10, h.ended(id))
	h.stop()
	for _, c := range h.run(id).Log {
		if exit, ok := c.Body.(*task.StageExit); ok && exit.Stage == task.StageCommand {
			t.Fatal("the stopped command has a StageExit")
		}
	}
	rc, err := (taskstore.MemoryLogs{M: h.store}).Open(id, task.StageCommand, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || string(got) != "did the work\nlate stopped\n" {
		t.Fatalf("stopped stage log = %q, %v, want its final bytes", got, err)
	}
}
