package taskrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/taskguest/guestsim"
	"github.com/solev/hibernaut/internal/tasks/internal/taskstore"
)

type publisherFunc func(context.Context, task.Run, task.Publication, string) (task.Publication, error)

func (f publisherFunc) Publish(ctx context.Context, r task.Run, p task.Publication, title string) (task.Publication, error) {
	return f(ctx, r, p, title)
}

func publicationSubmit(t *testing.T, h *harness, mode task.Publish, prompt string) task.RunID {
	t.Helper()
	sp := task.Spec{Repo: task.Repo{Owner: "solev", Name: "demo"}, Publish: mode,
		Command: []string{"codex", "exec"}, Image: "dev", Size: "dev", Timeout: time.Minute}
	if prompt != "" {
		sp.Inputs = []task.File{{Name: task.PromptName, Size: int64(len(prompt))}}
	}
	rc, err := h.store.Submit(t.Context(), task.Submission{RequestID: "publication-test",
		Spec: sp, Actor: alice, At: h.clock.Now()}, strings.NewReader(prompt))
	if err != nil {
		t.Fatal(err)
	}
	return rc.Run
}

func TestPublishResultAndTitle(t *testing.T) {
	for _, prompt := range []string{"", "Fix the bug\nPrivate remainder", strings.Repeat("x", 5000)} {
		t.Run(fmt.Sprint("bytes=", len(prompt)), func(t *testing.T) {
			h := newHarness(t, script(0, 0))
			calls := 0
			h.configure = func(cfg *Config) {
				cfg.Publisher = publisherFunc(func(ctx context.Context, r task.Run, p task.Publication, title string) (task.Publication, error) {
					calls++
					want, _, _ := strings.Cut(prompt[:min(len(prompt), 4096)], "\n")
					if title != want || p.Commit == "" || !r.Publishable() {
						t.Errorf("Publisher received title=%q publication=%+v publishable=%v", title, p, r.Publishable())
					}
					p.PR, p.Author = "https://github.com/solev/demo/pull/42", "github-user"
					return p, nil
				})
			}
			id := publicationSubmit(t, h, task.PublishDraftPR, prompt)
			h.start()
			h.until(30, h.ended(id))
			h.stop()
			r := h.run(id)
			p := r.Publication()
			if calls != 1 || outcome(r) != task.Succeeded || p == nil ||
				p.PR != "https://github.com/solev/demo/pull/42" || p.Author != "github-user" {
				t.Fatalf("Publisher calls=%d, status=%+v publication=%+v", calls, r.Status(""), p)
			}
		})
	}
}

type titleStore struct {
	taskstore.Store
	publishing *atomic.Bool
	attempts   *atomic.Int32
	readBytes  *atomic.Int64
	mode       string
}

func (s titleStore) Blob(hash task.Hash) (io.ReadCloser, error) {
	if !s.publishing.Load() {
		return s.Store.Blob(hash)
	}
	first := s.attempts.Add(1) == 1
	if first && s.mode == "open" {
		return nil, errors.New("title blob open failed")
	}
	rc, err := s.Store.Blob(hash)
	if err != nil {
		return nil, err
	}
	return titleReader{ReadCloser: rc, readBytes: s.readBytes,
		readError: first && s.mode == "read", closeError: first && s.mode == "close"}, nil
}

type titleReader struct {
	io.ReadCloser
	readBytes             *atomic.Int64
	readError, closeError bool
}

func (r titleReader) Read(b []byte) (int, error) {
	if r.readError {
		return 0, errors.New("title blob read failed")
	}
	n, err := r.ReadCloser.Read(b)
	r.readBytes.Add(int64(n))
	return n, err
}

func (r titleReader) Close() error {
	err := r.ReadCloser.Close()
	if r.closeError {
		return errors.Join(err, errors.New("title blob close failed"))
	}
	return err
}

