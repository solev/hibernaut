package machines

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/room"
	"github.com/solev/hibernaut/internal/task"
)

// DefaultCPUFactor is how many vCPUs running machines may have per host vCPU.
const DefaultCPUFactor = 2

// TaskLimits are the admission settings beside room. The zero value is the defaults.
type TaskLimits struct {
	// HostCPUs is the host's vCPU count. 0 skips the CPU check.
	HostCPUs int
	// CPUFactor overcommits vCPUs. 0 means DefaultCPUFactor.
	CPUFactor int
	// MaxTasks caps running task machines. 0 means no cap.
	MaxTasks int
}

// PeopleReserveMB is the RAM kept for a person's own command: one default machine. Task
// admission and the proxy's link wakes both hold it back, and nothing else may.
func PeopleReserveMB() int {
	mb, err := images.MemoryMB(images.Default)
	if err != nil {
		panic(fmt.Sprintf("the default image has no size: %v", err))
	}
	return mb
}

// Admit returns nil if a task machine of memoryMB and cpus may start next to running, or an
// error wrapping room.ErrFull that says what is short. It is pure: CreateTask and ResumeTask
// call it under RoomLock with a fresh reading, and the daemon calls it to pick from the queue.
func Admit(h room.Host, running []engine.Machine, lim TaskLimits, name string, memoryMB, cpus int) error {
	if err := h.Reserve(PeopleReserveMB()).Check(name, memoryMB, roomMachines(running)); err != nil {
		return fmt.Errorf("%w, keeping room for one %s machine", err, images.Default)
	}
	if lim.HostCPUs > 0 {
		used := cpus
		for _, em := range running {
			used += cmpOr(em.CPUs, images.DefaultSize().CPUs())
		}
		if limit := lim.HostCPUs * cmpOr(lim.CPUFactor, DefaultCPUFactor); used > limit {
			return fmt.Errorf("%w: %s needs %d vCPUs and running machines would have %d of %d",
				room.ErrFull, name, cpus, used, limit)
		}
	}
	if lim.MaxTasks > 0 {
		n := 0
		for _, em := range running {
			if em.Metadata[task.Label] != "" {
				n++
			}
		}
		if n >= lim.MaxTasks {
			return fmt.Errorf("%w: %d task machines are running, the most max-tasks allows",
				room.ErrFull, n)
		}
	}
	return nil
}

func cmpOr(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}

// ErrTaskName refuses a person's machine named like a task, so FindTask's lookup by name never
// meets one. Only CreateTask makes machines with such names.
var ErrTaskName = errors.New("names of t- and 8 letters or digits 2 to 7 are kept for tasks")

// ErrNotTask means a machine has a task's name but not its label: someone else's machine.
var ErrNotTask = errors.New("the machine with the task's name isn't the task's")

// refuseTaskCopy refuses a task's machine to Fork and SaveImage before they touch it. No task
// machine access allows a copy, so the label decides without reading tasks.db.
func refuseTaskCopy(em engine.Machine) error {
	if tid := em.Metadata[task.Label]; tid != "" {
		return fmt.Errorf("refused to copy task machine %s: %w", tid, task.ErrNoCopy)
	}
	return nil
}

// TaskFits returns Admit as a predicate over runs, on one reading taken now, for task.Pick. It
// is advice for the order: CreateTask decides.
func (m *Manager) TaskFits(ctx context.Context) (func(task.Run) error, error) {
	reading, err := m.roomNow(ctx)
	if err != nil {
		return nil, err
	}
	return func(r task.Run) error {
		plan, err := m.planRun(ctx, r)
		if err != nil {
			return err
		}
		return Admit(reading.host, reading.running, m.taskLim, string(r.ID.Task),
			plan.memoryMB, plan.cpus)
	}, nil
}

// FindTask returns the task's machine, found by name, which is the task ID. A machine with the
// name but not the task's label fails with ErrNotTask. found is false when E2B has neither.
func (m *Manager) FindTask(ctx context.Context, id task.ID) (em engine.Machine, found bool, err error) {
	unlock, err := m.lock(ctx)
	if err != nil {
		return engine.Machine{}, false, err
	}
	defer unlock()
	return m.findTask(ctx, id)
}

func (m *Manager) findTask(ctx context.Context, id task.ID) (engine.Machine, bool, error) {
	name := string(id)
	ems, err := m.e.List(ctx, engine.ListOptions{Metadata: map[string]string{labelName: name}})
	if err != nil {
		return engine.Machine{}, false, safeError(err)
	}
	ems = slices.DeleteFunc(ems, func(em engine.Machine) bool { return em.Metadata[labelName] != name })
	for _, em := range ems {
		if em.Metadata[task.Label] != name {
			return engine.Machine{}, false, fmt.Errorf("%w: %s (%s)", ErrNotTask, name, em.ID)
		}
	}
	if len(ems) == 0 {
		return engine.Machine{}, false, nil
	}
	slices.SortFunc(ems, func(a, b engine.Machine) int {
		if n := a.StartedAt.Compare(b.StartedAt); n != 0 {
			return n
		}
		return strings.Compare(a.ID, b.ID)
	})
	for _, duplicate := range ems[1:] {
		remove := func(ctx context.Context) error { return m.killCopy(ctx, duplicate.ID) }
		var err error
		if m.fenced != nil {
			err = m.fenced(ctx, duplicate.ID, name, "rm duplicate", remove)
		} else {
			err = remove(ctx)
		}
		if err != nil {
			return engine.Machine{}, false, fmt.Errorf("removing duplicate %s of %s: %w", duplicate.ID, name, err)
		}
	}
	return ems[0], true, nil
}

