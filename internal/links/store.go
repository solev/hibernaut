package links

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/solev/hibernaut/internal/statefile"
)

// store is the one reader and writer of links.json. Everything goes through it rather than
// reading the file, so the lock order, the fence's owner file and the version check live in one
// place.
//
// The lock order is a machine's fence lock, then LockName. Update takes LockName alone, Fence
// takes the fence lock and then updates under LockName, and its release clears the fence under
// LockName before it lets the fence lock go.
type store struct {
	dir string
	// owned is how files in dir are kept the state directory owner's, so a links.json or a
	// lock that root writes first is still one the login user's hibd can take.
	owned statefile.Dir
}

// maxState bounds what Read will parse. A full document, MaxMachines machines with MaxPorts
// ports each, is far smaller.
const maxState = 4 << 20

// releaseTimeout bounds clearing a fence. The release runs when the pause has settled, often
// after its caller's context is done, so it gets its own.
const releaseTimeout = 30 * time.Second

// NewStore returns the production store over stateDir, usually HIBD_STATE_DIR. The document is
// stateDir/links.json and the fence owner files are beside it.
func NewStore(stateDir string) Store {
	return &store{dir: stateDir, owned: statefile.Dir{Path: stateDir}}
}

// Path is the document this store reads and writes.
func (s *store) Path() string { return filepath.Join(s.dir, StateName) }

func (s *store) Read() (Doc, error) {
	root, owner, err := s.owned.Root()
	if errors.Is(err, fs.ErrNotExist) {
		return Doc{V: Version}, nil
	}
	if err != nil {
		return Doc{}, unreadable(err)
	}
	defer root.Close()
	return readDoc(root, owner)
}

func (s *store) Update(ctx context.Context, f func(*Doc) error) error {
	unlock, err := s.owned.WaitLock(ctx, LockName, "link state")
	if err != nil {
		return err
	}
	defer unlock()
	root, owner, err := s.owned.Root()
	if err != nil {
		return err
	}
	defer root.Close()
	doc, err := readDoc(root, owner)
	if err != nil {
		return err
	}
	if err := f(&doc); err != nil {
		return err
	}
	doc.V = Version
	if doc.Machines == nil {
		doc.Machines = map[string]*Machine{}
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	written, err := s.owned.PublishOwned(root, StateName, append(data, '\n'), owner, true)
	if err != nil {
		return fmt.Errorf("writing %s: %w", s.Path(), err)
	}
	return written.Close()
}

// errUnchanged is an Update function's way of saying there is nothing to write.
var errUnchanged = errors.New("unchanged")

func (s *store) Fence(ctx context.Context, id string, why string) (func(), error) {
	if !validID(id) {
		return func() {}, fmt.Errorf("fencing %q: not a machine ID", id)
	}
	owner := fmt.Sprintf(FenceName, id)
	unlock, err := s.owned.WaitLock(ctx, owner, "fence")
	if err != nil {
		return func() {}, err
	}
	err = s.Update(ctx, func(d *Doc) error {
		d.Entry(id).Fence = &Fence{Since: time.Now().UTC(), Why: why, Owner: owner}
		return nil
	})
	if err != nil {
		unlock()
		return func() {}, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			defer unlock()
			ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
			defer cancel()
			_ = s.Update(ctx, func(d *Doc) error {
				m := d.Machines[id]
				if m == nil || m.Fence == nil || m.Fence.Owner != owner {
					return errUnchanged
				}
				m.Fence = nil
				if empty(m) {
					delete(d.Machines, id)
				}
				return nil
			})
		})
	}, nil
}

// empty reports whether nothing is true of m any more, so its entry can go. A port's generation
// is kept after the port is gone, so a machine that ever published keeps its entry.
func empty(m *Machine) bool {
	return !m.Addr.IsValid() && len(m.Ports) == 0 && len(m.PortGen) == 0 && !m.PausedByHand && m.Fence == nil
}

// readDoc reads links.json from root under the owned-file rules: a regular file at the name,
// not a link to one, that the state directory's owner holds and nobody else can write. A file
// that isn't there is the empty document. Anything else that is wrong fails closed.
func readDoc(root *os.Root, owner statefile.Owner) (Doc, error) {
	named, err := root.Lstat(StateName)
	if errors.Is(err, fs.ErrNotExist) {
		return Doc{V: Version}, nil
	}
	if err != nil {
		return Doc{}, unreadable(err)
	}
	if !named.Mode().IsRegular() {
		return Doc{}, unreadable(errors.New("not a regular file"))
	}
	f, err := root.OpenFile(StateName, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return Doc{}, unreadable(err)
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return Doc{}, unreadable(err)
	}
	if !os.SameFile(named, opened) {
		return Doc{}, unreadable(errors.New("changed while it was opened"))
	}
	st, ok := opened.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != owner.UID || opened.Mode().Perm()&0o022 != 0 {
		return Doc{}, unreadable(fmt.Errorf("needs a private file owned by uid %d", owner.UID))
	}
	data, err := io.ReadAll(io.LimitReader(f, maxState+1))
	if err != nil {
		return Doc{}, unreadable(err)
	}
	if len(data) > maxState {
		return Doc{}, unreadable(fmt.Errorf("bigger than %d bytes", maxState))
	}
	return decodeDoc(data)
}

