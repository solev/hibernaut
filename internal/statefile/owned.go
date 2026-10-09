package statefile

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// Owner is who a state directory belongs to. Every file hibd publishes in it is given this owner,
// so a file root writes is still the login user's.
type Owner struct {
	UID, GID int
}

// Dir is a state directory, with what it takes to keep the files in it owned by the directory's
// owner. The bodies came from internal/machines (stateRoot, publishOwned, ensureDirectory and
// openLock) unchanged, except that OpenLock takes the lock's name, so that links can write an
// owned file and take an owned lock the way machines does.
type Dir struct {
	Path string
	// EUID returns the effective user ID. If it's nil, Dir uses os.Geteuid.
	EUID func() int
	// Chown gives a file to uid and gid. If it's nil, Dir uses (*os.File).Chown.
	Chown func(f *os.File, uid, gid int) error
}

func (d Dir) euid() int {
	if d.EUID == nil {
		return os.Geteuid()
	}
	return d.EUID()
}

func (d Dir) chown(f *os.File, uid, gid int) error {
	if d.Chown == nil {
		return f.Chown(uid, gid)
	}
	return d.Chown(f, uid, gid)
}

// DirectoryOwner reads a state directory's owner from fi. It refuses anything that isn't a
// directory, a directory others can write to and, unless uid is root's, one that uid doesn't own.
func DirectoryOwner(fi fs.FileInfo, name string, uid int) (Owner, error) {
	if !fi.IsDir() {
		return Owner{}, fmt.Errorf("state directory %s: not a directory", name)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return Owner{}, fmt.Errorf("state directory %s: can't tell its owner", name)
	}
	if uid != 0 && int(st.Uid) != uid {
		return Owner{}, fmt.Errorf("state directory %s: belongs to uid %d, not %d", name, st.Uid, uid)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return Owner{}, fmt.Errorf("state directory %s: others can write to it (%v)", name, fi.Mode().Perm())
	}
	return Owner{UID: int(st.Uid), GID: int(st.Gid)}, nil
}

func checkDir(dir string, uid int) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("state directory: %w", err)
	}
	_, err = DirectoryOwner(fi, dir, uid)
	return err
}

// Root opens the state directory and reads its owner.
func (d Dir) Root() (*os.Root, Owner, error) {
	if err := checkDir(d.Path, d.euid()); err != nil {
		return nil, Owner{}, err
	}
	root, err := os.OpenRoot(d.Path)
	if err != nil {
		return nil, Owner{}, err
	}
	fi, err := root.Stat(".")
	if err != nil {
		root.Close()
		return nil, Owner{}, err
	}
	owner, err := DirectoryOwner(fi, d.Path, d.euid())
	if err != nil {
		root.Close()
		return nil, Owner{}, err
	}
	return root, owner, nil
}

// PublishOwned writes data to name in root as a private file that owner owns. It writes a
// temporary file first and then renames it into place when replace is set, or links it, which
// never replaces a name that exists, when it isn't. It returns the published file, still open.
func (d Dir) PublishOwned(root *os.Root, name string, data []byte, owner Owner, replace bool) (published *os.File, err error) {
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return nil, err
	}
	tmp := fmt.Sprintf(".%s.tmp-%x", name, token)
	f, err := root.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	defer func() {
		if published == nil {
			f.Close()
		}
		root.Remove(tmp)
	}()
	if err := f.Chmod(0o600); err != nil {
		return nil, err
	}
	if err := d.chown(f, owner.UID, owner.GID); err != nil {
		return nil, err
	}
	if _, err := f.Write(data); err != nil {
		return nil, err
	}
	if err := f.Sync(); err != nil {
		return nil, err
	}
	if replace {
		err = root.Rename(tmp, name)
	} else {
		err = root.Link(tmp, name)
	}
	if err != nil {
		return nil, err
	}
	if !replace {
		if err := root.Remove(tmp); err != nil {
			return nil, err
		}
	}
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) && !errors.Is(err, errors.ErrUnsupported) {
		return nil, err
	}
	return f, nil
}

// EnsureDirectory makes the directory name in root if it isn't there, private and owned by
// owner, and checks that one that is there belongs to owner.
func (d Dir) EnsureDirectory(root *os.Root, owner Owner, name string) error {
	fi, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		if d.euid() == 0 {
			err = d.makeOwnedDirectory(root, owner, name)
		} else {
			err = root.Mkdir(name, 0o700)
		}
		if err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		fi, err = root.Lstat(name)
	}
	if err != nil {
		return err
	}
	dirOwner, err := DirectoryOwner(fi, root.Name()+"/"+name, d.euid())
	if err != nil {
		return err
	}
	if dirOwner.UID != owner.UID {
		return fmt.Errorf("state directory %s/%s: belongs to uid %d, not the state owner %d", root.Name(), name, dirOwner.UID, owner.UID)
	}
	return nil
}

func (d Dir) makeOwnedDirectory(root *os.Root, owner Owner, name string) error {
	if err := root.Mkdir(name, 0o700); err != nil {
		return err
	}
	dir, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := d.chown(dir, owner.UID, owner.GID); err != nil {
		root.Remove(name)
		return err
	}
	return nil
}

// OpenLock opens the lock file name in the state directory, creating it if it isn't there.
// Root creates it through PublishOwned, so a lock root made first still belongs to the state
// directory's owner and the login user can take it afterwards. Either way the lock has to be a
// regular private file the owner holds.
func (d Dir) OpenLock(name string) (*os.File, error) {
	root, owner, err := d.Root()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	var f *os.File
	if d.euid() == 0 {
		f, err = root.OpenFile(name, os.O_RDWR|syscall.O_NOFOLLOW, 0)
		if errors.Is(err, fs.ErrNotExist) {
			f, err = d.PublishOwned(root, name, nil, owner, false)
			if errors.Is(err, fs.ErrExist) {
				f, err = root.OpenFile(name, os.O_RDWR|syscall.O_NOFOLLOW, 0)
			}
		}
	} else {
		f, err = root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
		if errors.Is(err, fs.ErrExist) {
			f, err = root.OpenFile(name, os.O_RDWR|syscall.O_NOFOLLOW, 0)
		}
	}
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || !fi.Mode().IsRegular() || int(st.Uid) != owner.UID || fi.Mode().Perm()&0o022 != 0 {
		f.Close()
		return nil, fmt.Errorf("state lock %s/%s: needs a regular private file owned by uid %d", d.Path, name, owner.UID)
	}
	return f, nil
}

// TryLock is the package's TryLock over OpenLock's owned lock.
func (d Dir) TryLock(name, what string) (unlock func(), ok bool, err error) {
	return tryLock(func() (*os.File, error) { return d.OpenLock(name) }, what)
}

// WaitLock is the package's WaitLock over OpenLock's owned lock.
func (d Dir) WaitLock(ctx context.Context, name, what string) (func(), error) {
	return waitLock(ctx, func() (func(), bool, error) { return d.TryLock(name, what) })
}
