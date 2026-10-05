package machines_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/machines"
	"github.com/solev/hibernaut/internal/room"
)

type faultEngine struct {
	engine.Engine
	pause  func(context.Context, string) error
	resume func(context.Context, string, time.Duration) (engine.Machine, error)
	get    func(context.Context, string) (engine.Machine, error)
	save   func(context.Context, string, string) (engine.TemplateInfo, error)
	kill   func(context.Context, string) error
}

func (e *faultEngine) Pause(ctx context.Context, id string) error {
	if e.pause != nil {
		return e.pause(ctx, id)
	}
	return e.Engine.Pause(ctx, id)
}

func (e *faultEngine) Resume(ctx context.Context, id string, timeout time.Duration) (engine.Machine, error) {
	if e.resume != nil {
		return e.resume(ctx, id, timeout)
	}
	return e.Engine.Resume(ctx, id, timeout)
}

func (e *faultEngine) Get(ctx context.Context, id string) (engine.Machine, error) {
	if e.get != nil {
		return e.get(ctx, id)
	}
	return e.Engine.Get(ctx, id)
}

func (e *faultEngine) SaveTemplate(ctx context.Context, id, alias string) (engine.TemplateInfo, error) {
	if e.save != nil {
		return e.save(ctx, id, alias)
	}
	return e.Engine.SaveTemplate(ctx, id, alias)
}

func (e *faultEngine) Kill(ctx context.Context, id string) error {
	if e.kill != nil {
		return e.kill(ctx, id)
	}
	return e.Engine.Kill(ctx, id)
}

func noSavedAlias(t *testing.T, v *env) {
	t.Helper()
	if _, err := v.e.Template(t.Context(), images.SavedTemplate("myimg")); !errors.Is(err, engine.ErrNoTemplate) {
		t.Errorf("alias remains: %v", err)
	}
}

func TestSaveGuardRefusesWithoutDurable(t *testing.T) {
	v := newEnv(t)
	web := v.create(t, "web")
	m := imageManager(v, func(cfg *machines.Config) { cfg.Durable = nil })
	if _, err := m.SaveImage(t.Context(), "web", "myimg"); err == nil || !strings.Contains(err.Error(), "confirm snapshots reached disk") {
		t.Fatalf("SaveImage = %v", err)
	}
	if got := v.get(t, web.ID); got.State != engine.Running || got.StartedAt != web.StartedAt {
		t.Errorf("origin changed: %+v", got)
	}
	noSavedAlias(t, v)
}

func TestSaveGuardFailures(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*testing.T, *env, *faultEngine, *machines.Config)
		state     engine.State
		missing   bool
		message   string
	}{
		{
			name: "pause fails with machine still running", state: engine.Running, message: "web is running",
			configure: func(t *testing.T, v *env, e *faultEngine, cfg *machines.Config) {
				e.pause = func(context.Context, string) error { return errors.New("pause refused") }
			},
		},
		{
			name: "pause loses the machine", missing: true, message: "web is gone",
			configure: func(t *testing.T, v *env, e *faultEngine, cfg *machines.Config) {
				e.pause = func(ctx context.Context, id string) error {
					if err := v.e.Kill(ctx, id); err != nil {
						t.Fatal(err)
					}
					return errors.New("snapshot couldn't be written")
				}
			},
		},
		{
			name: "durable fails", state: engine.Running, message: "hibd couldn't confirm web's snapshot reached disk",
			configure: func(t *testing.T, v *env, e *faultEngine, cfg *machines.Config) {
				cfg.Durable = func(context.Context, string) error { return errors.New("build files incomplete") }
			},
		},
		{
			name: "resume fails before save", state: engine.Paused, message: "web is paused at a safe snapshot. Wake it with: ssh web.hib",
			configure: func(t *testing.T, v *env, e *faultEngine, cfg *machines.Config) {
				e.resume = func(context.Context, string, time.Duration) (engine.Machine, error) {
					return engine.Machine{}, errors.New("resume failed")
				}
			},
		},
		{
			name: "resume starts then fails", state: engine.Running, message: "web is running",
			configure: func(t *testing.T, v *env, e *faultEngine, cfg *machines.Config) {
				e.resume = func(ctx context.Context, id string, timeout time.Duration) (engine.Machine, error) {
					v.e.Resume(ctx, id, timeout)
					return engine.Machine{}, errors.New("reading resumed machine failed")
				}
			},
		},
		{
			name: "unknown save", state: engine.Running, message: "E2B couldn't confirm the save of web",
			configure: func(t *testing.T, v *env, e *faultEngine, cfg *machines.Config) {
				v.e.FailNextSave(errors.New("unknown\x1b]52;c;bad\x07 outcome"))
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := newEnv(t)
			web := v.create(t, "web")
			e := &faultEngine{Engine: v.e}
			m := imageManager(v, func(cfg *machines.Config) {
				cfg.Engine = e
				tc.configure(t, v, e, cfg)
			})
			_, err := m.SaveImage(t.Context(), "web", "myimg")
			if err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("SaveImage = %v, want %s", err, tc.message)
			}
			if strings.ContainsRune(err.Error(), 0x1b) || strings.ContainsRune(err.Error(), 7) {
				t.Errorf("unsafe error: %q", err)
			}
			em, err := v.e.Get(t.Context(), web.ID)
			if tc.missing {
				if !errors.Is(err, engine.ErrNotFound) {
					t.Errorf("origin survived pause failure: %+v, %v", em, err)
				}
			} else if err != nil || em.State != tc.state {
				t.Errorf("origin = %+v, %v, want %s", em, err, tc.state)
			}
			noSavedAlias(t, v)
		})
	}
}

