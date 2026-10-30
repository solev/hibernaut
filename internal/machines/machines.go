// Package machines is what hibd does with machines: it makes them from images, finds them by
// name, and connects to their SSH servers.
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
	"github.com/solev/hibernaut/internal/atomicfile"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/links"
	"github.com/solev/hibernaut/internal/room"
	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/termsafe"
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
	labelFork  = "hib.forkof"
	labelSize  = "hib.size"

	maxKeysFile    = 64 << 10
	initTimeout    = 2 * time.Minute
	cleanupTimeout = time.Minute
	// heartbeatEvery is how often an open session records the machine's activity. It has to
	// tick well inside links.LinkGrace, and it ticks whatever the machine's idle time.
	heartbeatEvery = 30 * time.Second
)

var (
	// ErrNoMachine is returned for a name no machine has.
	ErrNoMachine = errors.New("no machine")
	// ErrExists is returned when creating a machine whose name is taken.
	ErrExists = errors.New("there is already a machine")
	// ErrBusy is wrapped by SweepForks' error when another call holds the state lock, such as a
	// fork still making its copies. Try again later.
	ErrBusy = errors.New("the state lock is held")
)

var idRE = regexp.MustCompile(`^[a-z0-9]{1,64}$`)

// Config configures a Manager.
type Config struct {
	Engine engine.Engine
	// StateDir is where the lock and pinned host keys are kept. It must exist and be writable
	// only by its owner. Non-root callers must own it. Root may use trusted user-owned state,
	// and keeps its owner's UID and GID on new directories, locks and pins.
	// Lifecycle operations use its lock.
	// Create, Fork, Remove and Dial also use its pinned host keys.
	StateDir string
	// AuthorizedKeys is the authorized_keys file whose keys may log in to new machines: the
	// host's own. Only keys without options are copied: options restrict a key on the host,
	// and don't mean the same in a machine.
	AuthorizedKeys string
	// Now tells the time. Default time.Now.
	Now func() time.Time
	// ReadRoom reads the room on the host for creates, resumes, saves and forks. Default room.Read.
	ReadRoom func() (room.Host, error)
	// Setup is written into each new machine after machine-init. An empty Setup writes nothing
	// for built-in images, and clears the managed environment block in saved-image and fork copies.
	Setup Setup
	// Durable returns once machine id's newest snapshot is on disk. SaveImage and Fork call it
	// on the machine they save, before saving, and refuse to run without it.
	Durable func(ctx context.Context, id string) error
	// Awake returns why, if the host is going to sleep. SaveImage and Fork call it when they
	// start, before they save, and before each copy. Default: always awake.
	Awake func() error
	// BootID reads the host boot ID. An unreadable ID is the empty string.
	BootID func() string
	// Activity is the host's activity clock. A successful create, each fork copy and every open
	// session (Dial) record the machine's activity in it. Default: links.NewClock(StateDir), or
	// nothing recorded without a StateDir.
	Activity links.Clock
	// Fence fences machine id before Wake pauses it again, and the pause happens only while it
	// holds. Default: no fence, which is right only while links are off.
	Fence func(ctx context.Context, id, why string) (release func(), err error)
	// Fenced runs op on machine id the way every pause runs with links on: behind id's fence, so
	// nothing admits a link to or from id, and only once id's links are closed. SaveImage and
	// Fork run their whole round trip in it, from before the first pause or resume of the
	// machine they save until it is back in the state it started in. name is what messages call
	// the machine and why is the fence's reason. Fenced returns op's error as it was, or its own
	// when it left the machine as it was without running op. FindTask also uses it to remove
	// unused task duplicates by ID, including during task removal. Default: op runs unfenced,
	// which is right only while links are off.
	Fenced func(ctx context.Context, id, name, why string, op func(context.Context) error) error
	// TaskLimits are task admission's settings beside room. The zero value is the defaults.
	TaskLimits TaskLimits
}

