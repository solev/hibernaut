// Package machines is what hibd does with machines: it makes them from images, finds them by
// name, and connects to their SSH servers. It keeps what the engine doesn't, in a state
// directory: the lock that keeps names unique, and each machine's pinned SSH host key.
package machines

import (
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/room"
)

const (
	// DefaultIdle is how long a machine keeps running once it's idle, before it pauses, when
	// it isn't given another idle time.
	DefaultIdle = 15 * time.Minute
	// MaxRun is the longest E2B runs a machine before pausing it, however often it's renewed
	// (install-e2b.sh sets it). Machines that never pause when idle run for it at a time.
	MaxRun = 720 * time.Hour
)

const (
	labelName  = "hib.name"
	labelImage = "hib.image"
	labelIdle  = "hib.idle"

	maxKeysFile    = 64 << 10
	initTimeout    = 2 * time.Minute
	cleanupTimeout = time.Minute
)

var (
	// ErrNoMachine is returned for a name no machine has.
	ErrNoMachine = errors.New("no machine")
	// ErrExists is returned when creating a machine whose name is taken.
	ErrExists = errors.New("there is already a machine")
)

var idRE = regexp.MustCompile(`^[a-z0-9]{1,64}$`)

// Config configures a Manager.
type Config struct {
	Engine engine.Engine
	// StateDir is where the lock and pinned host keys are kept. It must exist, belong to the
	// user hibd runs as, and be writable by no one else. Only Create, Remove and Dial use it.
	StateDir string
	// AuthorizedKeys is the authorized_keys file whose keys may log in to new machines: the
	// host's own. Only keys without options are copied: options restrict a key on the host,
	// and don't mean the same in a machine.
	AuthorizedKeys string
	// Now tells the time. Default time.Now.
	Now func() time.Time
	// ReadRoom reads the room on the host, which Create and Dial check before they start or
	// resume a machine. Default room.Read.
	ReadRoom func() (room.Host, error)
	// Setup is written into each new machine after machine-init. An empty Setup writes nothing.
	Setup Setup
}

// Manager manages machines. Its methods may be called concurrently, also from other
// processes sharing its state directory.
type Manager struct {
	e        engine.Engine
	dir      string
	keysFile string
	now      func() time.Time
	readRoom func() (room.Host, error)
	setup    Setup
}

// New returns a Manager. It doesn't touch the state directory until a method needs it.
func New(cfg Config) *Manager {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	readRoom := cfg.ReadRoom
	if readRoom == nil {
		readRoom = room.Read
	}
	return &Manager{e: cfg.Engine, dir: cfg.StateDir, keysFile: cfg.AuthorizedKeys, now: now, readRoom: readRoom, setup: cfg.Setup}
}

// Name returns the name of machine em, or "" if hibd didn't make it.
func Name(em engine.Machine) string {
	if name := em.Metadata[labelName]; hibapi.CheckName(name) == nil {
		return name
	}
	return ""
}

// Idle returns how long machine em keeps running once it's idle before it pauses, 0 for never,
// and whether hibd made it. Machines made before hibd kept idle times get DefaultIdle.
func Idle(em engine.Machine) (idle time.Duration, ok bool) {
	if Name(em) == "" {
		return 0, false
	}
	idle, err := time.ParseDuration(em.Metadata[labelIdle])
	if err != nil || idle < 0 || idle > MaxRun {
		return DefaultIdle, true
	}
	return idle, true
}

// timeoutFor returns the engine timeout that pauses a machine idle from now.
func timeoutFor(idle time.Duration) time.Duration {
	if idle == 0 {
		return MaxRun
	}
	return idle
}

