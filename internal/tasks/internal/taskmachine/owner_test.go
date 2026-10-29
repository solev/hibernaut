package taskmachine

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/pauseall"
	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/taskguest/guestsim"
	"github.com/solev/hibernaut/internal/tasks/internal/taskstore"
)

// guarded are the engine methods that pause, wake or extend a machine, which Owner must
// define itself. Every other method passes through. A new engine method fails this test until
// someone decides which it is.
var (
	guarded     = []string{"DialWebSocket", "Pause", "Resume", "SaveTemplate", "SetTimeout"}
	passThrough = []string{"BuildTemplate", "Create", "DeleteTemplate", "Exec", "Get", "Kill",
		"List", "Metrics", "SavedTemplates", "Template", "Upload"}
)

func TestEveryMethodIsClassified(t *testing.T) {
	own := declared(t)
	ifc := reflect.TypeFor[engine.Engine]()
	for i := range ifc.NumMethod() {
		name := ifc.Method(i).Name
		switch {
		case slices.Contains(guarded, name) && !own[name]:
			t.Errorf("Owner must define %s: it can pause or wake a task machine", name)
		case slices.Contains(passThrough, name) && own[name]:
			t.Errorf("Owner defines %s, which is listed as passing through", name)
		case !slices.Contains(guarded, name) && !slices.Contains(passThrough, name):
			t.Errorf("engine.Engine.%s is new: list it as guarded or passing through", name)
		}
	}
}

// declared returns the methods owner.go declares on *Owner, read from the source, since
// reflection can't tell them from the embedded engine's.
func declared(t *testing.T) map[string]bool {
	f, err := parser.ParseFile(token.NewFileSet(), "owner.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	own := map[string]bool{}
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv != nil {
			if star, ok := fn.Recv.List[0].Type.(*ast.StarExpr); ok {
				if id, ok := star.X.(*ast.Ident); ok && id.Name == "Owner" {
					own[fn.Name.Name] = true
				}
			}
		}
	}
	return own
}

type memLocks struct {
	mu   sync.Mutex
	held map[string]*sync.Mutex
}

func (l *memLocks) WaitLock(ctx context.Context, name, what string) (func(), error) {
	l.mu.Lock()
	if l.held == nil {
		l.held = map[string]*sync.Mutex{}
	}
	m := l.held[name]
	if m == nil {
		m = &sync.Mutex{}
		l.held[name] = m
	}
	l.mu.Unlock()
	m.Lock()
	return m.Unlock, nil
}

var t0 = time.Date(2025, 10, 28, 9, 0, 0, 0, time.UTC)

// fixture is a task whose run has its command stage live in a running machine, plus a plain
// machine beside it.
type fixture struct {
	sim       *guestsim.Sim
	store     *taskstore.Memory
	owner     *Owner
	now       time.Time
	run       task.RunID
	tm, plain string
}