// Manager manages machines. Its methods may be called concurrently, also from other
// processes sharing its state directory.
type Manager struct {
	e         engine.Engine
	dir       string
	keysFile  string
	now       func() time.Time
	readRoom  func() (room.Host, error)
	setup     Setup
	durable   func(ctx context.Context, id string) error
	awake     func() error
	euid      func() int
	chownFile func(*os.File, int, int) error
	waitCut   func(context.Context, time.Duration) error
	bootID    func() string
	activity  links.Clock
	fence     func(ctx context.Context, id, why string) (release func(), err error)
	fenced    func(ctx context.Context, id, name, why string, op func(context.Context) error) error
	taskLim   TaskLimits
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
	awake := cfg.Awake
	if awake == nil {
		awake = func() error { return nil }
	}
	bootID := cfg.BootID
	if bootID == nil {
		bootID = readBootID
	}
	activity := cfg.Activity
	if activity == nil && cfg.StateDir != "" {
		activity = links.NewClock(cfg.StateDir)
	}
	return &Manager{
		e: cfg.Engine, dir: cfg.StateDir, keysFile: cfg.AuthorizedKeys, now: now, readRoom: readRoom,
		setup: cfg.Setup, durable: cfg.Durable, awake: awake,
		euid: os.Geteuid, chownFile: (*os.File).Chown,
		waitCut:  waitCut,
		bootID:   bootID,
		activity: activity,
		fence:    cfg.Fence,
		fenced:   cfg.Fenced,
		taskLim:  cfg.TaskLimits,
	}
}

// touch records that machine id did something that was not a link.
func (m *Manager) touch(id string) {
	if m.activity != nil {
		m.activity.Touch(id, m.now())
	}
}

// Name returns the name of machine em, or "" if hibd didn't make it.
func Name(em engine.Machine) string {
	if name := em.Metadata[labelName]; hibapi.CheckName(name) == nil {
		return name
	}
	return ""
}

// Idle returns how long machine em keeps running once it's idle before it pauses, 0 for never,
// and whether hibd made it. Missing or invalid idle times get DefaultIdle.
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

func timeoutFor(idle time.Duration) time.Duration {
	if idle == 0 {
		return MaxRun
	}
	return idle
}

// Create makes a machine called name from image at the image's own size. It's CreateSize with
// the zero Size.
func (m *Manager) Create(ctx context.Context, name, image string, idle time.Duration) (hibapi.Machine, error) {
	return m.CreateSize(ctx, name, image, images.Size{}, idle)
}

// CreateSize makes a machine called name from image at size, lets in the keys from
// Config.AuthorizedKeys, and pins its host key. The zero Size is the image's own size: the
// default for a built-in image, and the size of the machine a saved image was saved from. A
// saved image's machines keep that size, so any other Size with a saved image is refused. The
// machine pauses once it has been idle for idle; 0 means never. A machine that can't be set up is
// killed again.
func (m *Manager) CreateSize(ctx context.Context, name, image string, size images.Size, idle time.Duration) (hibapi.Machine, error) {
	if err := hibapi.CheckName(name); err != nil {
		return hibapi.Machine{}, err
	}
	if _, err := task.ParseID(name); err == nil {
		return hibapi.Machine{}, fmt.Errorf("%w: %s", ErrTaskName, name)
	}
	plan, err := m.planMachine(ctx, image, size)
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
	switch _, err := lookupExact(ctx, m.e, name); {
	case err == nil:
		return hibapi.Machine{}, fmt.Errorf("%w called %q", ErrExists, name)
	case !errors.Is(err, ErrNoMachine):
		return hibapi.Machine{}, err
	}
	builtIn := images.IsBuiltIn(image)
	unlockRoom, err := m.checkRoom(ctx, name, plan.memoryMB, builtIn)
	if err != nil {
		return hibapi.Machine{}, err
	}

	labels := map[string]string{labelName: name, labelImage: image, labelIdle: idle.String()}
	if plan.size != (images.Size{}) {
		labels[labelSize] = plan.size.Name()
	}
	em, err := m.e.Create(ctx, engine.CreateOptions{Template: plan.template, Timeout: timeoutFor(idle), Metadata: labels})
	unlockRoom()
	if err != nil {
		cause := fmt.Errorf("creating %q: %w", name, safeError(err))
		if errors.Is(err, engine.ErrNoTemplate) && builtIn {
			cause = describe(err, notBuilt(image, plan.size))
		}
		cause = m.refused(ctx, name, plan.memoryMB, builtIn, cause)
		return hibapi.Machine{}, m.failedCreate(ctx, name, cause)
	}
	setup := m.setupMachine
	if !builtIn {
		setup = m.setupCopy
	}
	if err := setup(ctx, em.ID, keys); err != nil {
		return hibapi.Machine{}, m.killFailedSetup(ctx, name, em.ID, err)
	}
	m.touch(em.ID)
	return toAPI(em), nil
}

