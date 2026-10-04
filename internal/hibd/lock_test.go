package hibd

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/solev/hibernaut/internal/engine/fake"
)

type loginLock struct {
	name, file, what string
	lock             func(stateDir string) (unlock func(), ok bool, err error)
}

var loginLocks = []loginLock{
	{"codex", "codex-login.lock", "codex login", lockCodexLogins},
	{"github", "github-login.lock", "GitHub login", lockGitHubLogins},
}

func TestLoginLocks(t *testing.T) {
	for _, l := range loginLocks {
		t.Run(l.name, func(t *testing.T) {
			dir := t.TempDir()
			unlock, ok, err := l.lock(dir)
			if err != nil || !ok {
				t.Fatalf("taking the lock = %v, %v; want it taken", ok, err)
			}
			if fi, err := os.Lstat(filepath.Join(dir, l.file)); err != nil {
				t.Error(err)
			} else if !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
				t.Errorf("lock file mode = %v, want a regular file with mode 0600", fi.Mode())
			}
			if again, ok, err := l.lock(dir); err != nil || ok || again != nil {
				t.Errorf("taking the lock while it's held = %v, %v; want it busy", ok, err)
			}
			unlock()
			unlock, ok, err = l.lock(dir)
			if err != nil || !ok {
				t.Fatalf("taking the lock after unlocking = %v, %v; want it taken", ok, err)
			}
			unlock()
		})
	}
}

func TestLoginLocksRefuseSymlinks(t *testing.T) {
	for _, l := range loginLocks {
		for _, target := range []struct {
			name           string
			inside, exists bool
		}{
			{"to a file outside", false, true},
			{"to a missing file outside", false, false},
			{"to a file inside", true, true},
			{"to a missing file inside", true, false},
		} {
			t.Run(l.name+" "+target.name, func(t *testing.T) {
				dir := t.TempDir()
				path, linked := plantLockLink(t, dir, l.file, target.inside, target.exists)
				unlock, ok, err := l.lock(dir)
				if unlock != nil {
					unlock()
				}
				if want := "opening the " + l.what + " lock: " + path + " isn't a regular file"; err == nil || err.Error() != want || ok {
					t.Errorf("taking the lock = %v, %v; want %q", ok, err, want)
				}
				wantLockLinkUntouched(t, path, linked, target.exists)
			})
		}
	}
}

func plantLockLink(t *testing.T, dir, file string, inside, exists bool) (path, linked string) {
	t.Helper()
	path, linked = filepath.Join(dir, file), filepath.Join(t.TempDir(), "target")
	to := linked
	if inside {
		linked, to = filepath.Join(dir, "target"), "target"
	}
	if exists {
		if err := os.WriteFile(linked, []byte("keep\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(to, path); err != nil {
		t.Fatal(err)
	}
	return path, linked
}

func wantLockLinkUntouched(t *testing.T, path, linked string, exists bool) {
	t.Helper()
	if fi, err := os.Lstat(path); err != nil {
		t.Error(err)
	} else if fi.Mode().Type() != fs.ModeSymlink {
		t.Errorf("the link at %s became a %v, want it left alone", path, fi.Mode())
	}
	fi, err := os.Lstat(linked)
	switch {
	case !exists && !errors.Is(err, fs.ErrNotExist):
		t.Errorf("the link's target exists (%v), want it never made", err)
	case !exists:
	case err != nil:
		t.Error(err)
	case fi.Mode() != 0o644:
		t.Errorf("the link's target has mode %v, want -rw-r--r--", fi.Mode())
	default:
		if data, err := os.ReadFile(linked); err != nil || string(data) != "keep\n" {
			t.Errorf("the link's target holds %q, %v; want %q", data, err, "keep\n")
		}
	}
}

func TestConnectRefusesALinkedLock(t *testing.T) {
	t.Run("github", func(t *testing.T) {
		a, out, errOut, gh := newGitHubApp(t, fake.New())
		gh.set(testToken, octocat)
		d := newFakeDevice(t, a, deviceAnswer{body: codeBody}, deviceAnswer{body: tokenBody})
		a.Stdin = unread{t}
		path, linked := plantLockLink(t, a.Getenv("HIBD_STATE_DIR"), "github-login.lock", false, false)
		code, r := runJSONReply(t, a, out, "connect", "github", "-source", "device", "-json")
		if want := "opening the GitHub login lock: " + path + " isn't a regular file"; code != 1 || r.Error != want {
			t.Errorf("exit %d, error %q; want 1 and %q", code, r.Error, want)
		}
		if errOut.Len() != 0 {
			t.Errorf("stderr = %q, want nothing", errOut)
		}
		if codes, polls := d.requests(); codes+polls != 0 {
			t.Errorf("fake GitHub got %d requests, want none", codes+polls)
		}
		wantNothingStored(t, a, gh)
		wantLockLinkUntouched(t, path, linked, false)
	})
	t.Run("codex", func(t *testing.T) {
		a, out, _, f := newCodexApp(t, nil, testCodexTokens())
		path, linked := plantLockLink(t, a.Getenv("HIBD_STATE_DIR"), "codex-login.lock", false, false)
		code, r := runJSONReply(t, a, out, "connect", "codex", "-json")
		if want := "opening the codex login lock: " + path + " isn't a regular file"; code != 1 || r.Error != want {
			t.Errorf("exit %d, error %q; want 1 and %q", code, r.Error, want)
		}
		if calls := f.calls(t); len(calls) != 0 {
			t.Errorf("codex ran %d times, want never", len(calls))
		}
		wantLockLinkUntouched(t, path, linked, false)
	})
}

func TestConnectCodexWhileAnotherIsRunning(t *testing.T) {
	a, out, _, f := newCodexApp(t, nil, testCodexTokens())
	unlock, ok, err := lockCodexLogins(a.Getenv("HIBD_STATE_DIR"))
	if err != nil || !ok {
		t.Fatalf("taking the codex login lock: %v, %v", ok, err)
	}
	defer unlock()
	code, r := runJSONReply(t, a, out, "connect", "codex", "-json")
	if want := "another hib connect codex is running: finish it, or stop it, then try again"; code != 1 || r.Error != want {
		t.Errorf("exit %d, error %q; want 1 and %q", code, r.Error, want)
	}
	if calls := f.calls(t); len(calls) != 0 {
		t.Errorf("codex ran %d times, want never", len(calls))
	}
	if logins := codexLogins(t, a); len(logins) != 0 {
		t.Errorf("login directories %v, want none", logins)
	}
}
