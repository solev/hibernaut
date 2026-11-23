package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/task"
)

type taskStartOptions struct {
	prompt, promptFile, image, size, recipe, requestID string
	files                                              []string
	timeout                                            time.Duration
	promptSet, promptFileSet                           bool
}

func taskStartCommand() *command {
	return &command{
		args: "[-- CMD ARG...]", takesCommand: true,
		opts: "[--prompt TEXT | --prompt-file F] [--file PATH] [--image IMAGE] [--size SIZE] " +
			"[--timeout DURATION] [--recipe NAME] [--request-id ID]",
		about: "Starts CMD in its own machine and saves the task before answering. The host supervises it\n" +
			"after this laptop disconnects. Give a command after --. Recipes come in a later build.\n" +
			"--prompt and --prompt-file are alternatives. --file may repeat. Inputs total at most\n" +
			"100 MiB. --timeout is the run's time limit, not the ten-minute upload timeout.\n" +
			"Retries send the same saved inputs. If the reply is lost, look up the request ID.",
		examples: []string{"hib task start -- echo hello",
			"hib task start --prompt Review -- codex exec -",
			"hib task start --prompt-file prompt.txt --file notes.md -- codex exec -"},
		flags: taskStartFlags,
	}
}

func taskStartFlags(fl *flag.FlagSet) runner {
	var o taskStartOptions
	fl.StringVar(&o.prompt, "prompt", "", "prompt `text` to send on the command's stdin")
	fl.StringVar(&o.promptFile, "prompt-file", "", "read the prompt from `F` instead of --prompt")
	fl.Func("file", "copy an input `path` into the task machine (may repeat)", func(path string) error {
		o.files = append(o.files, path)
		return nil
	})
	fl.StringVar(&o.image, "image", "", "the `image` to make the task machine from (default dev; recipes come in a later build)")
	fl.StringVar(&o.size, "size", "", "small, medium or large (default medium; recipe `size` comes in a later build)")
	fl.DurationVar(&o.timeout, "timeout", 0, "stop the run after whole seconds, such as 1h (default 24h; recipes come in a later build)")
	fl.StringVar(&o.recipe, "recipe", "", "recipe `NAME` (recipes come in a later build)")
	fl.StringVar(&o.requestID, "request-id", "", "idempotency `ID` (default a generated ID)")
	return withTaskJSON(fl, func(ctx context.Context, a *app, args []string) int {
		fl.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "prompt":
				o.promptSet = true
			case "prompt-file":
				o.promptFileSet = true
			}
		})
		return o.run(ctx, a, args)
	})
}

func (o taskStartOptions) run(ctx context.Context, a *app, args []string) (exit int) {
	requestID, err := taskRequestID(o.requestID)
	if err != nil {
		return a.mistake(err.Error())
	}
	if o.promptSet && o.promptFileSet {
		return a.mistake("--prompt and --prompt-file are alternatives")
	}
	if o.promptFileSet && o.promptFile == "" {
		return a.mistake("--prompt-file needs a path")
	}
	if o.image != "" && !a.checkNames(o.image) {
		return 2
	}
	if o.size != "" {
		if _, err := images.SizeNamed(o.size); err != nil {
			return a.mistake(err.Error())
		}
	}
	if o.timeout < 0 || o.timeout%time.Second != 0 {
		return a.mistake("--timeout must be non-negative whole seconds")
	}
	if o.recipe != "" {
		return a.fail(errors.New("recipes come in a later build"))
	}
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		return a.fail(errors.New("give a command after --. Recipes come in a later build"))
	}
	for i, arg := range args {
		if !utf8.ValidString(arg) || strings.ContainsRune(arg, 0) || (i == 0 && arg == "") {
			return a.mistake("command needs a non-empty executable and UTF-8 arguments without NUL")
		}
	}
	names := make(map[string]bool)
	for _, path := range o.files {
		name := filepath.Base(path)
		if err := task.CheckInputName(name); err != nil {
			return a.mistake(err.Error())
		}
		if !utf8.ValidString(name) {
			return a.mistake("file names must be UTF-8")
		}
		if name == task.PromptName {
			return a.mistake(fmt.Sprintf("file %q has the prompt's name", name))
		}
		if names[name] {
			return a.mistake(fmt.Sprintf("duplicate file name %q", name))
		}
		names[name] = true
	}
	start, err := o.prepare(hibapi.TaskStart{RequestID: requestID, Command: args,
		Image: o.image, Size: o.size, TimeoutSeconds: int64(o.timeout / time.Second)})
	if err != nil {
		return a.fail(err)
	}
	cfg, err := a.settings()
	if err != nil {
		return a.fail(err)
	}
	spool, err := start.spool(ctx)
	if err != nil {
		return a.fail(err)
	}
	lookup := "Look it up with hib task show --request '" + strings.ReplaceAll(requestID, "'", "'\\''") + "'"
	defer func() {
		if err := spool.Close(); err != nil {
			exit = a.fail(fmt.Errorf("closing task inputs: %w\n%s", err, lookup))
		}
	}()
	info, err := spool.Stat()
	if err != nil {
		return a.fail(err)
	}
	result, err := a.taskCall(ctx, cfg, taskCallOptions{path: "task start",
		requestID: requestID, timeout: 10 * time.Minute,
		input: func() (io.ReadCloser, error) {
			return io.NopCloser(io.NewSectionReader(spool, 0, info.Size())), nil
		}})
	if err == nil {
		if result.reply.RequestID != requestID || result.reply.Task == nil || len(result.reply.Task.Runs) != 1 {
			return a.fail(fmt.Errorf("hibd didn't return a task receipt\n%s", lookup))
		}
		if _, checkErr := task.ParseID(string(result.reply.Task.ID)); checkErr != nil {
			return a.fail(fmt.Errorf("%w\n%s", checkErr, lookup))
		}
	}
	var remote *hibapi.RemoteError
	if a.json && (err == nil || errors.As(err, &remote)) {
		if _, writeErr := a.stdout.Write(result.raw); writeErr != nil {
			return a.fail(writeErr)
		}
		if err != nil {
			if !result.reply.Code.RefusedBeforeSave() {
				fmt.Fprintln(a.stderr, err)
			}
			return 1
		}
		return 0
	}
	if err != nil {
		return a.fail(err)
	}
	run := result.reply.Task.Runs[0]
	status := string(run.Phase)
	if status == "" {
		status = string(run.Outcome)
	}
	if _, err := fmt.Fprintf(a.stdout, "%s  %s\n", result.reply.Task.ID, status); err != nil {
		return a.fail(err)
	}
	return 0
}