func TestPublishTitleBoundAndReadErrors(t *testing.T) {
	for _, mode := range []string{"bounded", "open", "read", "close"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t, script(0, 0))
			var publishing atomic.Bool
			var attempts atomic.Int32
			var readBytes atomic.Int64
			calls := 0
			h.store.FailAppends(func(c task.Checkpoint) error {
				if c.Step() == task.StepPublish {
					publishing.Store(true)
				}
				return nil
			})
			h.configure = func(cfg *Config) {
				cfg.Store = titleStore{Store: cfg.Store, publishing: &publishing,
					attempts: &attempts, readBytes: &readBytes, mode: mode}
				cfg.Publisher = publisherFunc(func(ctx context.Context, r task.Run, p task.Publication, title string) (task.Publication, error) {
					calls++
					if title != "Fix the bug" {
						t.Errorf("title=%q, want first line", title)
					}
					p.PR = "https://github.com/solev/demo/pull/42"
					return p, nil
				})
			}
			id := publicationSubmit(t, h, task.PublishDraftPR, "Fix the bug\n"+strings.Repeat("x", 8000))
			h.start()
			h.until(30, h.ended(id))
			h.stop()
			want := int32(2)
			if mode == "bounded" {
				want = 1
			}
			if calls != 1 || attempts.Load() != want || readBytes.Load() > int64(want)*4096 ||
				outcome(h.run(id)) != task.Succeeded {
				t.Fatalf("title mode=%s Publisher calls=%d reads=%d bytes=%d status=%+v",
					mode, calls, attempts.Load(), readBytes.Load(), h.run(id).Status(""))
			}
			if mode != "bounded" {
				h.logs.mu.Lock()
				defer h.logs.mu.Unlock()
				if len(h.logs.lines) != 2 || !strings.Contains(h.logs.lines[0], "read publication title") {
					t.Fatalf("title read errors were not surfaced: %q", h.logs.lines)
				}
			}
		})
	}
}

func TestPublishPermanentOrMissing(t *testing.T) {
	for _, detail := range []string{"GitHub refuses draft pull requests", "this hibd can't open pull requests",
		"GitHub\nrefused\x00\x1b" + strings.Repeat("x", 5000)} {
		t.Run(detail[:min(len(detail), 20)], func(t *testing.T) {
			h := newHarness(t, script(0, 0))
			calls := 0
			h.configure = func(cfg *Config) {
				cfg.Publisher = nil
				if detail != "this hibd can't open pull requests" {
					cfg.Publisher = publisherFunc(func(context.Context, task.Run, task.Publication, string) (task.Publication, error) {
						calls++
						return task.Publication{}, errors.New(detail)
					})
				}
			}
			h.start()
			id := h.submit(time.Hour)
			h.until(30, h.ended(id))
			h.stop()
			r := h.run(id)
			st := r.Status("")
			if st.Outcome != task.Failed || st.Stage != task.StageFinish ||
				len(st.Detail) > 4096 || strings.ContainsAny(st.Detail, "\n\x00\x1b") ||
				(len(detail) < 4096 && st.Detail != detail) || calls > 1 {
				t.Fatalf("permanent publication calls=%d status=%+v", calls, st)
			}
			h.logs.check(t, nil)
		})
	}
}

func TestPublishBudgetAfterRestart(t *testing.T) {
	h := newHarness(t, script(0, 0))
	calls := 0
	h.configure = func(cfg *Config) {
		cfg.Publisher = publisherFunc(func(context.Context, task.Run, task.Publication, string) (task.Publication, error) {
			calls++
			return task.Publication{}, fmt.Errorf("GitHub unavailable %d: %w", calls, ErrPublishRetry)
		})
	}
	h.start()
	id := h.submit(time.Hour)
	h.until(5, func() bool {
		for _, c := range h.run(id).Log {
			if c.Step() == task.StepPublish {
				return true
			}
		}
		return false
	})
	h.stop()
	var intent time.Time
	for _, c := range h.run(id).Log {
		if c.Step() == task.StepPublish {
			intent = c.At
		}
	}
	restarted := intent.Add(4*time.Minute + 59*time.Second)
	h.clock.Advance(restarted.Sub(h.clock.Now()))
	h.start()
	for range 298 {
		h.tick()
	}
	if h.ended(id)() {
		t.Fatal("restart spent downtime from the new publish budget")
	}
	h.until(10, h.ended(id))
	h.stop()
	r := h.run(id)
	last, _ := r.Last()
	want := fmt.Sprintf("GitHub unavailable %d: %s", calls, ErrPublishRetry)
	if st := r.Status(""); st.Outcome != task.Failed || st.Stage != task.StageFinish ||
		st.Detail != want || last.At.Sub(restarted) < 5*time.Minute {
		t.Fatalf("publish after restart elapsed=%s calls=%d status=%+v", last.At.Sub(restarted), calls, st)
	}
}

func TestPublishBudgetClockJumpWithoutError(t *testing.T) {
	h := newHarness(t, script(0, 0))
	calls := 0
	h.configure = func(cfg *Config) {
		cfg.Publisher = publisherFunc(func(context.Context, task.Run, task.Publication, string) (task.Publication, error) {
			calls++
			return task.Publication{}, errors.New("must not be called after budget exhaustion")
		})
	}
	h.store.FailAppends(func(c task.Checkpoint) error {
		if c.Step() == task.StepPublish && c.Kind() == task.Intent {
			h.clock.Advance(5 * time.Minute)
		}
		return nil
	})
	h.start()
	id := h.submit(time.Hour)
	h.until(5, h.ended(id))
	h.stop()
	st := h.run(id).Status("")
	if calls != 0 || st.Outcome != task.Failed || st.Stage != task.StageFinish ||
		st.Detail != "pull request publication budget exhausted" {
		t.Fatalf("clock jump calls=%d status=%+v", calls, st)
	}
}

