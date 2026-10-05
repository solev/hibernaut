package machines_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/machines"
	"github.com/solev/hibernaut/internal/room"
)

func TestFork(t *testing.T) {
	for _, paused := range []bool{false, true} {
		t.Run(map[bool]string{false: "running", true: "paused"}[paused], func(t *testing.T) {
			g := newCopyGuest()
			v := newEnv(t, fake.WithExec(g.exec))
			web := v.createIdle(t, "web", 45*time.Minute)
			if paused {
				if err := v.m.Pause(t.Context(), "web"); err != nil {
					t.Fatal(err)
				}
			}
			m := imageManager(v, nil)
			made, err := m.Fork(t.Context(), "web", 2)
			if err != nil || len(made) != 2 {
				t.Fatalf("Fork = %+v, %v", made, err)
			}
			for i, copy := range made {
				want := []string{"web-1", "web-2"}[i]
				em := v.get(t, copy.ID)
				if copy.Name != want || copy.ForkOf != "web" || copy.Image != "dev" || copy.State != "running" || *copy.IdleSeconds != 45*60 {
					t.Errorf("copy = %+v", copy)
				}
				if em.Metadata["hib.forkof"] != "web" || em.Metadata["hib.idle"] != "45m0s" || len(em.Metadata) != 4 {
					t.Errorf("copy labels = %+v", em.Metadata)
				}
				if got := em.EndAt.Sub(em.StartedAt); got != 45*time.Minute {
					t.Errorf("copy timeout = %v", got)
				}
				if req := g.requests[copy.ID][0]; req.Cmd != "sh" || req.Args[2] != "hibd-reset" || string(g.env[copy.ID]) != "KEEP=yes\n" {
					t.Errorf("copy wasn't re-keyed and given the empty setup")
				}
				c, err := m.Dial(t.Context(), copy.Name)
				if err != nil {
					t.Fatal(err)
				}
				if hibapi.FormatHostKey(c.HostKey) == hibapi.FormatHostKey(g.key) {
					t.Error("copy kept origin's key")
				}
				c.Close()
			}
			if hibapi.FormatHostKey(g.keys[made[0].ID]) == hibapi.FormatHostKey(g.keys[made[1].ID]) {
				t.Error("copies share a host key")
			}
			wantState := engine.Running
			if paused {
				wantState = engine.Paused
			}
			if got := v.get(t, web.ID); got.State != wantState {
				t.Errorf("origin = %s, want %s", got.State, wantState)
			}
			listed, err := m.List(t.Context(), "")
			if err != nil || len(listed) != 3 {
				t.Fatalf("List = %+v, %v", listed, err)
			}
			if !slices.ContainsFunc(listed, func(copy hibapi.Machine) bool { return copy.Name == "web-2" && copy.ForkOf == "web" }) {
				t.Errorf("List lost fork provenance: %+v", listed)
			}
		})
	}
}

func TestForkChoosesLowestFreeNames(t *testing.T) {
	v := newEnv(t, fake.WithExec(newCopyGuest().exec))
	v.create(t, "web")
	for _, name := range []string{"web-1", "web-3", "web-4"} {
		v.create(t, name)
	}
	made, err := imageManager(v, nil).Fork(t.Context(), "web", 2)
	if err != nil || len(made) != 2 || made[0].Name != "web-2" || made[1].Name != "web-5" {
		t.Fatalf("Fork = %+v, %v, want web-2 and web-5", made, err)
	}
}

func TestForkRejectsBeforeTouchingOrigin(t *testing.T) {
	for _, tc := range []struct {
		name    string
		origin  string
		count   int
		durable bool
	}{
		{"zero copies", "web", 0, true},
		{"too many copies", "web", 11, true},
		{"negative copies", "web", -1, true},
		{"too long", strings.Repeat("a", 62), 1, true},
		{"one of ten too long", strings.Repeat("a", 61), 10, true},
		{"no durable", "web", 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := newEnv(t)
			web := v.create(t, tc.origin)
			m := imageManager(v, func(cfg *machines.Config) {
				if !tc.durable {
					cfg.Durable = nil
				}
			})
			made, err := m.Fork(t.Context(), tc.origin, tc.count)
			if err == nil || len(made) != 0 {
				t.Fatalf("Fork = %+v, %v, want refusal", made, err)
			}
			if got := v.get(t, web.ID); got.State != engine.Running || got.StartedAt != web.StartedAt {
				t.Errorf("origin changed: %+v", got)
			}
			if tmpls, _ := v.e.SavedTemplates(t.Context()); len(tmpls) != 0 {
				t.Errorf("templates saved despite preflight failure: %+v", tmpls)
			}
		})
	}
}

func TestForkKeepsWholeCopiesAfterFailure(t *testing.T) {
	v := newEnv(t, fake.WithExec(newCopyGuest().exec))
	v.create(t, "web")
	v.e.FailCreateAfterStart(map[string]string{"hib.name": "web-2", "hib.forkof": "web"})
	m := imageManager(v, nil)
	made, err := m.Fork(t.Context(), "web", 3)
	if len(made) != 1 || made[0].Name != "web-1" || err == nil || !strings.Contains(err.Error(), "made web-1 from web. couldn't make web-2") {
		t.Fatalf("Fork = %+v, %v", made, err)
	}
	ms, _ := m.List(t.Context(), "")
	if len(ms) != 2 || slices.ContainsFunc(ms, func(em hibapi.Machine) bool { return em.Name == "web-2" || em.Name == "web-3" }) {
		t.Errorf("partial copies remain: %+v", ms)
	}
}

func TestForkRoomRetriesWhileCheckpointFreesMemory(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		v := newEnv(t, fake.WithExec(newCopyGuest().exec))
		v.create(t, "web")
		at := time.Now()
		reads := 0
		m := imageManager(v, func(cfg *machines.Config) {
			cfg.ReadRoom = func() (room.Host, error) {
				reads++
				h, _ := roomy()
				if reads > 1 && time.Since(at) < time.Second {
					h.FreePages = 0
				}
				return h, nil
			}
		})
		made, err := m.Fork(t.Context(), "web", 2)
		if err != nil || len(made) != 2 {
			t.Fatalf("Fork = %+v, %v", made, err)
		}
		if elapsed := time.Since(at); elapsed != time.Second {
			t.Errorf("waited %v, want 1s on the fake clock", elapsed)
		}
		if reads < 4 {
			t.Errorf("room read %d times, want retries and a check before each copy", reads)
		}
	})
}

func TestForkRoomWaitIsBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		v := newEnv(t, fake.WithExec(newCopyGuest().exec))
		v.create(t, "web")
		at := time.Now()
		reads := 0
		m := imageManager(v, func(cfg *machines.Config) {
			cfg.ReadRoom = func() (room.Host, error) {
				reads++
				h, _ := roomy()
				if reads > 1 {
					h.FreePages = 0
				}
				return h, nil
			}
		})
		made, err := m.Fork(t.Context(), "web", 1)
		if len(made) != 0 || !errors.Is(err, room.ErrFull) || !strings.Contains(err.Error(), "couldn't make web-1") {
			t.Fatalf("Fork = %+v, %v", made, err)
		}
		if elapsed := time.Since(at); elapsed != 15*time.Second {
			t.Errorf("waited %v, want 15s on the fake clock", elapsed)
		}
	})
}
