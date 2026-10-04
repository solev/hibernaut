package atomicfile

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
)

const secret = "tok-3f9c1a-SECRET"

var (
	content = []byte("{\"token\": \"" + secret + "\"}\n")
	old     = []byte("the old content, which is longer than the new content\n")
)

var writers = []struct {
	name  string
	write func(string, []byte, fs.FileMode) error
}{
	{"WriteFile", WriteFile},
	{"WriteNewFile", WriteNewFile},
}

func TestWriteMakesFileWithContentAndExactMode(t *testing.T) {

	for _, perm := range []fs.FileMode{0o600, 0o644, 0o640, 0o400, 0o666} {
		for _, w := range writers {
			t.Run(w.name+"/"+perm.String(), func(t *testing.T) {
				dir := t.TempDir()
				path := filepath.Join(dir, "auth.json")
				if err := w.write(path, content, perm); err != nil {
					t.Fatal(err)
				}
				assertFile(t, path, content, perm)
				assertOnly(t, dir, "auth.json")
			})
		}
	}
}

func TestWriteFileReplacesExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profile.json")
	if err := os.WriteFile(path, old, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	assertFile(t, path, content, 0o644)
	assertOnly(t, dir, "profile.json")
}

func TestWriteFileReplacesSymlinkRatherThanWritingThroughIt(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, old, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(link, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&fs.ModeSymlink != 0 {
		t.Errorf("link mode = %v, %v; want a regular file", fi.Mode(), err)
	}
	assertFile(t, link, content, 0o600)
	assertFile(t, target, old, 0o600)
	assertOnly(t, dir, "link", "target")
}

func TestWriteNewFileRefusesExistingPath(t *testing.T) {
	for name, setup := range map[string]func(path string) error{
		"file":             func(path string) error { return os.WriteFile(path, old, 0o600) },
		"dangling symlink": func(path string) error { return os.Symlink(path+".target", path) },
		"directory":        func(path string) error { return os.Mkdir(path, 0o700) },
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "ca-key.pem")
			if err := setup(path); err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			err = WriteNewFile(path, content, 0o600)
			if !errors.Is(err, fs.ErrExist) {
				t.Fatalf("WriteNewFile = %v, want fs.ErrExist", err)
			}
			assertNoSecret(t, err)
			if after, err := os.Lstat(path); err != nil || !os.SameFile(before, after) || after.Size() != before.Size() {
				t.Errorf("WriteNewFile changed the existing %s: %v", name, err)
			}
			assertOnly(t, dir, "ca-key.pem")
		})
	}
}

func TestWriteFailsInMissingDirectory(t *testing.T) {
	for _, w := range writers {
		t.Run(w.name, func(t *testing.T) {
			parent := t.TempDir()
			err := w.write(filepath.Join(parent, "missing", "auth.json"), content, 0o600)
			if !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("write = %v, want fs.ErrNotExist", err)
			}
			assertNoSecret(t, err)
			assertOnly(t, parent)
		})
	}
}

func TestWriteFileCleansUpWhenRenameFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.MkdirAll(filepath.Join(path, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	err := WriteFile(path, content, 0o600)
	if err == nil {
		t.Fatal("WriteFile over a directory succeeded")
	}
	assertNoSecret(t, err)
	assertOnly(t, dir, "config.json")
}

func TestWriteCleansUpWhenAFileStepFails(t *testing.T) {
	for _, step := range []string{"chmod", "write", "sync", "close"} {
		for _, w := range writers {
			t.Run(w.name+"/"+step, func(t *testing.T) {
				dir := t.TempDir()
				path := filepath.Join(dir, "auth.json")
				failing := &fs.PathError{Op: step, Path: "temp", Err: syscall.EIO}
				wrapTemp(t, func(f file) file { return faultyFile{f, step, failing} })
				if w.name == "WriteFile" {
					if err := os.WriteFile(path, old, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				err := w.write(path, content, 0o600)
				if !errors.Is(err, syscall.EIO) {
					t.Fatalf("write = %v, want the %s failure", err, step)
				}
				assertNoSecret(t, err)
				if w.name == "WriteFile" {
					assertFile(t, path, old, 0o600)
					assertOnly(t, dir, "auth.json")
				} else {
					assertOnly(t, dir)
				}
			})
		}
	}
}

func TestWriteSetsModeBeforeDataAndSyncsBeforePublishing(t *testing.T) {
	for _, w := range writers {
		t.Run(w.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "auth.json")
			spy := &fileSpy{path: path}
			wrapTemp(t, func(f file) file { spy.file = f; return spy })
			if err := w.write(path, content, 0o640); err != nil {
				t.Fatal(err)
			}
			if filepath.Dir(spy.Name()) != dir || !strings.HasPrefix(filepath.Base(spy.Name()), ".auth.json.") {
				t.Errorf("temp file = %s, want a hidden file named after auth.json in %s", spy.Name(), dir)
			}
			if spy.modeAtWrite != 0o640 {
				t.Errorf("temp file mode when written = %v, want 0640", spy.modeAtWrite)
			}
			if !spy.synced || spy.publishedAtSync {
				t.Errorf("synced = %v, published before sync = %v; want a sync before publishing", spy.synced, spy.publishedAtSync)
			}
		})
	}
}

func TestWriteSyncsDirectoryAfterPublishing(t *testing.T) {
	for _, w := range writers {
		t.Run(w.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "auth.json")
			var synced []string
			real := syncDirectory
			t.Cleanup(func() { syncDirectory = real })
			syncDirectory = func(d *os.File) error {
				if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, content) {
					t.Errorf("directory synced before the file was published: %v", err)
				}
				synced = append(synced, d.Name())

				err := real(d)
				if err != nil {
					t.Errorf("syncing the directory: %v", err)
				}
				return err
			}
			if err := w.write(path, content, 0o600); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(synced, []string{dir}) {
				t.Errorf("synced directories %v, want [%s]", synced, dir)
			}
		})
	}
}

func TestWriteIgnoresDirectorySyncTheFilesystemLacks(t *testing.T) {
	for _, errno := range []syscall.Errno{syscall.EINVAL, syscall.ENOTSUP} {
		for _, w := range writers {
			t.Run(w.name+"/"+errno.Error(), func(t *testing.T) {
				dir := t.TempDir()
				path := filepath.Join(dir, "auth.json")
				failDirectorySync(t, errno)
				if err := w.write(path, content, 0o600); err != nil {
					t.Fatalf("write = %v, want nil", err)
				}
				assertFile(t, path, content, 0o600)
				assertOnly(t, dir, "auth.json")
			})
		}
	}
}

func TestWriteReportsFailedDirectorySync(t *testing.T) {
	for _, w := range writers {
		t.Run(w.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "auth.json")
			failDirectorySync(t, syscall.EIO)
			err := w.write(path, content, 0o600)
			if !errors.Is(err, syscall.EIO) {
				t.Fatalf("write = %v, want EIO", err)
			}
			assertNoSecret(t, err)
			assertFile(t, path, content, 0o600)
			assertOnly(t, dir, "auth.json")
		})
	}
}

func TestWriteReportsUnreadableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any directory")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if err := os.Chmod(dir, 0o300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	err := WriteFile(path, content, 0o600)
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("WriteFile = %v, want fs.ErrPermission from opening the directory to sync it", err)
	}
	assertNoSecret(t, err)
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	assertFile(t, path, content, 0o600)
	assertOnly(t, dir, "auth.json")
}