// decodeDoc reads the version first, so that a newer file is reported as newer rather than as
// fields this build doesn't know, and then the document strictly.
func decodeDoc(data []byte) (Doc, error) {
	var probe struct {
		V *int `json:"v"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return Doc{}, unreadable(err)
	}
	switch {
	case probe.V == nil:
		return Doc{}, unreadable(errors.New("no version"))
	case *probe.V > Version:
		return Doc{}, &newerError{v: *probe.V}
	case *probe.V < 1:
		return Doc{}, unreadable(fmt.Errorf("version %d", *probe.V))
	}
	var doc Doc
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return Doc{}, unreadable(err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return Doc{}, unreadable(errors.New("data after the document"))
	}
	if err := checkDoc(&doc); err != nil {
		return Doc{}, unreadable(err)
	}
	return doc, nil
}

// checkDoc refuses a document the readers can't safely act on: an entry that is null, which
// every reader would trip over, an address outside CIDR, which nothing could reach, and two
// machines at one address, which would send one machine's links to the other.
func checkDoc(d *Doc) error {
	prefix := netip.MustParsePrefix(CIDR)
	held := map[netip.Addr]string{}
	for id, m := range d.Machines {
		if m == nil {
			return fmt.Errorf("machine %s has no entry", id)
		}
		if !m.Addr.IsValid() {
			continue
		}
		if !prefix.Contains(m.Addr) {
			return fmt.Errorf("machine %s's address %v is outside %s", id, m.Addr, CIDR)
		}
		if other, ok := held[m.Addr]; ok {
			return fmt.Errorf("machines %s and %s share the address %v", other, id, m.Addr)
		}
		held[m.Addr] = id
	}
	return nil
}

// unreadableError is a links.json that can't be read. It is ErrStateUnreadable, and it says why.
type unreadableError struct{ err error }

func unreadable(err error) error { return &unreadableError{err: err} }

func (e *unreadableError) Error() string        { return StateName + ": " + e.err.Error() }
func (e *unreadableError) Unwrap() error        { return e.err }
func (e *unreadableError) Is(target error) bool { return target == ErrStateUnreadable }

// newerError is a links.json from a newer hibd. It is ErrStateUnreadable, and NewerVersion
// lets a caller that wants to say "upgrade" tell it from a corrupt file without a new export.
type newerError struct{ v int }

func (e *newerError) Error() string {
	return fmt.Sprintf("%s: version %d is newer than this hibd's %d", StateName, e.v, Version)
}
func (e *newerError) Is(target error) bool { return target == ErrStateUnreadable }
func (e *newerError) NewerVersion() int    { return e.v }

// validID reports whether id can name a file in the state directory: one path element that
// doesn't start with a dot, so it can never be a temporary file or reach outside.
func validID(id string) bool {
	return id != "" && !strings.HasPrefix(id, ".") && !strings.ContainsAny(id, "/\x00")
}

// clock is the production activity clock. busy.Config.Activity feeds it, the SSH bridge
// heartbeats into it while a session is open, and a successful create or fork touches it once.
// It is kept under stateDir so a restart of hibd does not reset everyone's authority.
//
// Each machine is one empty file under ActivityDir whose modification time is the whole value.
type clock struct {
	dir   string
	owned statefile.Dir
}

// NewClock returns the production activity clock over stateDir, usually HIBD_STATE_DIR.
func NewClock(stateDir string) Clock {
	return &clock{dir: stateDir, owned: statefile.Dir{Path: stateDir}}
}

// Path is the directory the clock keeps its files in.
func (c *clock) Path() string { return filepath.Join(c.dir, ActivityDir) }

func (c *clock) Active(id string) time.Time {
	if !validID(id) {
		return time.Time{}
	}
	root, err := os.OpenRoot(c.dir)
	if err != nil {
		return time.Time{}
	}
	defer root.Close()
	fi, err := root.Lstat(ActivityDir + "/" + id)
	if err != nil || !fi.Mode().IsRegular() {
		return time.Time{}
	}
	return fi.ModTime()
}

// Touch is best effort: a failure costs the machine some authority over others, which is the
// safe direction, and it can't be allowed to stop the busy watcher or a session.
func (c *clock) Touch(id string, t time.Time) {
	if !validID(id) || t.IsZero() {
		return
	}
	// Times are kept to the microsecond, which is what futimes sets. Truncating first keeps
	// the comparison and the stored value the same thing.
	t = t.Truncate(time.Microsecond)
	root, owner, err := c.owned.Root()
	if err != nil {
		return
	}
	defer root.Close()
	for _, dir := range []string{filepath.Dir(ActivityDir), ActivityDir} {
		if err := c.owned.EnsureDirectory(root, owner, dir); err != nil {
			return
		}
	}
	activity, err := root.OpenRoot(ActivityDir)
	if err != nil {
		return
	}
	defer activity.Close()
	_, err = activity.Lstat(id)
	if errors.Is(err, fs.ErrNotExist) {
		f, err := c.owned.PublishPrepared(activity, id, owner, false, func(f *os.File) error {
			return setModTime(f, t)
		})
		if err == nil {
			f.Close()
			return
		}
		if !errors.Is(err, fs.ErrExist) {
			return
		}
	}
	touchExisting(activity, id, t)
}

// touchExisting moves id's time forward to t. The lock is on the file itself and is held only
// for the fstat and the futimes, so touches of different machines never wait on each other.
func touchExisting(activity *os.Root, id string, t time.Time) {
	named, err := activity.Lstat(id)
	if err != nil || !named.Mode().IsRegular() {
		return
	}
	f, err := activity.OpenFile(id, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	fi, err := f.Stat()
	if err != nil || !os.SameFile(named, fi) {
		return
	}
	if t.After(fi.ModTime()) {
		_ = setModTime(f, t)
	}
}

func setModTime(f *os.File, t time.Time) error {
	tv := syscall.NsecToTimeval(t.UnixNano())
	return syscall.Futimes(int(f.Fd()), []syscall.Timeval{tv, tv})
}

var (
	_ Store = (*store)(nil)
	_ Clock = (*clock)(nil)
)
