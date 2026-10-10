package linkadmit_test

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/linkadmit"
	"github.com/solev/hibernaut/internal/links"
)

// The reserve is the owner's. Two wakes in flight for different targets each count against it
// before either resume returns, so the second is refused rather than both squeezing in.
func TestDifferentTargetsNeverSpendTheReserve(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		v := newEnv(t)
		v.free.Store(1)
		v.source("src")
		v.target("web", engine.Paused, 8080)
		v.target("db", engine.Paused, 5432)
		release := v.e.holdResumes()

		first := v.admitLater("src", "web", 8080)
		synctest.Wait()
		if _, err := v.admit("src", "db", 5432); !errors.Is(err, links.ErrNoRoom) {
			t.Errorf("a second wake while the first holds the room = %v, want %v", err, links.ErrNoRoom)
		}
		close(release)
		r := <-first
		if r.err != nil || !r.ad.Woke {
			t.Fatalf("the first wake = %+v, %v; want it woken", r.ad, r.err)
		}
		v.a.Release(r.ad)
		if n := v.e.resumed(); n != 1 {
			t.Errorf("resumes = %d, want 1", n)
		}
	})
}

// A wake is charged the room its target takes, counted in default machines. A target twice the
// default size needs room for two besides the reserve, and its wake holds both until it ends.
func TestABigTargetNeverSpendsTheReserve(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		v := newEnv(t)
		v.free.Store(1)
		v.source("src")
		v.target("big", engine.Paused, 8080)
		v.target("db", engine.Paused, 5432)
		v.setNeed("big", 2)
		if ad, err := v.admit("src", "big", 8080); !errors.Is(err, links.ErrNoRoom) {
			v.a.Release(ad)
			t.Fatalf("waking a target that needs 2 with room for 1 = %v, want %v", err, links.ErrNoRoom)
		}

		v.free.Store(2)
		release := v.e.holdResumes()
		first := v.admitLater("src", "big", 8080)
		synctest.Wait()
		if ad, err := v.admit("src", "db", 5432); !errors.Is(err, links.ErrNoRoom) {
			v.a.Release(ad)
			t.Errorf("a default wake while the big one holds the room = %v, want %v", err, links.ErrNoRoom)
		}
		close(release)
		r := <-first
		if r.err != nil || !r.ad.Woke {
			t.Fatalf("waking the target that needs 2 with room for 2 = %+v, %v; want it woken", r.ad, r.err)
		}
		v.a.Release(r.ad)
		if n := v.e.resumed(); n != 1 {
			t.Errorf("resumes = %d, want 1", n)
		}
	})
}

// Connections to one paused target share one wake. Exactly one of them reports that it woke
// the target, so the log has one wake, not one per connection.
func TestOneTargetCoalescesIntoOneWake(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		v := newEnv(t)
		v.free.Store(1)
		v.source("src")
		v.source("cli")
		v.target("web", engine.Paused, 8080)
		release := v.e.holdResumes()

		first := v.admitLater("src", "web", 8080)
		synctest.Wait()
		second := v.admitLater("cli", "web", 8080)
		synctest.Wait()
		if n := linkadmit.Waiters(v.a, "web"); n != 2 {
			t.Fatalf("admissions waiting on the wake = %d, want 2", n)
		}
		close(release)
		r1, r2 := <-first, <-second
		if r1.err != nil || r2.err != nil {
			t.Fatalf("admissions = %v, %v", r1.err, r2.err)
		}
		if r1.ad.Woke == r2.ad.Woke {
			t.Errorf("woke = %v and %v, want exactly one", r1.ad.Woke, r2.ad.Woke)
		}
		if r1.ad.TargetRun != r2.ad.TargetRun || r1.ad.TargetGate != r2.ad.TargetGate {
			t.Errorf("one wake gave two runs: %+v, %+v", r1.ad, r2.ad)
		}
		if n := v.e.resumed(); n != 1 {
			t.Errorf("resumes = %d, want 1", n)
		}
		v.a.Release(r1.ad)
		v.a.Release(r2.ad)
	})
}