func TestWriteNewFileReportsTempFileItCannotRemove(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ca-key.pem")
	real := remove
	t.Cleanup(func() { remove = real })
	calls := 0
	remove = func(name string) error {
		calls++
		if calls == 1 {
			return &fs.PathError{Op: "remove", Path: name, Err: syscall.EIO}
		}
		return real(name)
	}
	err := WriteNewFile(path, content, 0o600)
	if !errors.Is(err, syscall.EIO) {
		t.Fatalf("WriteNewFile = %v, want EIO", err)
	}
	assertNoSecret(t, err)
	assertFile(t, path, content, 0o600)
	assertOnly(t, dir, "ca-key.pem")
}

func TestConcurrentWritersNeverMix(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.json")
	versions := make([][]byte, 4)
	for i := range versions {
		versions[i] = bytes.Repeat([]byte{byte('a' + i)}, 64<<10)
	}
	if err := WriteFile(path, versions[0], 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	read := make(chan error, 1)
	go func() {
		for {
			got, err := os.ReadFile(path)
			if err != nil || !slices.ContainsFunc(versions, func(v []byte) bool { return bytes.Equal(v, got) }) {
				read <- errors.New("a reader saw a missing or mixed file")
				return
			}
			select {
			case <-done:
				read <- nil
				return
			default:
			}
		}
	}()
	var wg sync.WaitGroup
	for _, v := range versions {
		wg.Go(func() {
			for range 3 {
				if err := WriteFile(path, v, 0o644); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	close(done)
	if err := <-read; err != nil {
		t.Fatal(err)
	}
	assertOnly(t, dir, "vault.json")
}

func wrapTemp(t *testing.T, wrap func(file) file) {
	real := createTemp
	t.Cleanup(func() { createTemp = real })
	createTemp = func(dir, pattern string) (file, error) {
		f, err := real(dir, pattern)
		if err != nil {
			return nil, err
		}
		return wrap(f), nil
	}
}

func failDirectorySync(t *testing.T, errno syscall.Errno) {
	real := syncDirectory
	t.Cleanup(func() { syncDirectory = real })
	syncDirectory = func(d *os.File) error {
		return &fs.PathError{Op: "sync", Path: d.Name(), Err: errno}
	}
}

type faultyFile struct {
	file
	step string
	err  error
}

func (f faultyFile) Chmod(mode fs.FileMode) error {
	if f.step == "chmod" {
		return f.err
	}
	return f.file.Chmod(mode)
}

func (f faultyFile) Write(b []byte) (int, error) {
	if f.step == "write" {
		n, _ := f.file.Write(b[:len(b)/2])
		return n, f.err
	}
	return f.file.Write(b)
}

func (f faultyFile) Sync() error {
	if f.step == "sync" {
		return f.err
	}
	return f.file.Sync()
}

func (f faultyFile) Close() error {
	err := f.file.Close()
	if f.step == "close" {
		return f.err
	}
	return err
}

type fileSpy struct {
	file
	path            string
	modeAtWrite     fs.FileMode
	synced          bool
	publishedAtSync bool
}

func (s *fileSpy) Write(b []byte) (int, error) {
	if fi, err := os.Stat(s.Name()); err == nil {
		s.modeAtWrite = fi.Mode().Perm()
	}
	return s.file.Write(b)
}

func (s *fileSpy) Sync() error {
	s.synced = true
	if got, err := os.ReadFile(s.path); err == nil && bytes.Equal(got, content) {
		s.publishedAtSync = true
	}
	return s.file.Sync()
}

func assertFile(t *testing.T, path string, want []byte, perm fs.FileMode) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s holds %d bytes, not the %d expected", filepath.Base(path), len(got), len(want))
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != perm {
		t.Errorf("%s mode = %v, want %v", filepath.Base(path), fi.Mode().Perm(), perm)
	}
}

func assertOnly(t *testing.T, dir string, names ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if !slices.Equal(got, names) {
		t.Errorf("%s holds %v, want %v", dir, got, names)
	}
}

func assertNoSecret(t *testing.T, err error) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), secret) {
		t.Error("an error contains the file's content")
	}
}