func newFixture(t *testing.T) *fixture {
	ctx := context.Background()
	f := &fixture{now: t0, store: taskstore.NewMemory()}
	now := func() time.Time { return f.now }
	f.sim = guestsim.New(now)
	f.owner = New(f.sim, f.store, &memLocks{}, now, "test")
	rc, err := f.store.Submit(ctx, task.Submission{RequestID: "r1", Spec: task.Spec{
		Command: []string{"codex"}, Image: "dev", Size: "dev", Timeout: time.Hour},
		Actor: task.Actor{Name: "alice"}, At: t0}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.run = rc.Run
	tm, err := f.sim.Create(ctx, engine.CreateOptions{Template: "dev", Timeout: time.Minute,
		Metadata: map[string]string{task.Label: string(f.run.Task)}})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := f.sim.Create(ctx, engine.CreateOptions{Template: "dev", Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	f.tm, f.plain = tm.ID, plain.ID
	for i, b := range []task.Body{&task.MachineIntent{}, &task.MachineResult{ID: tm.ID},
		&task.UploadIntent{}, &task.UploadResult{}, &task.StageIntent{Stage: task.StageCommand},
		&task.StageLaunched{Stage: task.StageCommand}} {
		c := task.Checkpoint{Run: f.run, Seq: i + 1, At: t0, By: "d1", Body: b}
		if err := f.store.Append(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *fixture) frozen(t *testing.T) bool {
	r, err := f.store.Run(context.Background(), f.run)
	if err != nil {
		t.Fatal(err)
	}
	return r.Frozen()
}

// end records the rest of the run, so it ends with its machine kept.
func (f *fixture) end(t *testing.T) {
	ctx := context.Background()
	r, err := f.store.Run(ctx, f.run)
	if err != nil {
		t.Fatal(err)
	}
	for i, b := range []task.Body{&task.StageExit{Stage: task.StageCommand,
		Exit: task.ExitRecord{Result: "success", Code: "exited"}}, &task.CollectIntent{},
		&task.CollectResult{}, &task.KeepIntent{}, &task.KeepResult{}} {
		c := task.Checkpoint{Run: f.run, Seq: len(r.Log) + i + 1, At: f.now, By: "d1", Body: b}
		if err := f.store.Append(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPauseFreezesFirst(t *testing.T) {
	f := newFixture(t)
	if err := f.owner.Pause(context.Background(), f.tm); err != nil {
		t.Fatal(err)
	}
	if !f.sim.Frozen(f.tm) || !f.frozen(t) {
		t.Fatalf("slice frozen %v, row frozen %v: want both", f.sim.Frozen(f.tm), f.frozen(t))
	}
	if f.sim.Frozen(f.plain) {
		t.Fatal("a plain machine was touched")
	}
}

// TestPauseAllFreezes runs the shutdown pause-all through the Owner, as hibd's engine() wires
// it in both the links-on and links-off branches.
func TestPauseAllFreezes(t *testing.T) {
	f := newFixture(t)
	if _, err := pauseall.Run(context.Background(), f.owner, pauseall.Options{}); err != nil {
		t.Fatal(err)
	}
	if !f.frozen(t) {
		t.Fatal("pause-all paused a task machine without the freeze")
	}
	m, _ := f.sim.Get(context.Background(), f.plain)
	if m.State != engine.Paused {
		t.Fatal("pause-all skipped a plain machine")
	}
}

func TestWakeGuard(t *testing.T) {
	ctx := context.Background()
	t.Run("frozen with time left wakes", func(t *testing.T) {
		f := newFixture(t)
		f.owner.Pause(ctx, f.tm)
		m, err := f.owner.Resume(ctx, f.tm, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if want := t0.Add(time.Hour + task.TimeoutMargin); m.EndAt.Before(want) {
			t.Fatalf("timeout ends %v, want at least the deadline plus margin %v", m.EndAt, want)
		}
		if !f.sim.Frozen(f.tm) {
			t.Fatal("a wake thawed the stage: only the daemon thaws")
		}
	})
	t.Run("frozen past the deadline stays paused", func(t *testing.T) {
		f := newFixture(t)
		f.owner.Pause(ctx, f.tm)
		f.now = t0.Add(2 * time.Hour)
		if _, err := f.owner.DialWebSocket(ctx, f.tm, 22, time.Minute); !errors.Is(err, task.ErrPastDeadline) {
			t.Fatalf("dial = %v, want ErrPastDeadline", err)
		}
	})
	t.Run("paused unfrozen is held", func(t *testing.T) {
		f := newFixture(t)
		f.sim.Pause(ctx, f.tm)
		if _, err := f.owner.Resume(ctx, f.tm, time.Minute); !errors.Is(err, task.ErrHeld) {
			t.Fatalf("resume = %v, want ErrHeld", err)
		}
	})
	t.Run("plain machines pass", func(t *testing.T) {
		f := newFixture(t)
		f.sim.Pause(ctx, f.plain)
		if _, err := f.owner.Resume(ctx, f.plain, time.Minute); err != nil {
			t.Fatal(err)
		}
	})
}

// TestNoCopyOfATaskMachine checks no save or fork copies a task machine, live or kept.
func TestNoCopyOfATaskMachine(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.owner.SaveTemplate(ctx, f.tm, "copy"); !errors.Is(err, task.ErrNoCopy) {
		t.Fatalf("save of a live task machine = %v, want ErrNoCopy", err)
	}
	f.end(t)
	if _, err := f.owner.SaveTemplate(ctx, f.tm, "copy"); !errors.Is(err, task.ErrNoCopy) {
		t.Fatalf("save of a kept task machine = %v, want ErrNoCopy", err)
	}
	if _, err := f.owner.SaveTemplate(ctx, f.plain, "copy"); errors.Is(err, ErrRefused) {
		t.Fatalf("save of a plain machine = %v, want it passed through", err)
	}
}

// TestCheck checks Check reads the machine's run and answers task.Access's table, passes plain
// machines and fails closed.
func TestCheck(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	tm, err := f.sim.Get(ctx, f.tm)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := f.sim.Get(ctx, f.plain)
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range task.Ops() {
		if err := Check(ctx, f.store, f.now, plain, op); err != nil {
			t.Errorf("%s of a plain machine = %v, want nil", op, err)
		}
	}
	err = Check(ctx, f.store, f.now, tm, task.OpPause)
	if !errors.Is(err, ErrRefused) || !errors.Is(err, task.ErrRunLive) {
		t.Errorf("pause of a live run's machine = %v, want ErrRefused and ErrRunLive", err)
	}
	f.end(t)
	if err := Check(ctx, f.store, f.now, tm, task.OpPause); err != nil {
		t.Errorf("pause of a kept machine = %v, want nil", err)
	}
	tm.Metadata = map[string]string{task.Label: "t-zzzzzzzz"}
	if err := Check(ctx, f.store, f.now, tm, task.OpSetup); !errors.Is(err, ErrRefused) {
		t.Errorf("setup of a machine whose task can't be read = %v, want ErrRefused", err)
	}
}

// TestThawRefusesAStoppingRun checks a thaw rechecks the deadline under the machine's lock.
func TestThawRefusesAStoppingRun(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.owner.Pause(ctx, f.tm)
	f.owner.Resume(ctx, f.tm, time.Minute)
	f.now = t0.Add(2 * time.Hour)
	if err := f.owner.Thaw(ctx, f.run.Task, f.tm); !errors.Is(err, ErrStopping) {
		t.Fatalf("thaw past the deadline = %v, want ErrStopping", err)
	}
	if !f.sim.Frozen(f.tm) {
		t.Fatal("a thaw past the deadline let the stage run")
	}
}

func TestTimeoutFloor(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if err := f.owner.SetTimeout(ctx, f.tm, time.Second); err != nil {
		t.Fatal(err)
	}
	m, _ := f.sim.Get(ctx, f.tm)
	if want := t0.Add(time.Hour); m.EndAt.Before(want) {
		t.Fatalf("an ssh keepalive shortened a task machine's timeout to %v", m.EndAt)
	}
}
