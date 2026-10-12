package links

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/statefile"
)

func testStore(t *testing.T) *store {
	t.Helper()
	return NewStore(t.TempDir()).(*store)
}

// asRoot makes s take root's branches while running as the test's user, and records every
// file it gives to the state directory's owner.
func asRoot(t *testing.T, dir string) (statefile.Dir, *[]string) {
	t.Helper()
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	var mu sync.Mutex
	var given []string
	return statefile.Dir{
		Path: dir,
		EUID: func() int { return 0 },
		Chown: func(f *os.File, uid, gid int) error {
			if uid != int(st.Uid) || gid != int(st.Gid) {
				t.Errorf("chown %s to %d:%d, want the directory owner %d:%d", f.Name(), uid, gid, st.Uid, st.Gid)
			}
			mu.Lock()
			given = append(given, filepath.Base(f.Name()))
			mu.Unlock()
			return f.Chown(uid, gid)
		},
	}, &given
}

func publish(id string, ports ...int) func(*Doc) error {
	return func(d *Doc) error {
		if _, err := d.Publish(id, ports); err != nil {
			return err
		}
		_, err := d.Allocate(id)
		return err
	}
}

func writeState(t *testing.T, dir, data string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, StateName), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadWithNoStateIsTheEmptyDocument(t *testing.T) {
	for _, dir := range []string{t.TempDir(), filepath.Join(t.TempDir(), "missing")} {
		doc, err := NewStore(dir).Read()
		if err != nil || doc.V != Version || doc.Machines != nil {
			t.Errorf("Read of %s = %+v, %v; want an empty document with no machines", dir, doc, err)
		}
	}
}

func TestUpdateWritesAnOwnedPrivateDocument(t *testing.T) {
	s := testStore(t)
	owned, given := asRoot(t, s.dir)
	s.owned = owned
	if err := s.Update(t.Context(), publish("sbx-api", 8080, 5432)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"v":1,"machines":{"sbx-api":{"addr":"198.18.0.1","ports":[5432,8080],"portGen":{"5432":1,"8080":1}}},"next":"198.18.0.2"}` + "\n"
	if string(data) != want {
		t.Errorf("links.json is\n%s\nwant\n%s", data, want)
	}
	for _, name := range []string{StateName, LockName} {
		fi, err := os.Stat(filepath.Join(s.dir, name))
		if err != nil || fi.Mode().Perm() != StateMode {
			t.Errorf("%s: %v, %v; want mode %v", name, fi, err, os.FileMode(StateMode))
		}
	}
	// The lock and the document's temporary file are both given away before anyone can
	// see them under their names.
	if len(*given) != 2 || !strings.HasPrefix((*given)[0], "."+LockName+".tmp-") ||
		!strings.HasPrefix((*given)[1], "."+StateName+".tmp-") {
		t.Errorf("chowned %v, want the new lock and the document's temporary file", *given)
	}
	assertNoTemporaries(t, s.dir)

	doc, err := NewStore(s.dir).Read()
	if err != nil {
		t.Fatal(err)
	}
	if m := doc.Machines["sbx-api"]; m == nil || m.Addr != First || len(m.Ports) != 2 {
		t.Errorf("read back %+v", doc.Machines["sbx-api"])
	}
}

func TestUpdateWritesAnEmptyMachinesMap(t *testing.T) {
	s := testStore(t)
	if err := s.Update(t.Context(), func(*Doc) error { return nil }); err != nil {
		t.Fatal(err)
	}
	doc, err := s.Read()
	if err != nil || doc.Machines == nil || len(doc.Machines) != 0 {
		t.Fatalf("Read after an empty update = %+v, %v; want an empty map", doc, err)
	}
}

func TestAFailedUpdateWritesNothing(t *testing.T) {
	s := testStore(t)
	if err := s.Update(t.Context(), publish("sbx-api", 8080)); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(s.Path())
	boom := errors.New("boom")
	err := s.Update(t.Context(), func(d *Doc) error {
		d.Entry("sbx-web").PausedByHand = true
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Update = %v, want f's error", err)
	}
	if err := s.Update(t.Context(), publish("sbx-api", 8081)); !errors.Is(err, ErrReservedPort) {
		t.Fatalf("publishing a reserved port = %v", err)
	}
	after, _ := os.ReadFile(s.Path())
	if string(before) != string(after) {
		t.Errorf("a failed update changed links.json:\n%s\nto\n%s", before, after)
	}
}

func TestUnreadableStateFailsClosed(t *testing.T) {
	for _, tc := range []struct{ name, data string }{
		{"corrupt", `{"v":1,"machines":`},
		{"no version", `{"machines":{}}`},
		{"version zero", `{"v":0}`},
		{"unknown field", `{"v":1,"colour":"blue"}`},
		{"trailing data", `{"v":1}{"v":1}`},
		{"null entry", `{"v":1,"machines":{"sbx-api":null}}`},
		{"address outside the range", `{"v":1,"machines":{"sbx-api":{"addr":"10.0.0.1"}}}`},
		{"shared address", `{"v":1,"machines":{"a":{"addr":"198.18.0.1"},"b":{"addr":"198.18.0.1"}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testStore(t)
			writeState(t, s.dir, tc.data)
			if _, err := s.Read(); !errors.Is(err, ErrStateUnreadable) {
				t.Errorf("Read = %v, want ErrStateUnreadable", err)
			}
			if err := s.Update(t.Context(), func(*Doc) error { return nil }); !errors.Is(err, ErrStateUnreadable) {
				t.Errorf("Update = %v, want ErrStateUnreadable", err)
			}
			if data, _ := os.ReadFile(s.Path()); string(data) != tc.data {
				t.Errorf("an unreadable file was rewritten as %s", data)
			}
		})
	}
}