func TestPublishLogsFailuresByTime(t *testing.T) {
	h := newHarness(t, script(0, 0))
	calls := 0
	h.configure = func(cfg *Config) {
		cfg.Publisher = publisherFunc(func(context.Context, task.Run, task.Publication, string) (task.Publication, error) {
			calls++
			return task.Publication{}, fmt.Errorf("GitHub address %d unavailable: %w", calls, ErrPublishRetry)
		})
	}
	h.start()
	id := h.submit(time.Hour)
	h.until(310, h.ended(id))
	h.stop()
	h.logs.mu.Lock()
	defer h.logs.mu.Unlock()
	if calls < 200 || len(h.logs.lines) != 2 ||
		!strings.Contains(h.logs.lines[1], "the workflow ended (") ||
		outcome(h.run(id)) != task.Failed {
		t.Fatalf("publish calls=%d logs=%q status=%+v", calls, h.logs.lines, h.run(id).Status(""))
	}
}

func TestPublishLostReply(t *testing.T) {
	h := newHarness(t, script(0, 0))
	calls, created := 0, 0
	var existing task.Publication
	h.configure = func(cfg *Config) {
		cfg.Publisher = publisherFunc(func(ctx context.Context, r task.Run, p task.Publication, title string) (task.Publication, error) {
			calls++
			if existing.PR == "" {
				created++
				existing = p
				existing.PR = "https://github.com/solev/demo/pull/42"
				h.stopSup()
				return task.Publication{}, context.Canceled
			}
			existing.Author = "recovered"
			return existing, nil
		})
	}
	h.start()
	id := h.submit(time.Hour)
	h.until(5, func() bool { return calls > 0 })
	h.stop()
	h.start()
	h.until(30, h.ended(id))
	h.stop()
	p := h.run(id).Publication()
	if calls != 2 || created != 1 || p == nil || p.Author != "recovered" || outcome(h.run(id)) != task.Succeeded {
		t.Fatalf("lost GitHub reply calls=%d created=%d publication=%+v", calls, created, p)
	}
}

func TestPublishRequestsAndLife(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline", "remove", "daemon", "stale"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newHarness(t, script(0, 0))
				h.clock.now = time.Now()
				release := make(chan struct{})
				var calls atomic.Int32
				h.configure = func(cfg *Config) {
					cfg.Clock = RealClock{}
					cfg.Publisher = publisherFunc(func(ctx context.Context, r task.Run, p task.Publication, title string) (task.Publication, error) {
						n := calls.Add(1)
						if mode == "stale" && n == 1 {
							return task.Publication{}, taskstore.ErrStale
						}
						select {
						case <-ctx.Done():
							return task.Publication{}, context.Cause(ctx)
						case <-release:
							p.PR = "https://github.com/solev/demo/pull/42"
							return p, nil
						}
					})
				}
				h.start()
				id := h.submit(30 * time.Second)
				bubbleUntil(t, h, 5*time.Second, func() bool { return calls.Load() > 0 })
				switch mode {
				case "cancel", "remove":
					kind := task.RequestCancel
					if mode == "remove" {
						kind = task.RequestRemove
					}
					h.request(id, kind)
					bubbleUntil(t, h, 5*time.Second, func() bool { return calls.Load() > 1 })
				case "deadline":
					time.Sleep(31 * time.Second)
					h.clock.Advance(31 * time.Second)
				case "daemon":
					h.stop()
					h.start()
				}
				close(release)
				bubbleUntil(t, h, 10*time.Second, h.ended(id))
				h.stop()
				r := h.run(id)
				if outcome(r) != task.Succeeded || r.Publication() == nil || r.Publication().PR == "" {
					t.Fatalf("%s changed publish result: %+v", mode, r.Status(""))
				}
				_, err := h.sim.Get(t.Context(), r.MachineID())
				if mode == "remove" && !errors.Is(err, engine.ErrNotFound) {
					t.Fatalf("rm did not remove the machine after publish: %v", err)
				}
				h.logs.check(t, nil)
			})
		})
	}
}