// RoomLock is the admission lock. Every check of room that is followed by a create or a resume
// holds it from the reading through the E2B call: CreateSize, CreateTask, ResumeTask, Wake (so
// the proxy's link wakes) and Dial's resume. So no create or wake in any process spends the
// pages another just counted. It nests inside hibd.lock and outside the task machine lock, and
// is held only for one reading and one E2B call, never for a whole save or fork.
const RoomLock = "room.lock"

// CreateTask creates run r's machine, named for its task and labelled with task.Label, if Admit
// allows. It takes hibd.lock, so the name stays unique, and RoomLock from the room reading
// through Engine.Create. A refusal wraps room.ErrFull. Its caller has recorded the intent, so
// after a crash FindTask finds what Create made.
func (m *Manager) CreateTask(ctx context.Context, r task.Run, timeout time.Duration) (engine.Machine, error) {
	plan, err := m.planRun(ctx, r)
	if err != nil {
		return engine.Machine{}, err
	}
	if err := m.checkState(); err != nil {
		return engine.Machine{}, err
	}
	keys, err := m.authorizedKeys()
	if err != nil {
		return engine.Machine{}, err
	}
	unlock, err := m.lock(ctx)
	if err != nil {
		return engine.Machine{}, err
	}
	defer unlock()
	if em, found, err := m.findTask(ctx, r.ID.Task); err != nil || found {
		return em, err
	}
	name := string(r.ID.Task)
	unlockRoom, err := m.lockRoom(ctx)
	if err != nil {
		return engine.Machine{}, err
	}
	em, err := func() (engine.Machine, error) {
		defer unlockRoom()
		if err := m.admitTask(ctx, name, plan.memoryMB, plan.cpus); err != nil {
			return engine.Machine{}, err
		}
		labels := map[string]string{
			labelName: name, task.Label: name, labelImage: r.Spec.Image,
			labelSize: plan.size.Name(), labelIdle: DefaultIdle.String(),
		}
		em, err := m.e.Create(ctx, engine.CreateOptions{
			Template: plan.template, Timeout: timeout, Metadata: labels,
		})
		if err != nil {
			cause := fmt.Errorf("creating %q: %w", name, safeError(err))
			if errors.Is(err, engine.ErrNoTemplate) && images.IsBuiltIn(r.Spec.Image) {
				cause = describe(err, notBuilt(r.Spec.Image, plan.size))
			}
			return engine.Machine{}, m.refused(ctx, name, plan.memoryMB, false, cause)
		}
		return em, nil
	}()
	if err != nil {
		return engine.Machine{}, m.failedCreate(ctx, name, err)
	}
	setup := m.setupMachine
	if !images.IsBuiltIn(r.Spec.Image) {
		setup = m.setupCopy
	}
	if err := setup(ctx, em.ID, keys); err != nil {
		return engine.Machine{}, m.killFailedSetup(ctx, name, em.ID, err)
	}
	m.touch(em.ID)
	return em, nil
}

// ResumeTask resumes the task's paused machine if Admit allows, under RoomLock. The engine
// underneath is taskmachine's Owner, so the wake guard applies here too.
func (m *Manager) ResumeTask(ctx context.Context, r task.Run, id string, timeout time.Duration) (engine.Machine, error) {
	unlock, err := m.lockRoom(ctx)
	if err != nil {
		return engine.Machine{}, err
	}
	defer unlock()
	em, err := m.e.Get(ctx, id)
	if err != nil {
		return engine.Machine{}, safeError(err)
	}
	if em.Metadata[task.Label] != string(r.ID.Task) || Name(em) != string(r.ID.Task) {
		return engine.Machine{}, fmt.Errorf("%w: %s", ErrNotTask, id)
	}
	if em.State == engine.Running {
		return em, nil
	}
	if err := m.admitTask(ctx, Name(em), em.MemoryMB, cmpOr(em.CPUs, images.DefaultSize().CPUs())); err != nil {
		return engine.Machine{}, err
	}
	resumed, err := m.e.Resume(ctx, id, timeout)
	if err != nil {
		return engine.Machine{}, m.refused(ctx, Name(em), em.MemoryMB, false, safeError(err))
	}
	return resumed, nil
}

func (m *Manager) planRun(ctx context.Context, r task.Run) (machinePlan, error) {
	var size images.Size
	if r.Spec.Size != "" {
		var err error
		size, err = images.SizeNamed(r.Spec.Size)
		if err != nil {
			return machinePlan{}, err
		}
	}
	return m.planMachine(ctx, r.Spec.Image, size)
}

func (m *Manager) admitTask(ctx context.Context, name string, memoryMB, cpus int) error {
	reading, err := m.roomNow(ctx)
	if err != nil {
		return err
	}
	return Admit(reading.host, reading.running, m.taskLim, name, memoryMB, cpus)
}

// Task keep and removal use hibd's fenced pause and Manager.Remove by name. FindTask removes
// crash duplicates by ID through Config.Fenced while holding hibd.lock, before a name is safe
// to use again. Manager has no PauseTask or RemoveTask.
