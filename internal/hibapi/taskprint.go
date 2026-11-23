package hibapi

import (
	"cmp"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/clipperhouse/displaywidth"

	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/termsafe"
)

func PrintTaskList(w io.Writer, records []Task, now time.Time) error {
	var table strings.Builder
	tw := NewTable(&table, nil)
	fmt.Fprintln(tw, "TASK\tRUN\tPHASE\tSTAGE\tREPO\tAGE")
	reasons := make([][]string, len(records))
	for i, record := range records {
		var n int
		var phase, stage string
		if len(record.Runs) > 0 {
			r := record.Runs[len(record.Runs)-1]
			n, phase, stage = r.N, cmp.Or(string(r.Outcome), string(r.Phase)), string(r.Stage)
			if r.Detail != "" {
				reasons[i] = append(reasons[i], r.Detail)
			}
			if r.Held != "" && r.Held != r.Detail {
				reasons[i] = append(reasons[i], r.Held)
			}
		}
		age := int64(now.Sub(record.Created) / time.Second)
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\n", field(string(record.ID)), n,
			field(phase), field(stage), field(record.Repo), FormatIdle(&age))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	var out strings.Builder
	for i, row := range strings.Split(strings.TrimSuffix(table.String(), "\n"), "\n") {
		fmt.Fprintln(&out, row)
		if i > 0 {
			for _, reason := range reasons[i-1] {
				printTaskReason(&out, reason)
			}
		}
	}
	_, err := io.WriteString(w, out.String())
	return err
}

func printTaskReason(out *strings.Builder, reason string) {
	words := strings.Fields(field(reason))
	line := ""
	for i := 0; i < len(words); i++ {
		word := words[i]
		if word == "hib" && i+3 < len(words) && words[i+1] == "task" {
			word = strings.Join(words[i:i+4], " ")
			i += 3
		}
		word = taskReasonWord(word)
		if line != "" && displaywidth.String(line)+1+displaywidth.String(word) > 78 {
			fmt.Fprintln(out, "  "+line)
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += word
	}
	if line != "" {
		fmt.Fprintln(out, "  "+line)
	}
}

func taskReasonWord(word string) string {
	if displaywidth.String(word) <= 78 {
		return word
	}
	end, columns := 0, 0
	for end < len(word) {
		_, size := utf8.DecodeRuneInString(word[end:])
		if word[end] == '\\' && end+1 < len(word) {
			switch word[end+1] {
			case 'x':
				size = min(4, len(word)-end)
			case 'u':
				size = min(6, len(word)-end)
			case 'U':
				size = min(10, len(word)-end)
			}
		}
		width := displaywidth.String(word[end : end+size])
		if columns+width > 75 {
			break
		}
		columns += width
		end += size
	}
	return word[:end] + "..."
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
		for _, unread := range r.Unread {
			fmt.Fprintf(&b, "unread %s\n", field(unread))
		}
		spec := task.Spec{}
		if record.Repo != "" {
			spec.Repo = task.Repo{Owner: record.Repo}
		}
		for _, s := range task.Steps() {
			fmt.Fprintf(&b, "  %s: %s", s, termsafe.Line(s.Doc()))
			if s.Skipped(task.RunID{Task: record.ID, N: r.N}, spec) {
				fmt.Fprintf(&b, " (skipped: %s)", field(s.SkipReason()))
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