type taskStartInput struct {
	name, path, text string
	size             int64
}

type preparedTaskStart struct {
	request hibapi.TaskStart
	inputs  []taskStartInput
}

func (o taskStartOptions) prepare(req hibapi.TaskStart) (preparedTaskStart, error) {
	start := preparedTaskStart{request: req}
	if o.promptSet || o.promptFileSet {
		start.inputs = append(start.inputs, taskStartInput{name: task.PromptName, path: o.promptFile, text: o.prompt})
	}
	for _, path := range o.files {
		start.inputs = append(start.inputs, taskStartInput{name: filepath.Base(path), path: path})
	}
	var total int64
	for i := range start.inputs {
		in := &start.inputs[i]
		in.size = int64(len(in.text))
		if in.path != "" {
			info, err := os.Stat(in.path)
			if err != nil {
				return preparedTaskStart{}, err
			}
			if !info.Mode().IsRegular() {
				return preparedTaskStart{}, fmt.Errorf("%s isn't a regular file", in.path)
			}
			in.size = info.Size()
		}
		if in.size > task.MaxInputBytes-total {
			return preparedTaskStart{}, errors.New("the prompt and files exceed 100 MiB")
		}
		total += in.size
		f := hibapi.TaskFile{Name: in.name, Size: in.size}
		if in.name == task.PromptName {
			start.request.Prompt = &f
		} else {
			start.request.Files = append(start.request.Files, f)
		}
	}
	return start, nil
}

func (s preparedTaskStart) spool(ctx context.Context) (_ *os.File, err error) {
	header, err := json.Marshal(s.request)
	if err != nil {
		return nil, err
	}
	if len(header) >= 1<<20 {
		return nil, errors.New("task request JSON must be less than 1 MiB")
	}
	file, err := os.CreateTemp("", "hib-task-start-*")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, file.Close())
		}
	}()
	if err := os.Remove(file.Name()); err != nil {
		return nil, err
	}
	if _, err := file.Write(append(header, '\n')); err != nil {
		return nil, err
	}
	for _, in := range s.inputs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if in.path == "" {
			if _, err := io.WriteString(file, in.text); err != nil {
				return nil, err
			}
		} else if err := in.copy(ctx, file); err != nil {
			return nil, err
		}
	}
	return file, nil
}

func (in taskStartInput) copy(ctx context.Context, dst io.Writer) (err error) {
	file, err := os.OpenFile(in.path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != in.size {
		return fmt.Errorf("%s size changed before it was read", in.path)
	}
	if _, err := io.CopyN(dst, taskStartReader{ctx, file}, in.size); err != nil {
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%s size changed while it was read", in.path)
		}
		return fmt.Errorf("reading %s: %w", in.path, err)
	}
	var extra [1]byte
	n, readErr := file.Read(extra[:])
	info, err = file.Stat()
	if err != nil {
		return err
	}
	if n != 0 || info.Size() != in.size {
		return fmt.Errorf("%s size changed while it was read", in.path)
	}
	if readErr != io.EOF {
		return fmt.Errorf("reading %s: %w", in.path, readErr)
	}
	return nil
}

type taskStartReader struct {
	ctx context.Context
	io.Reader
}

func (r taskStartReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(b)
}