func TestSaveFailedResumeReportsUnknownState(t *testing.T) {
	for _, checkpoint := range []bool{false, true} {
		t.Run(map[bool]string{false: "initial resume", true: "checkpoint recovery"}[checkpoint], func(t *testing.T) {
			v := newEnv(t)
			v.create(t, "web")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			e := &faultEngine{Engine: v.e}
			calls := 0
			e.resume = func(ctx context.Context, id string, timeout time.Duration) (engine.Machine, error) {
				calls++
				if checkpoint && calls == 1 {
					return v.e.Resume(ctx, id, timeout)
				}
				cancel()
				return engine.Machine{}, errors.New("resume response lost")
			}
			e.get = func(ctx context.Context, id string) (engine.Machine, error) {
				if _, ok := ctx.Deadline(); !ok || ctx.Err() != nil {
					t.Error("state read must have its own uncancelled, bounded context")
				}
				return engine.Machine{}, errors.New("state read failed")
			}
			m := imageManager(v, func(cfg *machines.Config) { cfg.Engine = e })
			if checkpoint {
				v.e.FailNextSave(engine.ErrCheckpoint)
			}
			_, err := m.SaveImage(ctx, "web", "myimg")
			if err == nil || !strings.Contains(err.Error(), "state is unknown") || !strings.Contains(err.Error(), "hib ls") || strings.Contains(err.Error(), "is paused") {
				t.Fatalf("SaveImage state claim = %v", err)
			}
			noSavedAlias(t, v)
		})
	}
}

func TestSaveCancelledDurableRestoresOrigin(t *testing.T) {
	v := newEnv(t)
	web := v.create(t, "web")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	m := imageManager(v, func(cfg *machines.Config) {
		cfg.Durable = func(context.Context, string) error {
			cancel()
			return context.Canceled
		}
	})
	_, err := m.SaveImage(ctx, "web", "myimg")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("SaveImage = %v, want Canceled", err)
	}
	if em := v.get(t, web.ID); em.State != engine.Running {
		t.Errorf("origin = %s, want running", em.State)
	}
	noSavedAlias(t, v)
}

func TestSaveChecksAwakeAndRoomBeforeChanges(t *testing.T) {
	for _, atSave := range []bool{false, true} {
		t.Run(map[bool]string{false: "as it starts", true: "before save"}[atSave], func(t *testing.T) {
			v := newEnv(t)
			web := v.create(t, "web")
			awake := 0
			sleeping := errors.New("host is going to sleep")
			m := imageManager(v, func(cfg *machines.Config) {
				cfg.Awake = func() error {
					awake++
					if !atSave || awake == 2 {
						return sleeping
					}
					return nil
				}
			})
			_, err := m.SaveImage(t.Context(), "web", "myimg")
			if !errors.Is(err, sleeping) {
				t.Fatalf("SaveImage = %v, want sleeping", err)
			}
			if em := v.get(t, web.ID); em.State != engine.Running {
				t.Errorf("origin = %s, want running", em.State)
			}
			noSavedAlias(t, v)
		})
	}
	v := newEnv(t)
	web := v.create(t, "web")
	m := imageManager(v, func(cfg *machines.Config) {
		cfg.ReadRoom = func() (room.Host, error) {
			return room.Host{PageSize: 2 << 20, Pages: 3200, DiskFree: 80 << 30}, nil
		}
		cfg.Durable = func(context.Context, string) error {
			t.Error("Durable called despite no room")
			return nil
		}
	})
	if _, err := m.SaveImage(t.Context(), "web", "myimg"); !errors.Is(err, room.ErrFull) {
		t.Fatalf("SaveImage = %v, want ErrFull", err)
	}
	if em := v.get(t, web.ID); em.State != engine.Running || em.StartedAt != web.StartedAt {
		t.Errorf("origin changed before room check: %+v", em)
	}
}