// Create makes a machine called name from image, lets in the keys from Config.AuthorizedKeys,
// and pins its host key. The machine pauses once it has been idle for idle; 0 means never. A
// machine that can't be set up is killed again.
func (m *Manager) Create(ctx context.Context, name, image string, idle time.Duration) (hibapi.Machine, error) {
	if err := hibapi.CheckName(name); err != nil {
		return hibapi.Machine{}, err
	}
	memoryMB, err := images.MemoryMB(image)
	if err != nil {
		return hibapi.Machine{}, err
	}
	if idle < 0 || idle > MaxRun {
		return hibapi.Machine{}, fmt.Errorf("bad idle time %v: want 0 (never pause) up to %v", idle, MaxRun)
	}
	if err := m.checkState(); err != nil {
		return hibapi.Machine{}, err
	}
	keys, err := m.authorizedKeys()
	if err != nil {
		return hibapi.Machine{}, err
	}

	unlock, err := m.lock(ctx)
	if err != nil {
		return hibapi.Machine{}, err
	}
	defer unlock()
	switch _, err := m.lookup(ctx, name); {
	case err == nil:
		return hibapi.Machine{}, fmt.Errorf("%w called %q", ErrExists, name)
	case !errors.Is(err, ErrNoMachine):
		return hibapi.Machine{}, err
	}
	if err := m.checkRoom(ctx, name, memoryMB); err != nil {
		return hibapi.Machine{}, err
	}

	em, err := m.e.Create(ctx, engine.CreateOptions{
		Template: images.Template(image),
		Timeout:  timeoutFor(idle),
		Metadata: map[string]string{labelName: name, labelImage: image, labelIdle: idle.String()},
	})
	if errors.Is(err, engine.ErrNoTemplate) {
		return hibapi.Machine{}, fmt.Errorf("image %q isn't built yet (run: hib image build %s)", image, image)
	}
	if err != nil {
		return hibapi.Machine{}, m.refused(ctx, name, memoryMB, fmt.Errorf("creating %q: %w", name, err))
	}
	if err := m.setupMachine(ctx, em.ID, keys); err != nil {
		return hibapi.Machine{}, m.killFailedSetup(ctx, name, em.ID, err)
	}
	return toAPI(em), nil
}

func (m *Manager) setupMachine(ctx context.Context, id string, keys []byte) error {
	if _, err := m.initMachine(ctx, id, keys); err != nil {
		return err
	}
	if m.setup.empty() {
		return nil
	}
	return m.setup.Write(ctx, m.e, id)
}

func (m *Manager) killFailedSetup(ctx context.Context, name, id string, err error) error {
	kctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	if kerr := m.e.Kill(kctx, id); kerr != nil && !errors.Is(kerr, engine.ErrNotFound) {
		return fmt.Errorf("setting up %q: %w (and killing machine %s: %v)", name, err, id, kerr)
	}
	return fmt.Errorf("setting up %q: %w", name, err)
}

// List returns the machines in state, or all machines if state is "". Machines hibd didn't
// make have no name.
func (m *Manager) List(ctx context.Context, state engine.State) ([]hibapi.Machine, error) {
	var opts engine.ListOptions
	if state != "" {
		opts.States = []engine.State{state}
	}
	ems, err := m.e.List(ctx, opts)
	if err != nil {
		return nil, err
	}
	out := make([]hibapi.Machine, len(ems))
	for i, em := range ems {
		out[i] = toAPI(em)
	}
	return out, nil
}

