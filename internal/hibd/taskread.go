package hibd

import (
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/links"
	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/tasks"
	"github.com/solev/hibernaut/internal/termsafe"
)

func (a *App) taskReadState(ctx context.Context, st *tasks.Tasks) (task.Instance, []hibapi.Machine, error) {
	inst, err := st.Instance(ctx)
	if err != nil {
		return "", nil, err
	}
	m, err := a.manager()
	if err != nil {
		return "", nil, err
	}
	ms, err := m.List(ctx, "")
	return inst, ms, err
}

func taskReadOf(record task.Task, inst task.Instance, ms []hibapi.Machine, full bool) hibapi.Task {
	out := hibapi.Task{ID: record.ID, Created: record.Created, Runs: []hibapi.TaskRun{}}
	runs := record.Runs
	if len(runs) > 0 {
		last := runs[len(runs)-1]
		out.Repo = runs[0].Spec.Repo.String()
		if id := last.MachineID(); id != "" {
			out.Machine = task.MachineGone
			for _, m := range ms {
				if m.ID == id {
					switch engine.State(m.State) {
					case engine.Running:
						out.Machine = task.MachineRunning
					case engine.Paused:
						out.Machine = task.MachinePaused
					}
					break
				}
			}
		}
		last.Machine = out.Machine
		if !full {
			runs = []task.Run{last}
		}
		for i, r := range runs {
			if i == len(runs)-1 {
				r = last
			}
			out.Runs = append(out.Runs, hibapi.RunOf(r, inst))
		}
	}
	if full {
		for _, q := range record.Requests {
			out.Requests = append(out.Requests, hibapi.RequestOf(q))
		}
		if r := record.Removal; r != nil {
			out.Removal = &hibapi.TaskRemoval{Why: r.Why, At: r.At}
			if !r.Done.IsZero() {
				done := r.Done
				out.Removal.Done = &done
			}
		}
	}
	return out
}

func (a *App) recordTaskRequest(ctx context.Context, c taskCommand, args []string) int {
	fl := a.flags(c.path)
	asJSON := fl.Bool("json", false, "print JSON")
	requestID := fl.String("request-id", "", "UTF-8 idempotency key of 1 to 128 bytes")
	rest, ok := a.parse(fl, args, 1, c.path+" "+c.args)
	if !ok {
		return 2
	}
	supplied := false
	fl.Visit(func(f *flag.Flag) {
		if f.Name == "request-id" {
			supplied = true
		}
	})
	id, err := task.ParseID(rest[0])
	if err != nil {
		return a.taskFail(*asJSON, c.path, *requestID, fmt.Errorf("%w: %w", errBadTaskRequest, err))
	}
	if supplied && (*requestID == "" || len(*requestID) > 128 || !utf8.ValidString(*requestID)) {
		return a.taskFail(*asJSON, c.path, *requestID, fmt.Errorf("%w: a UTF-8 request ID of 1 to 128 bytes is required", errBadTaskRequest))
	}
	if !supplied {
		*requestID = rand.Text()
	}
	var kind task.RequestKind
	switch c.path {
	case "task cancel":
		kind = task.RequestCancel
	case "task rescue":
		kind = task.RequestRescue
	case "task rm":
		kind = task.RequestRemove
	default:
		return a.taskFail(*asJSON, c.path, *requestID, fmt.Errorf("%w: unknown request %q", errBadTaskRequest, c.path))
	}
	cfg, err := loadSettings(a.Getenv)
	if err != nil {
		return a.taskFail(*asJSON, c.path, *requestID, err)
	}
	st := a.tasks(cfg)
	defer st.Close()
	user := cmp.Or(a.Getenv("SUDO_USER"), "root")
	q, err := st.Request(ctx, tasks.Request{RequestID: *requestID, Task: id, Kind: kind,
		Actor: task.Actor{Owner: user, Name: user, Source: "cli"}, At: a.now()})
	if err != nil {
		return a.taskFail(*asJSON, c.path, *requestID, err)
	}
	out := hibapi.RequestOf(q)
	if *asJSON {
		return a.reply(hibapi.Response{TaskReply: hibapi.TaskReply{RequestID: *requestID, Request: &out}})
	}
	if _, err := fmt.Fprintf(a.Stdout, "recorded %s for %s run %d\nrequest %s\n", out.Kind, id, out.Run, termsafe.Line(*requestID)); err != nil {
		return a.taskFail(false, c.path, *requestID, err)
	}
	return 0
}

func (a *App) printTaskList(records []hibapi.Task) error {
	tw := hibapi.NewTable(a.Stdout, nil)
	fmt.Fprintln(tw, "TASK\tRUN\tPHASE\tSTAGE\tREPO\tAGE")
	for _, record := range records {
		var n int
		var phase, stage string
		if len(record.Runs) > 0 {
			r := record.Runs[len(record.Runs)-1]
			n, phase, stage = r.N, cmp.Or(string(r.Outcome), string(r.Phase)), string(r.Stage)
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\n", record.ID, n, orDash(phase),
			orDash(stage), orDash(termsafe.Line(record.Repo)), shortDuration(a.now().Sub(record.Created)))
	}
	return tw.Flush()
}

func (a *App) printTask(record hibapi.Task) error {
	if _, err := fmt.Fprintf(a.Stdout, "task %s\nrepo %s\nmachine %s\n", record.ID,
		orDash(termsafe.Line(record.Repo)), orDash(string(record.Machine))); err != nil {
		return err
	}
	for _, r := range record.Runs {
		if _, err := fmt.Fprintf(a.Stdout, "run %d %s %s\n", r.N,
			cmp.Or(string(r.Outcome), string(r.Phase)), orDash(string(r.Stage))); err != nil {
			return err
		}
		if r.Held != "" {
			if _, err := fmt.Fprintln(a.Stdout, termsafe.Line(r.Held)); err != nil {
				return err
			}
		}
	}
	for _, q := range record.Requests {
		if _, err := fmt.Fprintf(a.Stdout, "request %d %s run %d by %s via %s at %s\n",
			q.ID, q.Kind, q.Run, termsafe.Line(q.Actor.Name), termsafe.Line(q.Actor.Source),
			q.At.UTC().Format(time.RFC3339)); err != nil {
			return err
		}
	}
	if r := record.Removal; r != nil {
		done := ""
		if r.Done != nil {
			done = " done at " + r.Done.UTC().Format(time.RFC3339)
		}
		_, err := fmt.Fprintf(a.Stdout, "removal %s at %s%s\n", termsafe.Line(r.Why), r.At.UTC().Format(time.RFC3339), done)
		return err
	}
	return nil
}

func (a *App) dropRemovedLink(ctx context.Context, cfg config, id, name string) {
	if !cfg.linksConfiguredOn() {
		return
	}
	unchanged := errors.New("no entry")
	err := a.linkPartsFor(cfg).store.Update(ctx, func(d *links.Doc) error {
		if _, ok := d.Machines[id]; !ok {
			return unchanged
		}
		delete(d.Machines, id)
		return nil
	})
	if err != nil && !errors.Is(err, unchanged) {
		a.warnf("removed %s, but couldn't drop its entry from the link state: %v. hibd cleanup drops it later", name, err)
	}
}