func (m *Manager) setupMachine(ctx context.Context, id string, keys []byte) error {
	return m.retryCut(ctx, func(ctx context.Context) error {
		if _, err := m.initMachine(ctx, id, keys); err != nil {
			return err
		}
		if m.setup.empty() {
			return nil
		}
		return m.setup.Write(ctx, m.e, id)
	})
}

func (m *Manager) killFailedSetup(ctx context.Context, name, id string, err error) error {
	kctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	cause := fmt.Errorf("setting up %q: %w", name, safeError(err))
	if kerr := m.killCopy(kctx, id); kerr != nil {
		return m.copyLeftBehind(cause, name, id, kerr)
	}
	return cause
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
		return nil, safeError(err)
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
	unlock, err := m.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	em, err := m.lookup(ctx, name)
	if err != nil {
		return err
	}
	return m.killCopy(ctx, em.ID)
}

// RemoveID kills exactly id and forgets its host key, even if the machine is already gone.
// The caller holds id's fence when links are on.
func (m *Manager) RemoveID(ctx context.Context, id string) error {
	if err := m.checkState(); err != nil {
		return err
	}
	unlock, err := m.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	return m.killCopy(ctx, id)
}

// Pause pauses the machine called name. Pausing a paused machine does nothing.
func (m *Manager) Pause(ctx context.Context, name string) error {
	if err := m.checkState(); err != nil {
		return err
	}
	unlock, err := m.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
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
// paused. The machine keeps running while the connection is open, unless something pauses it,
// and its activity is recorded every heartbeatEvery for as long as it is open, whatever its
// idle time. ctx bounds only the dial.
func (m *Manager) Dial(ctx context.Context, name string) (*Conn, error) {
	if err := m.checkState(); err != nil {
		return nil, err
	}
	em, err := m.lookup(ctx, name)
	if err != nil {
		return nil, err
	}
	idle, _ := Idle(em)

	if em.State != engine.Paused {
		err := Renew(ctx, m.e, em, timeoutFor(idle), m.now())
		if errors.Is(err, engine.ErrNotRunning) {
			em.State = engine.Paused
		} else if err != nil {
			return nil, fmt.Errorf("%q: %w", name, err)
		}
	}
	fail := func(err error) (*Conn, error) {
		return nil, m.refused(ctx, name, em.MemoryMB, false, err)
	}
	key, err := m.readPin(em.ID)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fail(fmt.Errorf("%q: %w", name, err))
	}
	var keysForPin []byte
	pin := func(ctx context.Context) error {
		var err error
		key, err = m.initMachine(ctx, em.ID, keysForPin)
		return err
	}
	if key == nil {
		keysForPin, err = m.authorizedKeys()
		if err != nil {
			return fail(fmt.Errorf("%q: %w", name, err))
		}
		if em.State != engine.Paused {
			err = m.retryCut(ctx, pin)
			if errors.Is(err, engine.ErrNotRunning) {
				em.State = engine.Paused
			} else if err != nil {
				return fail(fmt.Errorf("%q: %w", name, err))
			}
		}
	}
	var conn net.Conn
	if em.State != engine.Paused {
		conn, err = m.e.DialRunningWebSocket(ctx, em.ID, images.SSHPort)
		if errors.Is(err, engine.ErrNotRunning) || errors.Is(err, engine.ErrNoTrafficToken) {
			em.State = engine.Paused
		}
	}
	if em.State == engine.Paused {
		err = m.retryResume(ctx, name, em, func(ctx context.Context) error {
			if key == nil {
				if _, err := m.e.Resume(ctx, em.ID, timeoutFor(idle)); err != nil {
					return err
				}
			}
			var err error
			conn, err = m.e.DialWebSocket(ctx, em.ID, images.SSHPort, timeoutFor(idle))
			return err
		})
	}
	if err != nil {
		if errors.Is(err, room.ErrFull) {
			return nil, err
		}
		return fail(fmt.Errorf("connecting to %q: %w", name, err))
	}
	if key == nil {
		if err := m.retryCut(ctx, pin); err != nil {
			conn.Close()
			return fail(fmt.Errorf("%q: %w", name, err))
		}
	}
	m.touch(em.ID)
	bg := context.WithoutCancel(ctx)
	kctx, cancel := context.WithCancel(bg)
	done := make(chan struct{})
	beat := make(chan struct{})
	go m.keepalive(kctx, em.ID, idle, done)
	go m.heartbeat(kctx, em.ID, beat)
	return &Conn{Conn: conn, HostKey: key, stop: func() {
		cancel()
		<-done
		<-beat
		m.touch(em.ID)
		m.extend(bg, em.ID, idle)
	}}, nil
}

