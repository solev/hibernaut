package hibapi

import (
	"cmp"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/termsafe"
)

func PrintTaskList(w io.Writer, records []Task, now time.Time) error {
	tw := NewTable(w, nil)
	fmt.Fprintln(tw, "TASK\tRUN\tPHASE\tSTAGE\tREPO\tAGE")
	for _, record := range records {
		var n int
		var phase, stage string
		if len(record.Runs) > 0 {
			r := record.Runs[len(record.Runs)-1]
			n, phase, stage = r.N, cmp.Or(string(r.Outcome), string(r.Phase)), string(r.Stage)
			if r.Detail != "" {
				phase += " (" + r.Detail + ")"
			}
			if r.Held != "" && r.Held != r.Detail {
				phase += " (" + r.Held + ")"
			}
		}
		age := int64(now.Sub(record.Created) / time.Second)
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\n", field(string(record.ID)), n,
			field(phase), field(stage), field(record.Repo), FormatIdle(&age))
	}
	return tw.Flush()
}

func PrintTask(w io.Writer, record Task) error {
	var b strings.Builder
	fmt.Fprintf(&b, "task %s\nrepo %s\nmachine %s\n", field(string(record.ID)),
		field(record.Repo), field(string(record.Machine)))
	for _, r := range record.Runs {
		fmt.Fprintf(&b, "run %d %s %s\ncommand", r.N,
			field(cmp.Or(string(r.Outcome), string(r.Phase))), field(string(r.Stage)))
		if len(r.Command) == 0 {
			b.WriteString(" -")
		}
		for _, arg := range r.Command {
			fmt.Fprintf(&b, " %q", termsafe.Line(arg))
		}
		fmt.Fprintf(&b, "\nrecipe %s\nsubmitted %s\ndeadline %s\ndetail %s\nheld %s\nstep %s\n",
			field(r.Recipe), r.Submitted.UTC().Format(time.RFC3339), taskTime(r.Deadline),
			field(r.Detail), field(r.Held), field(string(r.Step)))
		spec := task.Spec{}
		if record.Repo != "" {
			spec.Repo = task.Repo{Owner: record.Repo}
		}
		for _, s := range task.Steps() {
			fmt.Fprintf(&b, "  %s: %s", s, termsafe.Line(s.Doc()))
			if s.Skipped(task.RunID{Task: record.ID, N: r.N}, spec) {
				why := "no repo"
				if s == task.StepSetup {
					why = "follow-up or no repo"
				}
				fmt.Fprintf(&b, " (skipped: %s)", why)
			}
			b.WriteByte('\n')
		}
	}
	for _, q := range record.Requests {
		fmt.Fprintf(&b, "request %d %s run %d by %s via %s at %s\n",
			q.ID, field(string(q.Kind)), q.Run, field(q.Actor.Name), field(q.Actor.Source),
			q.At.UTC().Format(time.RFC3339))
	}
	if r := record.Removal; r != nil {
		fmt.Fprintf(&b, "removal %s at %s", field(r.Why), r.At.UTC().Format(time.RFC3339))
		if r.Done != nil {
			fmt.Fprintf(&b, " done at %s", taskTime(r.Done))
		}
		b.WriteByte('\n')
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func taskTime(at *time.Time) string {
	if at == nil {
		return "-"
	}
	return at.UTC().Format(time.RFC3339)
}

func PrintTaskRequest(w io.Writer, id task.ID, requestID string, q TaskRequest) error {
	_, err := fmt.Fprintf(w, "recorded %s for %s run %d\nrequest %s\n",
		field(string(q.Kind)), field(string(id)), q.Run, termsafe.Line(requestID))
	return err
}