// A wake that fails gives back the rate limit and all the room its target takes together, so
// neither the same target nor another one is held off by a wake that never happened.
func TestAFailedWakeRollsBackTheRateAndTheRoom(t *testing.T) {
	for _, need := range []int{1, 2} {
		t.Run(fmt.Sprintf("a target that needs %d", need), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				v := newEnv(t)
				v.free.Store(int64(need))
				v.source("src")
				v.target("web", engine.Paused, 8080)
				v.target("db", engine.Paused, 5432)
				v.setNeed("web", need)
				down := errors.New("orchestrator down")
				v.e.setHook(func(context.Context, string) error { return down })
				if _, err := v.admit("src", "web", 8080); !errors.Is(err, down) {
					t.Fatalf("a failed wake = %v, want %v", err, down)
				}

				v.e.setHook(nil)
				web, err := v.admit("src", "web", 8080)
				if err != nil || !web.Woke {
					t.Fatalf("waking web again = %+v, %v; want it woken", web, err)
				}
				db, err := v.admit("src", "db", 5432)
				if err != nil || !db.Woke {
					t.Fatalf("waking db = %+v, %v; want it woken", db, err)
				}
				v.a.Release(web)
				v.a.Release(db)
			})
		})
	}
}

// Release gives back the caps but keeps a successful wake's rate history, or a connection that
// opens and closes could wake a machine as often as it liked.
func TestReleaseKeepsTheRateHistory(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		v := newEnv(t)
		v.source("src")
		v.target("web", engine.Paused, 8080)
		ad, err := v.admit("src", "web", 8080)
		if err != nil || !ad.Woke {
			t.Fatalf("wake = %+v, %v", ad, err)
		}
		v.a.Release(ad)
		v.a.Release(ad)
		if err := v.e.Pause(context.Background(), "web"); err != nil {
			t.Fatal(err)
		}
		if _, err := v.admit("src", "web", 8080); !errors.Is(err, links.ErrRateLimited) {
			t.Errorf("waking again at once = %v, want %v", err, links.ErrRateLimited)
		}
		time.Sleep(links.WakeEvery)
		v.clock.Touch("src", time.Now())
		ad, err = v.admit("src", "web", 8080)
		if err != nil || !ad.Woke {
			t.Fatalf("waking after WakeEvery = %+v, %v", ad, err)
		}
		v.a.Release(ad)
	})
}

// The target's gate is open before the listener dials: Admit fills it from the Keeper for the
// run the wake started, and holds the woken target to the source's lease.
func TestAdmitFillsTheTargetGateAfterAWake(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		v := newEnv(t)
		v.source("src")
		v.target("web", engine.Paused, 8080)
		ad, err := v.admit("src", "web", 8080)
		if err != nil {
			t.Fatal(err)
		}
		defer v.a.Release(ad)
		run, err := v.runs.Current("web")
		if err != nil {
			t.Fatal(err)
		}
		want := v.keeper.gate("web", run)
		if !ad.Woke || ad.TargetRun != run || !want.Addr.IsValid() || ad.TargetGate != want {
			t.Errorf("admission = %+v, want woken with run %s and gate %+v", ad, run, want)
		}
		if ad.SourceGate != sourceGate || ad.PortGen != 1 || ad.Port != 8080 {
			t.Errorf("admission = %+v, want the listener's source gate and generation 1", ad)
		}
		if lease := links.LeaseUntil(v.clock, "src", time.Now()); !ad.LeaseUntil.Equal(lease) || v.endAt(t, "web").After(lease) {
			t.Errorf("lease %v, web ends %v, want both at most %v", ad.LeaseUntil, v.endAt(t, "web"), lease)
		}
	})
}

// A source that has stopped working is refused, and the links it already holds stop keeping
// their targets up.
func TestAStaleSourceIsRefusedAndStopsRenewing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		v := newEnv(t)
		v.source("src")
		v.target("web", engine.Running, 8080)
		ad, err := v.admit("src", "web", 8080)
		if err != nil {
			t.Fatal(err)
		}
		defer v.a.Release(ad)

		time.Sleep(time.Minute)
		v.clock.Touch("src", time.Now())
		before := v.endAt(t, "web")
		v.a.Renew(ad, 100)
		synctest.Wait()
		if after := v.endAt(t, "web"); !after.After(before) {
			t.Fatalf("a fresh source's renewal left web ending at %v", after)
		}

		time.Sleep(links.LinkGrace)
		stuck := v.endAt(t, "web")
		v.a.Renew(ad, 100)
		synctest.Wait()
		if after := v.endAt(t, "web"); !after.Equal(stuck) {
			t.Errorf("a stale source's renewal moved web's end from %v to %v", stuck, after)
		}
		if _, err := v.admit("src", "web", 8080); !errors.Is(err, links.ErrSourceIdle) {
			t.Errorf("admitting from a stale source = %v, want %v", err, links.ErrSourceIdle)
		}
		if ad.Ctx.Err() != nil {
			t.Errorf("the lease closed an established link: %v", context.Cause(ad.Ctx))
		}
	})
}