// heartbeat records machine id's activity until ctx is done. It is not keepalive, which never
// ticks for a machine that never pauses.
func (m *Manager) heartbeat(ctx context.Context, id string, done chan<- struct{}) {
	defer close(done)
	if m.activity == nil {
		return
	}
	t := time.NewTicker(heartbeatEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		m.touch(id)
	}
}

func (m *Manager) checkRoom(ctx context.Context, name string, memoryMB int, suggestSizes bool) (func(), error) {
	unlock, err := m.lockRoom(ctx)
	if err != nil {
		return nil, err
	}
	if err := m.roomError(ctx, name, memoryMB, suggestSizes); err != nil {
		unlock()
		return nil, err
	}
	return unlock, nil
}

func (m *Manager) roomError(ctx context.Context, name string, memoryMB int, suggestSizes bool) error {
	ems, err := m.runningNow(ctx)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(ems, func(em engine.Machine) bool { return Name(em) == name }) {
		return nil
	}
	h, err := m.readRoom()
	if err != nil {
		return fmt.Errorf("checking the room on the host: %w", err)
	}
	running := roomMachines(ems)
	err = h.Check(name, memoryMB, running)
	if err == nil || !suggestSizes {
		return err
	}
	var fit []string
	for _, s := range sizesRoom(h, running) {
		if s.Fits > 0 {
			fit = append(fit, s.Size)
		}
	}
	if len(fit) == 0 {
		return err
	}
	return fmt.Errorf("%w. Sizes that still fit: %s", err, strings.Join(fit, ", "))
}

func sizesRoom(h room.Host, running []room.Machine) []hibapi.SizeRoom {
	var out []hibapi.SizeRoom
	for _, s := range images.Sizes() {
		out = append(out, hibapi.SizeRoom{Size: s.Name(), Fits: h.Fits(s.MemoryMB(), running)})
	}
	return out
}

// Room returns the room on the host for more machines of image.
func (m *Manager) Room(ctx context.Context, image string) (hibapi.Room, error) {
	memoryMB, err := m.imageMemory(ctx, image)
	if err != nil {
		return hibapi.Room{}, err
	}
	reading, err := m.roomNow(ctx)
	if err != nil {
		return hibapi.Room{}, err
	}
	h := reading.host
	free, total := h.MemoryMB()
	running := roomMachines(reading.running)
	return hibapi.Room{
		Fits:         h.Fits(memoryMB, running),
		Image:        image,
		MemoryFreeMB: free,
		MemoryMB:     total,
		DiskFreeMB:   h.DiskFree >> 20,
		Sizes:        sizesRoom(h, running),
	}, nil
}

type roomReading struct {
	running []engine.Machine
	host    room.Host
}

