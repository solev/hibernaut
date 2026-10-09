package statefile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestTryLockExcludesASecondHolder(t *testing.T) {
	dir := t.TempDir()
	unlock, ok, err := TryLock(dir, "test.lock", "test")
	if err != nil || !ok {
		t.Fatalf("first TryLock: ok %v, err %v", ok, err)
	}
	again, ok, err := TryLock(dir, "test.lock", "test")
	if err != nil {
		t.Fatalf("second TryLock: %v", err)
	}
	if ok || again != nil {
		t.Fatal("the second TryLock took a lock someone else held")
	}
	unlock()
	unlock, ok, err = TryLock(dir, "test.lock", "test")
	if err != nil || !ok {
		t.Fatalf("TryLock after the release: ok %v, err %v", ok, err)
	}
	unlock()
}

func TestWaitLockWaitsThenTakesIt(t *testing.T) {
	dir := t.TempDir()
	unlock, ok, err := TryLock(dir, "test.lock", "test")
	if err != nil || !ok {
		t.Fatalf("TryLock: ok %v, err %v", ok, err)
	}
	done := make(chan error, 1)
	go func() {
		release, err := WaitLock(context.Background(), dir, "test.lock", "test")
		if release != nil {
			release()
		}
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("WaitLock returned %v while the lock was held", err)
	case <-time.After(50 * time.Millisecond):
	}
	unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WaitLock after the release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitLock never returned after the lock was released")
	}
}

func TestWaitLockGivesUpWithTheContext(t *testing.T) {
	dir := t.TempDir()
	unlock, ok, err := TryLock(dir, "test.lock", "test")
	if err != nil || !ok {
		t.Fatalf("TryLock: ok %v, err %v", ok, err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := WaitLock(ctx, dir, "test.lock", "test"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitLock error is %v, want a deadline", err)
	}
}

// Held is what a fence's owner check rests on, so it has to be true exactly while a holder is
// alive and false the moment that holder's file descriptors are gone.
func TestHeldFollowsTheHolder(t *testing.T) {
	dir := t.TempDir()
	held, err := Held(dir, "fence.lock")
	if err != nil || held {
		t.Fatalf("Held with no file: %v, %v", held, err)
	}
	unlock, ok, err := TryLock(dir, "fence.lock", "fence")
	if err != nil || !ok {
		t.Fatalf("TryLock: ok %v, err %v", ok, err)
	}
	if held, err := Held(dir, "fence.lock"); err != nil || !held {
		t.Fatalf("Held while locked: %v, %v", held, err)
	}
	unlock()
	if held, err := Held(dir, "fence.lock"); err != nil || held {
		t.Fatalf("Held after the release: %v, %v", held, err)
	}
}

// Held must not take the lock it tests, or two checkers would fence each other out.
func TestHeldLeavesTheLockFree(t *testing.T) {
	dir := t.TempDir()
	f, err := OpenLock(dir, "fence.lock")
	if err != nil {
		t.Fatalf("creating the lock: %v", err)
	}
	f.Close()
	for range 3 {
		if held, err := Held(dir, "fence.lock"); err != nil || held {
			t.Fatalf("Held: %v, %v", held, err)
		}
	}
	unlock, ok, err := TryLock(dir, "fence.lock", "fence")
	if err != nil || !ok {
		t.Fatalf("TryLock after Held: ok %v, err %v", ok, err)
	}
	unlock()
}

func TestOpenLockRefusesASymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "test.lock")); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenLock(dir, "test.lock"); err == nil {
		t.Fatal("OpenLock followed a symlink out of the state directory")
	}
}

func TestOpenLockCreatesItPrivate(t *testing.T) {
	dir := t.TempDir()
	f, err := OpenLock(dir, "test.lock")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fi, err := os.Stat(filepath.Join(dir, "test.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("the lock is mode %v, want nothing for group or other", perm)
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