// Remove kills the machine called name and forgets its host key.
func (m *Manager) Remove(ctx context.Context, name string) error {
	if err := m.checkState(); err != nil {
		return err
	}
	em, err := m.lookup(ctx, name)
	if err != nil {
		return err
	}
	if err := m.e.Kill(ctx, em.ID); err != nil && !errors.Is(err, engine.ErrNotFound) {
		return err
	}
	path, err := m.pinPath(em.ID)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// Pause pauses the machine called name. Pausing a paused machine does nothing.
func (m *Manager) Pause(ctx context.Context, name string) error {
	em, err := m.lookup(ctx, name)
	if err != nil {
		return err
	}
	if err := m.e.Pause(ctx, em.ID); err != nil && !errors.Is(err, engine.ErrNotRunning) {
		return err
	}
	return nil
}

// Conn is a connection to a machine's SSH server.
type Conn struct {
	net.Conn
	// HostKey is the key the SSH server must present: the one pinned for the machine.
	HostKey ssh.PublicKey

	stop func()
	once sync.Once
}

// Close closes the connection. Unless something keeps the machine running, it pauses once it
// has been idle for its idle time from now.
func (c *Conn) Close() error {
	c.once.Do(c.stop)
	return c.Conn.Close()
}

// Dial connects to the SSH server of the machine called name, resuming the machine if it's
// paused. The machine keeps running while the connection is open, unless something pauses it.
// ctx bounds only the dial.
func (m *Manager) Dial(ctx context.Context, name string) (*Conn, error) {
	if err := m.checkState(); err != nil {
		return nil, err
	}
	em, err := m.lookup(ctx, name)
	if err != nil {
		return nil, err
	}
	idle, _ := Idle(em)
	// Connecting resumes a paused machine, which needs room. Renewing a running machine first
	// stops its timeout pausing it before the connection, which would then resume it unchecked.
	paused := em.State == engine.Paused
	if !paused {
		err := Renew(ctx, m.e, em, timeoutFor(idle), m.now())
		if errors.Is(err, engine.ErrNotRunning) {
			paused = true
		} else if err != nil {
			return nil, fmt.Errorf("%q: %w", name, err)
		}
	}
	if paused {
		if err := m.checkRoom(ctx, name, em.MemoryMB); err != nil {
			return nil, err
		}
	}
	fail := func(err error) (*Conn, error) {
		return nil, m.refused(ctx, name, em.MemoryMB, err)
	}
	key, err := m.readPin(em.ID)
	if errors.Is(err, fs.ErrNotExist) {
		// The pin was lost: ask the machine again. Its key file outlives its pin.
		key, err = m.repin(ctx, em.ID, idle)
	}
	if err != nil {
		return fail(fmt.Errorf("%q: %w", name, err))
	}
	conn, err := m.e.DialWebSocket(ctx, em.ID, images.SSHPort, timeoutFor(idle))
	if err != nil {
		return fail(fmt.Errorf("connecting to %q: %w", name, err))
	}
	bg := context.WithoutCancel(ctx)
	kctx, cancel := context.WithCancel(bg)
	done := make(chan struct{})
	go m.keepalive(kctx, em.ID, idle, done)
	return &Conn{Conn: conn, HostKey: key, stop: func() {
		cancel()
		<-done
		m.extend(bg, em.ID, idle)
	}}, nil
}

// checkRoom returns an error unless machine name, with memoryMB of RAM, fits on the host next to
// the running machines, or runs already. It wraps room.ErrFull if the host is full.
func (m *Manager) checkRoom(ctx context.Context, name string, memoryMB int) error {
	ems, h, err := m.roomNow(ctx)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(ems, func(em engine.Machine) bool { return Name(em) == name }) {
		return nil
	}
	return h.Check(name, memoryMB, roomMachines(ems))
}

// Room returns the room on the host for more machines of image.
func (m *Manager) Room(ctx context.Context, image string) (hibapi.Room, error) {
	memoryMB, err := images.MemoryMB(image)
	if err != nil {
		return hibapi.Room{}, err
	}
	ems, h, err := m.roomNow(ctx)
	if err != nil {
		return hibapi.Room{}, err
	}
	free, total := h.MemoryMB()
	return hibapi.Room{
		Fits:         h.Fits(memoryMB, roomMachines(ems)),
		Image:        image,
		MemoryFreeMB: free,
		MemoryMB:     total,
		DiskFreeMB:   h.DiskFree >> 20,
	}, nil
}

// roomNow returns the running machines and a reading of the room on the host.
func (m *Manager) roomNow(ctx context.Context) ([]engine.Machine, room.Host, error) {
	ems, err := m.e.List(ctx, engine.ListOptions{States: []engine.State{engine.Running}})
	if err != nil {
		return nil, room.Host{}, fmt.Errorf("checking the room on the host: %w", err)
	}
	h, err := m.readRoom()
	if err != nil {
		return nil, room.Host{}, fmt.Errorf("checking the room on the host: %w", err)
	}
	return ems, h, nil
}

func roomMachines(ems []engine.Machine) []room.Machine {
	ms := make([]room.Machine, len(ems))
	for i, em := range ems {
		ms[i] = room.Machine{Name: cmp.Or(Name(em), em.ID), MemoryMB: em.MemoryMB}
	}
	return ms
}

// refused returns why the engine didn't start or resume machine name: the room message if the
// host turns out to be full after all, or else err. Another start can take the room between
// checkRoom and the engine's start.
func (m *Manager) refused(ctx context.Context, name string, memoryMB int, err error) error {
	if rerr := m.checkRoom(ctx, name, memoryMB); errors.Is(rerr, room.ErrFull) {
		return rerr
	}
	return err
}

func (m *Manager) repin(ctx context.Context, id string, idle time.Duration) (ssh.PublicKey, error) {
	keys, err := m.authorizedKeys()
	if err != nil {
		return nil, err
	}
	if _, err := m.e.Resume(ctx, id, timeoutFor(idle)); err != nil {
		return nil, err
	}
	return m.initMachine(ctx, id, keys)
}

// keepalive keeps machine id running until ctx is done.
func (m *Manager) keepalive(ctx context.Context, id string, idle time.Duration, done chan<- struct{}) {
	defer close(done)
	if idle == 0 {
		<-ctx.Done() // It runs for MaxRun at a time anyway.
		return
	}
	t := time.NewTicker(idle / 4)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		m.extend(ctx, id, idle)
	}
}

