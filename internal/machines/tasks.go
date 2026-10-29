package machines

import (
	"context"
	"errors"
	"fmt"
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
			used += em.CPUs
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
	return nil, errors.New("machines: TaskFits is not implemented")
	// TODO:
	//   ems, h, err := m.roomNow(ctx)
	//   return func(r) error { mb, cpus := plan of r.Spec.Image and r.Spec.Size
	//     return Admit(h, ems, m.taskLim, string(r.ID.Task), mb, cpus) }
}

// FindTask returns the task's machine, found by name, which is the task ID. A machine with the
// name but not the task's label fails with ErrNotTask. found is false when E2B has neither.
func (m *Manager) FindTask(ctx context.Context, id task.ID) (em engine.Machine, found bool, err error) {
	return engine.Machine{}, false, errors.New("machines: FindTask is not implemented")
	// TODO: lookupExact(ctx, m.e, string(id)), then check em.Metadata[task.Label] == string(id).
	// Two machines with the label mean a create landed after a crash: return the oldest and
	// remove the others through the fenced rm.
}

// RoomLock is the admission lock. Every check of room that is followed by a create or a resume
// holds it from the reading through the E2B call: CreateSize, CreateTask, ResumeTask, Wake (so
// the proxy's link wakes) and Dial's resume. So no create or wake in any process spends the
// pages another just counted. It nests inside hibd.lock and outside the task machine lock, and
// is held only for one reading and one E2B call, never for a save or a fork.
const RoomLock = "room.lock"

// CreateTask creates run r's machine, named for its task and labelled with task.Label, if Admit
// allows. It takes hibd.lock, so the name stays unique, and RoomLock from the room reading
// through Engine.Create. A refusal wraps room.ErrFull. Its caller has recorded the intent, so
// after a crash FindTask finds what Create made.
func (m *Manager) CreateTask(ctx context.Context, r task.Run, timeout time.Duration) (engine.Machine, error) {
	return engine.Machine{}, errors.New("machines: CreateTask is not implemented")
	// TODO:
	//   plan := m.planMachine(ctx, r.Spec.Image, size of r.Spec.Size)
	//   unlock := m.lock(ctx)
	//   lookupExact(name) must be ErrNoMachine, else return what is there for FindTask's checks
	//   unlockRoom := WaitLock(RoomLock)
	//   ems, h := m.roomNow(ctx); Admit(h, ems, m.taskLim, name, plan.memoryMB, plan.cpus)
	//   labels: labelName and task.Label are the task ID, labelImage, labelSize, labelIdle the
	//   default idle, so a kept machine pauses like any other once its run ends
	//   m.e.Create(ctx, CreateOptions{Template: plan.template, Timeout: timeout, Metadata: labels})
	//   E2B refusing placement is m.refused(...), which wraps room.ErrFull
	//   setupMachine, with the task's secrets written by Setup, never in an Exec argv
}

// ResumeTask resumes the task's paused machine if Admit allows, under RoomLock. The engine
// underneath is taskmachine's Owner, so the wake guard applies here too.
func (m *Manager) ResumeTask(ctx context.Context, r task.Run, id string, timeout time.Duration) (engine.Machine, error) {
	return engine.Machine{}, errors.New("machines: ResumeTask is not implemented")
	// TODO: RoomLock, Get, running is returned as is, Admit with em.MemoryMB and em.CPUs, Resume
}

// The task machine's pause and removal are Manager.Pause and Manager.Remove by name, the task
// ID. hibd's taskMachines calls them inside pauseFenced, as hibd pause and hibd rm do, so the
// fence and the closing of links stay where they are, and the engine underneath, taskmachine's
// Owner, freezes before the pause. Manager gains no PauseTask or RemoveTask.
