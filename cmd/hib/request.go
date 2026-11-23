package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
)

type requestRuns struct{ mayHaveRun bool }

func (r *requestRuns) observe(ssh runFunc) runFunc {
	return func(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer) (int, error) {
		output := requestOutput{Writer: out}
		code, err := ssh(ctx, args, in, &output, stderr)
		if !output.wrote {
			var lookup *exec.Error
			var start *os.PathError
			if errors.As(err, &lookup) || errors.As(err, &start) && start.Op == "fork/exec" {
				return code, err
			}
			// JSON task and recipe failures write a reply; an empty sudo exit 1 precedes hibd.
			if err == nil && (code == 1 || code == 127) {
				return code, err
			}
		}
		r.mayHaveRun = true
		return code, err
	}
}

type requestOutput struct {
	io.Writer
	wrote bool
}

func (w *requestOutput) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	w.wrote = w.wrote || n > 0
	return n, err
}
