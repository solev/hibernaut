package machines_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/machines"
	"github.com/solev/hibernaut/internal/room"
)

func TestGuardOrder(t *testing.T) {
	for _, paused := range []bool{false, true} {
		t.Run(map[bool]string{false: "running", true: "paused"}[paused], func(t *testing.T) {
			v := newEnv(t, fake.WithExec(newCopyGuest().exec))
			v.create(t, "web")
			if paused {
				v.m.Pause(t.Context(), "web")
			}
			var steps []string
			e := &faultEngine{Engine: v.e}
			e.pause = func(ctx context.Context, id string) error {
				steps = append(steps, "pause")
				return v.e.Pause(ctx, id)
			}
			e.resume = func(ctx context.Context, id string, timeout time.Duration) (engine.Machine, error) {
				steps = append(steps, "resume")
				return v.e.Resume(ctx, id, timeout)
			}
			e.save = func(ctx context.Context, id, alias string) (engine.TemplateInfo, error) {
				steps = append(steps, "save")
				return v.e.SaveTemplate(ctx, id, alias)
			}
			m := imageManager(v, func(cfg *machines.Config) {
				cfg.Engine = e
				cfg.Durable = func(ctx context.Context, id string) error {
					steps = append(steps, "durable")
					em, err := v.e.Get(ctx, id)
					if err != nil || em.State != engine.Paused {
						t.Fatalf("Durable = %+v, %v", em, err)
					}
					return nil
				}
			})
			made, err := m.Fork(t.Context(), "web", 1)
			if err != nil || len(made) != 1 {
				t.Fatalf("Fork = %+v, %v", made, err)
			}
			want := []string{"pause", "durable", "resume", "save"}
			if paused {
				want = []string{"durable", "resume", "save", "pause"}
			}
			if !slices.Equal(steps, want) {
				t.Errorf("guard steps = %v, want %v", steps, want)
			}
		})
	}
}

func TestGuardRenewsAfterSlowResume(t *testing.T) {
	at := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return at }
	v := newEnvAt(t, now)
	v.createIdle(t, "web", time.Second)
	e := &faultEngine{Engine: v.e}
	e.resume = func(ctx context.Context, id string, timeout time.Duration) (engine.Machine, error) {
		em, err := v.e.Resume(ctx, id, timeout)
		at = at.Add(time.Minute)
		return em, err
	}
	e.save = func(ctx context.Context, id, alias string) (engine.TemplateInfo, error) {
		em, err := v.e.Get(ctx, id)
		if err != nil {
			return engine.TemplateInfo{}, err
		}
		if em.EndAt.Before(at.Add(2 * time.Minute)) {
			return engine.TemplateInfo{}, errors.New("timeout wasn't renewed after resume")
		}
		return v.e.SaveTemplate(ctx, id, alias)
	}
	m := imageManager(v, func(cfg *machines.Config) {
		cfg.Engine, cfg.Now = e, now
	})
	if _, err := m.SaveImage(t.Context(), "web", "myimg"); err != nil {
		t.Fatalf("SaveImage = %v", err)
	}
}

func TestGuardDurableWaitIsBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		v := newEnv(t)
		web := v.create(t, "web")
		at := time.Now()
		m := imageManager(v, func(cfg *machines.Config) {
			cfg.Durable = func(ctx context.Context, id string) error {
				<-ctx.Done()
				return ctx.Err()
			}
		})
		_, err := m.SaveImage(t.Context(), "web", "myimg")
		if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "snapshot reached disk") {
			t.Fatalf("SaveImage = %v", err)
		}
		if elapsed := time.Since(at); elapsed != 2*time.Minute {
			t.Errorf("Durable waited %v, want 2m", elapsed)
		}
		if em := v.get(t, web.ID); em.State != engine.Running {
			t.Errorf("origin = %s, want running", em.State)
		}
	})
}

func TestManagerChecksWholeOperationPlan(t *testing.T) {
	for _, tc := range []struct {
		name    string
		paused  bool
		copies  int
		pages   int64
		diskGiB int64
	}{
		{"save running memory", false, 0, 255, 80},
		{"save paused memory", true, 0, 511, 80},
		{"save running disk", false, 0, 3200, 4},
		{"save paused disk", true, 0, 3200, 2},
		{"fork running memory", false, 3, 767, 80},
		{"fork paused memory", true, 1, 511, 80},
		{"fork running disk", false, 3, 3200, 8},
		{"fork paused disk", true, 3, 3200, 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := newEnv(t)
			web := v.create(t, "web")
			if tc.paused {
				v.m.Pause(t.Context(), "web")
			}
			before := v.get(t, web.ID)
			m := imageManager(v, func(cfg *machines.Config) {
				cfg.ReadRoom = func() (room.Host, error) {
					return room.Host{PageSize: 2 << 20, Pages: 3200, FreePages: tc.pages, DiskFree: tc.diskGiB << 30}, nil
				}
				cfg.Durable = func(context.Context, string) error {
					t.Error("operation changed the origin before refusing its plan")
					return nil
				}
			})
			var err error
			if tc.copies == 0 {
				_, err = m.SaveImage(t.Context(), "web", "myimg")
			} else {
				_, err = m.Fork(t.Context(), "web", tc.copies)
			}
			if !errors.Is(err, room.ErrFull) {
				t.Fatalf("operation = %v, want ErrFull", err)
			}
			if em := v.get(t, web.ID); em.State != before.State || em.StartedAt != before.StartedAt {
				t.Errorf("origin changed before room refusal: %+v", em)
			}
			if tmpls, _ := v.e.SavedTemplates(t.Context()); len(tmpls) != 0 {
				t.Errorf("unexpected templates: %+v", tmpls)
			}
		})
	}
}

func TestForkChecksAwakeBeforeCopies(t *testing.T) {
	v := newEnv(t, fake.WithExec(newCopyGuest().exec))
	v.create(t, "web")
	sleeping := errors.New("host is going to sleep")
	m := imageManager(v, func(cfg *machines.Config) {
		cfg.Awake = func() error {
			ms, _ := v.e.List(t.Context(), engine.ListOptions{Metadata: map[string]string{"hib.name": "web-1"}})
			if len(ms) != 0 {
				return sleeping
			}
			return nil
		}
	})
	made, err := m.Fork(t.Context(), "web", 2)
	if len(made) != 1 || !errors.Is(err, sleeping) || !strings.Contains(err.Error(), "couldn't make web-2") {
		t.Fatalf("Fork = %+v, %v", made, err)
	}
}

func TestForkAliasesStayFreshOnFixedClock(t *testing.T) {
	at := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	v := newEnvAt(t, func() time.Time { return at }, fake.WithExec(newCopyGuest().exec))
	v.create(t, "web")
	m := imageManager(v, func(cfg *machines.Config) { cfg.Now = func() time.Time { return at } })
	for range 2 {
		if _, err := m.Fork(t.Context(), "web", 1); err != nil {
			t.Fatal(err)
		}
	}
	tmpls, err := v.e.SavedTemplates(t.Context())
	if err != nil || len(tmpls) != 2 || !images.IsForkTemplate(tmpls[0].Alias) || tmpls[0].Alias == tmpls[1].Alias {
		t.Fatalf("fork aliases = %+v, %v", tmpls, err)
	}
}
