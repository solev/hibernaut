package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/termsafe"
)

func taskCommands() map[string]*command {
	commands := taskReadCommands()
	commands["start"] = taskStartCommand()
	for name, c := range taskFollowCommands() {
		commands[name] = c
	}
	return commands
}

func taskGroupCommand(group string, commands map[string]*command, about string, examples []string) *command {
	c := &command{name: group, about: about, examples: examples, subs: []*command{}}
	for _, row := range hibapi.TaskCommands() {
		name, ok := strings.CutPrefix(row.Path, group+" ")
		if !ok || !row.Laptop {
			continue
		}
		leaf := commands[name]
		if leaf == nil {
			panic("laptop row " + row.Path + " has no hib command")
		}
		leaf.name, leaf.summary = name, row.Summary
		c.subs = append(c.subs, leaf)
		delete(commands, name)
	}
	for name := range commands {
		panic("hib " + group + " " + name + " has no laptop row")
	}
	return c
}

func taskDocs() string {
	var b strings.Builder
	b.WriteString("\n\nphases:\n")
	for _, p := range task.Phases() {
		fmt.Fprintf(&b, "  %s\n", p)
	}
	b.WriteString("\noutcomes:\n")
	for _, o := range task.Outcomes() {
		fmt.Fprintf(&b, "  %s\n", o)
	}
	b.WriteString("\nsteps:\n")
	tw := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	for _, s := range task.Steps() {
		fmt.Fprintf(tw, "  %s\t%s\n", s, s.Doc())
	}
	tw.Flush()
	return strings.TrimSuffix(b.String(), "\n")
}

func taskJSONOnly(run runner) func(*flag.FlagSet) runner {
	return func(fl *flag.FlagSet) runner { return withTaskJSON(fl, run) }
}

func withTaskJSON(fl *flag.FlagSet, run runner) runner {
	wrapped := withJSON(fl, run)
	fl.Lookup("json").Usage = "print hibd's JSON reply unchanged"
	return wrapped
}

type taskCallOptions struct {
	path      string
	args      []string
	requestID string
	retry     []string
	input     func() (io.ReadCloser, error)
	timeout   time.Duration
}

type taskCallResult struct {
	reply     hibapi.Response
	raw       []byte
	requestID string
}

func taskRequestID(given string) (string, error) {
	if given == "" {
		return rand.Text(), nil
	}
	if len(given) > 128 || !utf8.ValidString(given) {
		return "", errors.New("a UTF-8 request ID of 1 to 128 bytes is required")
	}
	if strings.Contains(given, "'") || termsafe.Line(given) != given {
		return "", errors.New("a request ID must not contain single quotes or terminal controls")
	}
	return given, nil
}

var errNoTasks = errors.New("this host's hibd is too old for hib task and hib recipe. Update the host")

func (a *app) needTasks(ctx context.Context, cfg *config) error {
	if a.tasksReady {
		return nil
	}
	features, err := a.features(ctx, cfg)
	if err != nil {
		return err
	}
	if !slices.Contains(features, hibapi.FeatureTasks) {
		return errNoTasks
	}
	a.tasksReady = true
	return nil
}

func (a *app) taskCall(ctx context.Context, cfg *config, opts taskCallOptions) (taskCallResult, error) {
	result := taskCallResult{requestID: opts.requestID}
	var row hibapi.TaskCommand
	for _, c := range hibapi.TaskCommands() {
		if c.Path == opts.path && c.Laptop {
			row = c
			break
		}
	}
	if row.Path == "" {
		return result, fmt.Errorf("no laptop task or recipe command %q", opts.path)
	}
	if opts.timeout <= 0 {
		return result, errors.New("a task call needs a positive timeout")
	}
	if row.RequestID {
		if opts.path == "task start" && opts.requestID == "" {
			return result, errors.New("choose the task start request ID before preparing its input")
		}
		var err error
		result.requestID, err = taskRequestID(opts.requestID)
		if err != nil {
			return result, err
		}
	} else if opts.requestID != "" {
		return result, fmt.Errorf("%s doesn't take a request ID", opts.path)
	}
	if err := a.needTasks(ctx, cfg); err != nil {
		return result, err
	}
	args := append(strings.Fields(opts.path), opts.args...)
	if row.RequestID && opts.path != "task start" {
		args = append(args, "-request-id", result.requestID)
	}
	args = append(args, "-json")
	var runs requestRuns
	ssh := a.ssh
	a.ssh = runs.observe(ssh)
	defer func() { a.ssh = ssh }()
	resent := false
	var resend func() bool
	if strings.HasPrefix(opts.path, "task ") {
		resend = func() bool {
			if resent || !runs.mayHaveRun {
				return false
			}
			resent = true
			return true
		}
	}
	for busy := 0; ; busy++ {
		var raw bytes.Buffer
		reply, err := a.remoteSudo(ctx, cfg, opts.timeout,
			callOptions{input: opts.input, raw: &raw, resend: resend}, args...)
		result.reply, result.raw = reply, raw.Bytes()
		if reply.Code == hibapi.CodeBusy && busy < 3 {
			timer := time.NewTimer(200 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				err = ctx.Err()
			case <-timer.C:
				continue
			}
		}
		if err != nil && runs.mayHaveRun && result.requestID != "" && !reply.Code.RefusedBeforeSave() {
			id := "'" + strings.ReplaceAll(result.requestID, "'", "'\\''") + "'"
			if len(opts.retry) == 0 {
				err = fmt.Errorf("%w\nThe request may have been recorded. Look it up with hib task show --request %s", err, id)
			} else {
				retry := "hib " + opts.path
				for _, arg := range opts.retry {
					retry += " '" + strings.ReplaceAll(arg, "'", "'\\''") + "'"
				}
				if opts.path == "task rm" {
					retry += " --yes"
				}
				retry += " --request-id " + id
				if a.json {
					retry += " --json"
				}
				err = fmt.Errorf("%w\nThe request may have been recorded. Retry with:\n%s", err, retry)
			}
		}
		return result, err
	}
}