func (m *Manager) roomNow(ctx context.Context) (roomReading, error) {
	ems, err := m.runningNow(ctx)
	if err != nil {
		return roomReading{}, err
	}
	h, err := m.readRoom()
	if err != nil {
		return roomReading{}, fmt.Errorf("checking the room on the host: %w", err)
	}
	return roomReading{running: ems, host: h}, nil
}

func (m *Manager) runningNow(ctx context.Context) ([]engine.Machine, error) {
	ems, err := m.e.List(ctx, engine.ListOptions{States: []engine.State{engine.Running}})
	if err != nil {
		return nil, fmt.Errorf("checking the room on the host: %w", err)
	}
	return ems, nil
}

func roomMachines(ems []engine.Machine) []room.Machine {
	ms := make([]room.Machine, len(ems))
	for i, em := range ems {
		ms[i] = room.Machine{Name: cmp.Or(Name(em), em.ID), MemoryMB: em.MemoryMB}
	}
	return ms
}

func (m *Manager) refused(ctx context.Context, name string, memoryMB int, suggestSizes bool, err error) error {
	if rerr := m.roomError(ctx, name, memoryMB, suggestSizes); errors.Is(rerr, room.ErrFull) {
		return rerr
	}
	return err
}

func (m *Manager) resumeRoom(ctx context.Context, em engine.Machine, timeout time.Duration) (engine.Machine, error) {
	unlock, err := m.checkRoom(ctx, Name(em), em.MemoryMB, false)
	if err != nil {
		return engine.Machine{}, err
	}
	resumed, err := m.e.Resume(ctx, em.ID, timeout)
	unlock()
	if err != nil {
		return engine.Machine{}, m.refused(ctx, Name(em), em.MemoryMB, false, err)
	}
	return resumed, nil
}

func (m *Manager) keepalive(ctx context.Context, id string, idle time.Duration, done chan<- struct{}) {
	defer close(done)
	if idle == 0 {
		<-ctx.Done()
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
// machine's timeout or resumes it. If renewal is needed, it returns engine.ErrNotRunning if
// em has paused since.
func Renew(ctx context.Context, e engine.Engine, em engine.Machine, timeout time.Duration, now time.Time) error {
	if !em.EndAt.Before(now.Add(timeout)) {
		return nil
	}
	return e.SetTimeout(ctx, em.ID, timeout)
}

func (m *Manager) lookup(ctx context.Context, name string) (engine.Machine, error) {
	return Lookup(ctx, m.e, name)
}

func (m *Manager) initMachine(ctx context.Context, id string, keys []byte) (ssh.PublicKey, error) {
	if _, err := m.pinPath(id); err != nil {
		return nil, err
	}
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

func (m *Manager) checkState() error {
	if m.dir == "" {
		return errors.New("no state directory configured")
	}
	root, owner, err := m.stateRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	return m.ensurePins(root, owner)
}

func (m *Manager) lock(ctx context.Context) (unlock func(), err error) {
	f, err := m.openLock()
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
	if m.euid() == 0 {
		return m.writeOwnedPin(id, []byte(hibapi.FormatHostKey(key)+"\n"))
	}
	return atomicfile.WriteFile(path, []byte(hibapi.FormatHostKey(key)+"\n"), 0o600)
}

func toAPI(em engine.Machine) hibapi.Machine {
	name, image := Name(em), em.Metadata[labelImage]
	if hibapi.CheckName(image) != nil {
		image = ""
	}
	forkOf := em.Metadata[labelFork]
	if hibapi.CheckName(forkOf) != nil {
		forkOf = ""
	}
	var idleSeconds *int64
	if idle, ok := Idle(em); ok {
		s := int64((idle + time.Second - 1) / time.Second)
		idleSeconds = &s
	}
	size, _ := machineSize(em)
	return hibapi.Machine{
		Name:        name,
		ID:          termsafe.String(em.ID),
		Image:       image,
		Size:        size.Name(),
		ForkOf:      forkOf,
		State:       termsafe.String(string(em.State)),
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
