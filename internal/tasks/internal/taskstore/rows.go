package taskstore

import (
	"slices"
	"time"

	"github.com/solev/hibernaut/internal/task"
)

type taskRecord struct {
	id      task.ID
	actor   task.Actor
	created time.Time
	removal *task.Removal
}

type runRow struct {
	id        task.RunID
	spec      task.Spec
	actor     task.Actor
	submitted time.Time
	log       []task.Checkpoint
}

type taskRows struct {
	record   taskRecord
	runs     []runRow
	requests []task.Request
	freeze   task.Freeze
}

func cloneSpec(sp task.Spec) task.Spec {
	sp.Command = slices.Clone(sp.Command)
	sp.Inputs = slices.Clone(sp.Inputs)
	return sp
}

func taskView(rows taskRows) task.Task {
	t := rows.record
	out := task.Task{ID: t.id, Actor: t.actor, Created: t.created}
	if t.removal != nil {
		r := *t.removal
		out.Removal = &r
	}
	for _, q := range rows.requests {
		q.Actor.Owner = t.actor.Owner
		out.Requests = append(out.Requests, q)
	}
	kept := ""
	for _, r := range rows.runs {
		actor := r.actor
		actor.Owner = t.actor.Owner
		run := task.Run{ID: r.id, Spec: cloneSpec(r.spec), Actor: actor,
			Submitted: r.submitted, Log: slices.Clone(r.log), Freeze: rows.freeze, Kept: kept}
		for _, q := range out.Requests {
			if q.Run == r.id.N {
				run.Requests = append(run.Requests, q)
			}
		}
		kept = run.MachineID()
		out.Runs = append(out.Runs, run)
	}
	return out
}