// extend makes machine id run for at least idle from now, if it's running. It never shortens
// the machine's timeout and, unlike Resume, never starts a machine that was paused on purpose.
// Errors are ignored: callers try again later, or leave the machine to its timeout.
func (m *Manager) extend(ctx context.Context, id string, idle time.Duration) {
	if idle == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, cleanupTimeout)
	defer cancel()
	em, err := m.e.Get(ctx, id)
	if err != nil || em.State != engine.Running {
		return
	}
	Renew(ctx, m.e, em, idle, m.now())
}

// Renew makes running machine em run for at least timeout from now. It never shortens the
// machine's timeout or resumes it: it returns engine.ErrNotRunning if em has paused since.
func Renew(ctx context.Context, e engine.Engine, em engine.Machine, timeout time.Duration, now time.Time) error {
	if !em.EndAt.Before(now.Add(timeout)) {
		return nil
	}
	return e.SetTimeout(ctx, em.ID, timeout)
}

// lookup finds the machine called name.
func (m *Manager) lookup(ctx context.Context, name string) (engine.Machine, error) {
	if err := hibapi.CheckName(name); err != nil {
		return engine.Machine{}, err
	}
	ems, err := m.e.List(ctx, engine.ListOptions{Metadata: map[string]string{labelName: name}})
	if err != nil {
		return engine.Machine{}, err
	}
	var found []engine.Machine
	for _, em := range ems {
		if em.Metadata[labelName] == name {
			found = append(found, em)
		}
	}
	switch len(found) {
	case 0:
		return engine.Machine{}, fmt.Errorf("%w called %q", ErrNoMachine, name)
	case 1:
		return found[0], nil
	}
	ids := make([]string, len(found))
	for i, em := range found {
		ids[i] = em.ID
	}
	return engine.Machine{}, fmt.Errorf("%d machines are called %q (%s)", len(found), name, strings.Join(ids, ", "))
}

// initMachine runs machine-init in running machine id, and pins the host key it prints.
func (m *Manager) initMachine(ctx context.Context, id string, keys []byte) (ssh.PublicKey, error) {
	if _, err := m.pinPath(id); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, initTimeout)
	defer cancel()
	res, err := m.e.Exec(ctx, id, engine.ExecRequest{
		Cmd:  images.MachineInit,
		Args: []string{base64.StdEncoding.EncodeToString(keys)},
		User: "root",
	})
	if err != nil {
		return nil, fmt.Errorf("machine-init: %w", err)
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("machine-init exited %d: %q", res.ExitCode, tail(res.Stderr, 1024))
	}
	key, err := hibapi.ParseHostKey(strings.TrimSuffix(string(res.Stdout), "\n"))
	if err != nil {
		return nil, fmt.Errorf("machine-init: %w", err)
	}
	if err := m.writePin(id, key); err != nil {
		return nil, err
	}
	return key, nil
}