func TestPublishEligibility(t *testing.T) {
	for _, mode := range []string{"success", "failed", "finish-failed", "timeout", "finish-timeout", "cancel", "missing"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t, func(id task.RunID, s task.Stage) guestsim.Behavior {
				b := script(0, 0)(id, s)
				if (mode == "failed" && s == task.StageCommand) ||
					(mode == "finish-failed" && s == task.StageFinish) {
					b.Status = 1
				}
				if (mode == "timeout" && s == task.StageCommand) ||
					(mode == "finish-timeout" && s == task.StageFinish) {
					b.Runs = time.Hour
				}
				if mode == "missing" {
					b.Publish = nil
				}
				return b
			})
			calls := 0
			h.configure = func(cfg *Config) {
				cfg.Publisher = publisherFunc(func(ctx context.Context, r task.Run, p task.Publication, title string) (task.Publication, error) {
					calls++
					p.PR = "https://github.com/solev/demo/pull/42"
					return p, nil
				})
			}
			id := h.submit(20 * time.Second)
			if mode == "cancel" {
				h.request(id, task.RequestCancel)
			}
			h.start()
			h.until(50, h.ended(id))
			h.stop()
			r := h.run(id)
			want, stage := task.Succeeded, task.Stage("")
			switch mode {
			case "failed":
				want, stage = task.Failed, task.StageCommand
			case "missing":
				want, stage = task.Failed, task.StageFinish
			case "finish-failed":
				want, stage = task.Failed, task.StageFinish
			case "timeout":
				want, stage = task.TimedOut, task.StageCommand
			case "finish-timeout":
				want, stage = task.TimedOut, task.StageFinish
			case "cancel":
				want = task.Cancelled
			}
			o, gotStage := r.Outcome()
			if o != want || gotStage != stage || (calls == 1) != (mode == "success") {
				t.Fatalf("%s calls=%d outcome=%s at %s, want %s at %s", mode, calls, o, gotStage, want, stage)
			}
			if mode == "missing" && r.Status("").Detail !=
				"the host could not read what finish published; the work is in the kept machine" {
				t.Fatalf("missing publication detail=%q", r.Status("").Detail)
			}
			if mode != "success" {
				var collected, published *task.Publication
				for _, c := range r.Log {
					switch b := c.Body.(type) {
					case *task.CollectResult:
						collected = b.Publication
					case *task.PublishResult:
						published = b.Publication
						if b.Failed != "" {
							t.Fatalf("ineligible run recorded publish failure %q", b.Failed)
						}
					}
				}
				if !reflect.DeepEqual(collected, published) {
					t.Fatalf("ineligible publish changed collected publication: %+v, %+v", collected, published)
				}
			}
		})
	}
}

func TestPublishAfterMachinePausedOrGone(t *testing.T) {
	for _, mode := range []string{"paused", "gone", "remove-paused"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t, script(0, 0))
			h.configure = func(cfg *Config) {
				cfg.Publisher = publisherFunc(func(ctx context.Context, r task.Run, p task.Publication, title string) (task.Publication, error) {
					var err error
					if mode == "gone" {
						err = h.sim.Kill(ctx, r.MachineID())
					} else {
						err = h.sim.Pause(ctx, r.MachineID())
					}
					if mode == "remove-paused" {
						h.request(r.ID, task.RequestRemove)
					}
					p.PR = "https://github.com/solev/demo/pull/42"
					return p, err
				})
			}
			h.start()
			id := h.submit(time.Hour)
			h.until(30, h.ended(id))
			h.stop()
			r := h.run(id)
			_, err := h.sim.Get(t.Context(), r.MachineID())
			if outcome(r) != task.Succeeded || r.Publication() == nil || r.Publication().PR == "" ||
				(mode == "paused" && r.Machine != task.MachinePaused) ||
				(mode != "paused" && !errors.Is(err, engine.ErrNotFound)) {
				t.Fatalf("%s after publish status=%+v machine=%s lookup=%v", mode, r.Status(""), r.Machine, err)
			}
			h.logs.check(t, nil)
		})
	}
}

func TestPublishNoneCompatibility(t *testing.T) {
	for _, mode := range []task.Publish{"", task.PublishNone} {
		t.Run(string(mode), func(t *testing.T) {
			h := newHarness(t, script(0, 0))
			calls := 0
			h.configure = func(cfg *Config) {
				cfg.Publisher = publisherFunc(func(context.Context, task.Run, task.Publication, string) (task.Publication, error) {
					calls++
					return task.Publication{}, errors.New("must not publish")
				})
			}
			id := publicationSubmit(t, h, mode, "")
			h.start()
			h.until(30, h.ended(id))
			h.stop()
			r := h.run(id)
			if calls != 0 || outcome(r) != task.Succeeded || r.Publication() == nil || r.Publication().PR != "" {
				t.Fatalf("legacy publish=%q calls=%d status=%+v publication=%+v", mode, calls, r.Status(""), r.Publication())
			}
			for _, c := range r.Log {
				if c.Step() == task.StepPublish {
					t.Fatal("legacy run entered publish")
				}
			}
		})
	}
}