// A file from a newer hibd is never rewritten, so a downgrade can't silently drop ports, and
// the error says it is newer so hibd port can say "upgrade" rather than "corrupt".
func TestANewerVersionIsRefusedAndKept(t *testing.T) {
	s := testStore(t)
	const newer = `{"v":2,"machines":{},"next":"198.18.0.1","colour":"blue"}`
	writeState(t, s.dir, newer)
	_, err := s.Read()
	var v interface{ NewerVersion() int }
	if !errors.Is(err, ErrStateUnreadable) || !errors.As(err, &v) || v.NewerVersion() != 2 {
		t.Fatalf("Read = %v, want an unreadable error that says it is version 2", err)
	}
	if err := s.Update(t.Context(), publish("sbx-api", 8080)); !errors.Is(err, ErrStateUnreadable) {
		t.Fatalf("Update = %v", err)
	}
	if data, _ := os.ReadFile(s.Path()); string(data) != newer {
		t.Errorf("a newer file was rewritten as %s", data)
	}
	writeState(t, s.dir, `{"v":1,"machines":`)
	if _, err := s.Read(); errors.As(err, &v) {
		t.Errorf("a corrupt file reads as a newer one: %v", err)
	}
}

func TestReadRefusesAFileItShouldNotTrust(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		s := testStore(t)
		writeState(t, s.dir, `{"v":1}`)
		if err := os.Rename(s.Path(), filepath.Join(s.dir, "real.json")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("real.json", s.Path()); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Read(); !errors.Is(err, ErrStateUnreadable) {
			t.Errorf("Read through a symlink = %v", err)
		}
	})
	t.Run("writable by others", func(t *testing.T) {
		s := testStore(t)
		writeState(t, s.dir, `{"v":1}`)
		if err := os.Chmod(s.Path(), 0o622); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Read(); !errors.Is(err, ErrStateUnreadable) {
			t.Errorf("Read of a file others can write = %v", err)
		}
	})
	t.Run("too big", func(t *testing.T) {
		s := testStore(t)
		writeState(t, s.dir, `{"v":1,"next":"`+strings.Repeat(" ", maxState)+`"}`)
		if _, err := s.Read(); !errors.Is(err, ErrStateUnreadable) {
			t.Errorf("Read of an oversized file = %v", err)
		}
	})
}

