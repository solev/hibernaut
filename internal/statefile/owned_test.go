package statefile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// asRoot is a Dir that takes root's branches while running as the test's user, recording what
// it gives away. The chown is to the test's own user, so it succeeds.
func asRoot(t *testing.T, dir string) (Dir, *[]string) {
	t.Helper()
	var given []string
	return Dir{
		Path: dir,
		EUID: func() int { return 0 },
		Chown: func(f *os.File, uid, gid int) error {
			if uid != os.Geteuid() || gid != os.Getegid() {
				t.Errorf("chown %s to %d:%d, want the directory owner %d:%d", f.Name(), uid, gid, os.Geteuid(), os.Getegid())
			}
			given = append(given, filepath.Base(f.Name()))
			return f.Chown(uid, gid)
		},
	}, &given
}

func TestRootRefusesADirectoryOthersCanWrite(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, _, err := (Dir{Path: dir}).Root(); err == nil {
		t.Fatal("Root opened a directory anyone can write to")
	}
}

func TestRootRefusesAnotherUsersDirectory(t *testing.T) {
	dir := t.TempDir()
	d := Dir{Path: dir, EUID: func() int { return os.Geteuid() + 1 }}
	if _, _, err := d.Root(); err == nil {
		t.Fatal("Root opened a directory another user owns")
	}
}

func TestPublishOwnedReplacesOnlyWhenAsked(t *testing.T) {
	dir := t.TempDir()
	d, given := asRoot(t, dir)
	root, owner, err := d.Root()
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, data := range []string{"one", "two"} {
		f, err := d.PublishOwned(root, "state.json", []byte(data), owner, true)
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	if _, err := d.PublishOwned(root, "state.json", []byte("three"), owner, false); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("publishing over a file without replace: %v, want it to exist", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil || string(got) != "two" {
		t.Fatalf("state.json = %q, %v; want the last replacement", got, err)
	}
	fi, err := os.Stat(filepath.Join(dir, "state.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("state.json mode: %v, %v", fi, err)
	}
	if len(*given) != 3 {
		t.Fatalf("chowned %v, want every temporary file given to the owner", *given)
	}
	assertNoTemporaries(t, dir)
}

func TestEnsureDirectoryMakesItPrivateAndOwned(t *testing.T) {
	dir := t.TempDir()
	d, given := asRoot(t, dir)
	root, owner, err := d.Root()
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for range 2 {
		if err := d.EnsureDirectory(root, owner, "links"); err != nil {
			t.Fatal(err)
		}
	}
	fi, err := os.Stat(filepath.Join(dir, "links"))
	if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("links: %v, %v", fi, err)
	}
	if len(*given) != 1 || (*given)[0] != "links" {
		t.Fatalf("chowned %v, want only the new directory", *given)
	}
	if err := d.EnsureDirectory(root, Owner{UID: owner.UID + 1}, "links"); err == nil {
		t.Fatal("EnsureDirectory accepted a directory that isn't the state owner's")
	}
}

func TestEnsureDirectoryRefusesAFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "links"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	d := Dir{Path: dir}
	root, owner, err := d.Root()
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := d.EnsureDirectory(root, owner, "links"); err == nil {
		t.Fatal("EnsureDirectory accepted a file")
	}
}

// Root's first lock has to be the login user's, or the user's hibd could never take it again.
func TestOpenLockAsRootGivesTheLockToTheOwner(t *testing.T) {
	dir := t.TempDir()
	d, given := asRoot(t, dir)
	unlock, ok, err := d.TryLock("links.lock", "link state")
	if err != nil || !ok {
		t.Fatalf("TryLock as root: ok %v, err %v", ok, err)
	}
	unlock()
	if len(*given) != 1 {
		t.Fatalf("chowned %v, want the new lock given to the owner", *given)
	}
	assertNoTemporaries(t, dir)
	user := Dir{Path: dir}
	unlock, ok, err = user.TryLock("links.lock", "link state")
	if err != nil || !ok {
		t.Fatalf("TryLock as the user after root: ok %v, err %v", ok, err)
	}
	if _, ok, err := d.TryLock("links.lock", "link state"); err != nil || ok {
		t.Fatalf("root took a lock the user held: ok %v, err %v", ok, err)
	}
	unlock()
	if len(*given) != 1 {
		t.Fatalf("chowned %v, want an existing lock left alone", *given)
	}
}

func TestOpenLockRefusesALockOthersCanWrite(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "links.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "links.lock"), 0o622); err != nil {
		t.Fatal(err)
	}
	for _, d := range []Dir{{Path: dir}, {Path: dir, EUID: func() int { return 0 }}} {
		if _, err := d.OpenLock("links.lock"); err == nil {
			t.Fatalf("OpenLock (euid %d) took a lock others can write to", d.euid())
		}
	}
}

func TestOpenLockRefusesADirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "links.lock"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := (Dir{Path: dir}).OpenLock("links.lock"); err == nil {
		t.Fatal("OpenLock took a directory")
	}
}

func TestDirWaitLockQueues(t *testing.T) {
	d := Dir{Path: t.TempDir()}
	unlock, ok, err := d.TryLock("links.lock", "link state")
	if err != nil || !ok {
		t.Fatalf("TryLock: ok %v, err %v", ok, err)
	}
	if held, err := Held(d.Path, "links.lock"); err != nil || !held {
		t.Fatalf("Held on the owned lock: %v, %v", held, err)
	}
	done := make(chan error, 1)
	go func() {
		release, err := d.WaitLock(t.Context(), "links.lock", "link state")
		if release != nil {
			release()
		}
		done <- err
	}()
	unlock()
	if err := <-done; err != nil {
		t.Fatalf("WaitLock: %v", err)
	}
}

func assertNoTemporaries(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name()[0] == '.' {
			t.Errorf("left a temporary file %s", e.Name())
		}
	}
}