func TestAdmitRefuses(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*linkadmit.Config)
		setup     func(v *env)
		source    string
		port      int
		want      error
	}{
		{name: "self-link", source: "web", want: links.ErrSelfLink},
		{name: "running target paused by hand", want: links.ErrPausedByHand, setup: func(v *env) {
			v.store.edit("web", func(m *links.Machine) { m.PausedByHand = true })
		}},
		{name: "fenced source", want: links.ErrFenced, setup: func(v *env) {
			v.store.Fence(context.Background(), "src", "pause")
		}},
		{name: "fenced target", want: links.ErrFenced, setup: func(v *env) {
			v.store.Fence(context.Background(), "web", "save")
		}},
		{name: "unpublished port", port: 9999, want: links.ErrNotPublished},
		{name: "quiescing", want: links.ErrQuiescing, configure: func(c *linkadmit.Config) {
			c.Quiescing = func() bool { return true }
		}},
		{name: "links off", want: links.ErrLinksOff, configure: func(c *linkadmit.Config) { c.Keeper = nil }},
		{name: "never active", want: links.ErrSourceIdle, setup: func(v *env) {
			v.clock.mu.Lock()
			delete(v.clock.at, "src")
			v.clock.mu.Unlock()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var configure []func(*linkadmit.Config)
			if tc.configure != nil {
				configure = append(configure, tc.configure)
			}
			v := newEnv(t, configure...)
			v.source("src")
			v.source("web")
			v.store.publish("web", 8080)
			if tc.setup != nil {
				tc.setup(v)
			}
			ad, err := v.admit(cmp.Or(tc.source, "src"), "web", cmp.Or(tc.port, 8080))
			if !errors.Is(err, tc.want) {
				v.a.Release(ad)
				t.Errorf("admit = %v, want %v", err, tc.want)
			}
		})
	}
}

// A link ends when either of its runs does, which is what the run watcher reports.
func TestALinkEndsWithItsRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		v := newEnv(t)
		v.source("src")
		v.target("web", engine.Running, 8080)
		ad, err := v.admit("src", "web", 8080)
		if err != nil {
			t.Fatal(err)
		}
		defer v.a.Release(ad)
		close(v.runs.watch("web", ad.TargetRun))
		synctest.Wait()
		if cause := context.Cause(ad.Ctx); !errors.Is(cause, links.ErrRunEnded) {
			t.Errorf("after the target's run ended the link's cause is %v, want %v", cause, links.ErrRunEnded)
		}
	})
}

// A pause leaves StartedAt alone, and a cgroup can linger, so neither the run nor the start time
// tells a paused machine from a running one. The sweep asks the engine for Running.
func TestTheSweepCatchesAPausedMachineWhoseRunDidNotMove(t *testing.T) {
	v := newEnv(t)
	v.source("src")
	v.target("web", engine.Running, 8080)
	ad, err := v.admit("src", "web", 8080)
	if err != nil {
		t.Fatal(err)
	}
	defer v.a.Release(ad)
	linkadmit.Sweep(v.a, t.Context())
	if ad.Ctx.Err() != nil {
		t.Fatalf("the sweep closed a healthy link: %v", context.Cause(ad.Ctx))
	}

	v.runs.pin("web", ad.TargetRun)
	before, _ := v.e.Get(t.Context(), "web")
	if err := v.e.Pause(t.Context(), "web"); err != nil {
		t.Fatal(err)
	}
	if after, _ := v.e.Get(t.Context(), "web"); !after.StartedAt.Equal(before.StartedAt) {
		t.Fatalf("the fake moved StartedAt on pause")
	}
	linkadmit.Sweep(v.a, t.Context())
	if cause := context.Cause(ad.Ctx); !errors.Is(cause, links.ErrRunEnded) {
		t.Errorf("after the sweep the link's cause is %v, want %v", cause, links.ErrRunEnded)
	}
}

// The sweep also closes a link whose port has been unpublished or published again since.
func TestTheSweepCatchesARepublishedPort(t *testing.T) {
	v := newEnv(t)
	v.source("src")
	v.target("web", engine.Running, 8080, 5432)
	web, err := v.admit("src", "web", 8080)
	if err != nil {
		t.Fatal(err)
	}
	defer v.a.Release(web)
	db, err := v.admit("src", "web", 5432)
	if err != nil {
		t.Fatal(err)
	}
	defer v.a.Release(db)
	v.store.publish("web", 8080)
	linkadmit.Sweep(v.a, t.Context())
	if cause := context.Cause(web.Ctx); !errors.Is(cause, links.ErrRevoked) {
		t.Errorf("a republished port's link has cause %v, want %v", cause, links.ErrRevoked)
	}
	if db.Ctx.Err() != nil {
		t.Errorf("the sweep closed the link on the other port: %v", context.Cause(db.Ctx))
	}
}