func TestUpdateWaitsForTheLock(t *testing.T) {
	s := testStore(t)
	unlock, ok, err := s.owned.TryLock(LockName, "test")
	if err != nil || !ok {
		t.Fatalf("TryLock: %v, %v", ok, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	called := false
	err = s.Update(ctx, func(*Doc) error { called = true; return nil })
	if !errors.Is(err, context.DeadlineExceeded) || called {
		t.Fatalf("Update while locked = %v, called %v; want it to wait and give up", err, called)
	}
	unlock()
	if _, err := os.Stat(s.Path()); !os.IsNotExist(err) {
		t.Fatalf("Update wrote while someone else held the lock: %v", err)
	}
}

func TestConcurrentUpdatesLoseNothing(t *testing.T) {
	s := testStore(t)
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Go(func() {
			// Separate stores, as separate hibd processes would have.
			if err := NewStore(s.dir).Update(t.Context(), publish(fmt.Sprintf("sbx-%02d", i), 8000+i)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	doc, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	next := First
	for range 12 {
		next = next.Next()
	}
	if len(doc.Machines) != 12 || doc.Next != next {
		t.Errorf("after 12 concurrent publishes: %d machines, next %v", len(doc.Machines), doc.Next)
	}
}

func TestAFenceIsHeldUntilReleased(t *testing.T) {
	s := testStore(t)
	release, err := s.Fence(t.Context(), "sbx-api", "pause")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	m := doc.Machines["sbx-api"]
	if m == nil || m.Fence == nil || m.Fence.Why != "pause" || m.Fence.Owner != fmt.Sprintf(FenceName, "sbx-api") {
		t.Fatalf("fence = %+v", m)
	}
	alive := func(owner string) (bool, error) { return statefile.Held(s.dir, owner) }
	if state, err := m.Fenced(alive); err != nil || state != FencedLive {
		t.Fatalf("Fenced while held = %v, %v", state, err)
	}
	release()
	release()
	doc, err = s.Read()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := doc.Machines["sbx-api"]; ok {
		t.Errorf("a fence-only entry outlived its release: %+v", doc.Machines["sbx-api"])
	}
	if held, err := statefile.Held(s.dir, m.Fence.Owner); err != nil || held {
		t.Errorf("the fence lock is still held after release: %v, %v", held, err)
	}
}

// A fence clears on a machine that has more to record, and only the fence goes.
func TestReleasingAFenceKeepsTheRestOfTheEntry(t *testing.T) {
	s := testStore(t)
	if err := s.Update(t.Context(), publish("sbx-api", 8080)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	release, err := s.Fence(ctx, "sbx-api", "save")
	if err != nil {
		t.Fatal(err)
	}
	// The pause settles after its caller has gone, and the fence must still come down.
	cancel()
	release()
	doc, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	m := doc.Machines["sbx-api"]
	if m == nil || m.Fence != nil || m.Addr != First || len(m.Ports) != 1 {
		t.Errorf("after release: %+v", m)
	}
}

// A second fence on one machine waits for the first, so the first's release can't clear it.
func TestASecondFenceWaitsForTheFirst(t *testing.T) {
	s := testStore(t)
	first, err := s.Fence(t.Context(), "sbx-api", "pause")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := s.Fence(ctx, "sbx-api", "rm"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a second fence while the first is held = %v", err)
	}
	first()
	second, err := s.Fence(t.Context(), "sbx-api", "rm")
	if err != nil {
		t.Fatal(err)
	}
	defer second()
	doc, _ := s.Read()
	if m := doc.Machines["sbx-api"]; m == nil || m.Fence == nil || m.Fence.Why != "rm" {
		t.Errorf("second fence = %+v", m)
	}
}

func TestAFailedFenceHoldsNothing(t *testing.T) {
	s := testStore(t)
	writeState(t, s.dir, `{"v":1,"machines":`)
	release, err := s.Fence(t.Context(), "sbx-api", "pause")
	if !errors.Is(err, ErrStateUnreadable) || release == nil {
		t.Fatalf("Fence over unreadable state = %v", err)
	}
	release()
	if held, err := statefile.Held(s.dir, fmt.Sprintf(FenceName, "sbx-api")); err != nil || held {
		t.Errorf("a failed fence left its lock held: %v, %v", held, err)
	}
	for _, id := range []string{"", ".hidden", "a/b", "../x"} {
		if _, err := s.Fence(t.Context(), id, "pause"); err == nil {
			t.Errorf("Fence(%q) succeeded", id)
		}
	}
}

func testClock(t *testing.T) *clock {
	t.Helper()
	return NewClock(t.TempDir()).(*clock)
}

var base = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

func TestTheClockOnlyMovesForward(t *testing.T) {
	c := testClock(t)
	if got := c.Active("sbx-api"); !got.IsZero() {
		t.Fatalf("Active with no record = %v", got)
	}
	c.Touch("sbx-api", base)
	c.Touch("sbx-api", base.Add(-time.Minute))
	if got := c.Active("sbx-api"); !got.Equal(base) {
		t.Fatalf("an earlier touch moved the clock to %v", got)
	}
	c.Touch("sbx-api", base.Add(time.Minute))
	if got := c.Active("sbx-api"); !got.Equal(base.Add(time.Minute)) {
		t.Fatalf("a later touch left the clock at %v", got)
	}
	if got := NewClock(c.dir).Active("sbx-api"); !got.Equal(base.Add(time.Minute)) {
		t.Fatalf("another clock over the directory reads %v", got)
	}
	if got := c.Active("sbx-web"); !got.IsZero() {
		t.Fatalf("another machine's clock is %v", got)
	}
}

// Creating the file would stamp it with now, which would turn a late observation into fresh
// activity.
func TestAFirstTouchRecordsItsOwnTime(t *testing.T) {
	c := testClock(t)
	past := time.Now().Add(-time.Hour).Truncate(time.Microsecond)
	c.Touch("sbx-api", past)
	if got := c.Active("sbx-api"); !got.Equal(past) {
		t.Fatalf("a first touch an hour ago reads %v, want %v", got, past)
	}
	for _, dir := range []string{filepath.Join(c.dir, "links"), c.Path()} {
		fi, err := os.Stat(dir)
		if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
			t.Errorf("%s: %v, %v", dir, fi, err)
		}
	}
	fi, err := os.Stat(filepath.Join(c.Path(), "sbx-api"))
	if err != nil || fi.Mode().Perm() != 0o600 || fi.Size() != 0 {
		t.Errorf("activity file: %v, %v", fi, err)
	}
	assertNoTemporaries(t, c.Path())
}

// As root, the directories and the file go to the state directory's owner, the file before it
// is published, so the user's hibd can set explicit times on it later.
func TestRootsFirstTouchIsTheOwners(t *testing.T) {
	c := testClock(t)
	owned, given := asRoot(t, c.dir)
	c.owned = owned
	c.Touch("sbx-api", base)
	if len(*given) != 3 || (*given)[0] != "links" || (*given)[1] != "activity" || !strings.HasPrefix((*given)[2], ".sbx-api.tmp-") {
		t.Fatalf("chowned %v, want links, activity and the unpublished file", *given)
	}
	c.owned = statefile.Dir{Path: c.dir}
	c.Touch("sbx-api", base.Add(time.Second))
	if got := c.Active("sbx-api"); !got.Equal(base.Add(time.Second)) {
		t.Fatalf("the user's touch after root's reads %v", got)
	}
}

func TestConcurrentTouchesKeepTheLatest(t *testing.T) {
	for _, first := range []bool{false, true} {
		t.Run(fmt.Sprintf("first=%v", first), func(t *testing.T) {
			c := testClock(t)
			if !first {
				c.Touch("sbx-api", base)
			}
			times := make([]time.Time, 32)
			for i := range times {
				times[i] = base.Add(time.Duration(i+1) * time.Millisecond)
			}
			rand.Shuffle(len(times), func(i, j int) { times[i], times[j] = times[j], times[i] })
			var wg sync.WaitGroup
			start := make(chan struct{})
			for _, at := range times {
				wg.Go(func() {
					<-start
					NewClock(c.dir).Touch("sbx-api", at)
				})
			}
			close(start)
			wg.Wait()
			if got, want := c.Active("sbx-api"), base.Add(32*time.Millisecond); !got.Equal(want) {
				t.Errorf("after concurrent touches the clock is %v, want %v", got, want)
			}
			assertNoTemporaries(t, c.Path())
		})
	}
}

func TestTheClockIgnoresBadIDs(t *testing.T) {
	c := testClock(t)
	for _, id := range []string{"", ".", "..", ".sbx-api.tmp-1", "a/b", "../escape", "a\x00b"} {
		c.Touch(id, base)
		if got := c.Active(id); !got.IsZero() {
			t.Errorf("Active(%q) = %v", id, got)
		}
	}
	if entries, err := os.ReadDir(c.dir); err != nil || len(entries) != 0 {
		t.Errorf("bad IDs left %v, %v", entries, err)
	}
	c.Touch("sbx-api", time.Time{})
	if _, err := os.Stat(c.Path()); !os.IsNotExist(err) {
		t.Errorf("a zero touch made the clock: %v", err)
	}
}

func assertNoTemporaries(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("left a temporary file %s", e.Name())
		}
	}
}