// authorizedKeys returns the keys new machines let in, one per line.
func (m *Manager) authorizedKeys() ([]byte, error) {
	if m.keysFile == "" {
		return nil, errors.New("no authorized_keys file to take machines' keys from")
	}
	f, err := os.Open(m.keysFile)
	if err != nil {
		return nil, fmt.Errorf("reading the keys that may log in to machines: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxKeysFile+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxKeysFile {
		return nil, fmt.Errorf("%s is bigger than %d bytes", m.keysFile, maxKeysFile)
	}
	var out []byte
	seen := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		key, _, options, _, err := ssh.ParseAuthorizedKey([]byte(line))
		if err != nil || len(options) > 0 {
			continue
		}
		if _, ok := key.(*ssh.Certificate); ok {
			continue
		}
		k := ssh.MarshalAuthorizedKey(key)
		if !seen[string(k)] {
			seen[string(k)] = true
			out = append(out, k...)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s has no key without options: machines need one to let you in", m.keysFile)
	}
	return out, nil
}

// checkState returns an error unless the state directory and its host-keys directory, which it
// makes if need be, belong to this user and no one else can write to them.
func (m *Manager) checkState() error {
	if m.dir == "" {
		return errors.New("no state directory configured")
	}
	if err := checkDir(m.dir); err != nil {
		return err
	}
	pins := filepath.Join(m.dir, "host-keys")
	if err := os.Mkdir(pins, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	return checkDir(pins)
}

func checkDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("state directory: %w", err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("state directory %s: not a directory", dir)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("state directory %s: can't tell its owner", dir)
	}
	if uid := os.Geteuid(); int(st.Uid) != uid {
		return fmt.Errorf("state directory %s: belongs to uid %d, not %d", dir, st.Uid, uid)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("state directory %s: others can write to it (%v)", dir, fi.Mode().Perm())
	}
	return nil
}

// lock waits for and takes the state directory's lock, which keeps machine names unique.
func (m *Manager) lock(ctx context.Context) (unlock func(), err error) {
	f, err := os.OpenFile(filepath.Join(m.dir, "hibd.lock"), os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { f.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			f.Close()
			return nil, fmt.Errorf("locking %s: %w", f.Name(), err)
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, fmt.Errorf("waiting for another hibd: %w", ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (m *Manager) pinPath(id string) (string, error) {
	if !idRE.MatchString(id) {
		return "", fmt.Errorf("unexpected machine ID %q", id)
	}
	return filepath.Join(m.dir, "host-keys", id), nil
}

func (m *Manager) readPin(id string) (ssh.PublicKey, error) {
	path, err := m.pinPath(id)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	key, err := hibapi.ParseHostKey(strings.TrimSuffix(string(data), "\n"))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return key, nil
}

func (m *Manager) writePin(id string, key ssh.PublicKey) error {
	path, err := m.pinPath(id)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".pin-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(hibapi.FormatHostKey(key) + "\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func toAPI(em engine.Machine) hibapi.Machine {
	name, image := Name(em), em.Metadata[labelImage]
	if hibapi.CheckName(image) != nil {
		image = ""
	}
	var idleSeconds *int64
	if idle, ok := Idle(em); ok {
		s := int64((idle + time.Second - 1) / time.Second) // Round up: only never is 0.
		idleSeconds = &s
	}
	return hibapi.Machine{
		Name:        name,
		ID:          em.ID,
		Image:       image,
		State:       string(em.State),
		StartedAt:   em.StartedAt,
		EndAt:       em.EndAt,
		IdleSeconds: idleSeconds,
	}
}

func tail(b []byte, n int) []byte {
	if len(b) > n {
		return b[len(b)-n:]
	}
	return b
}
