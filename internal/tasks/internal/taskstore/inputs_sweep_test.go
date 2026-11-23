package taskstore_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/statefile"
	"github.com/solev/hibernaut/internal/task"
	"github.com/solev/hibernaut/internal/tasks/internal/taskstore"
)

func TestOpenSweepsAbandonedInputs(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	dir := statefile.Dir{Path: t.TempDir()}
	s, err := taskstore.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	abandoned := filepath.Join(dir.Path, taskstore.Files, "staging", "killed-upload")
	if err := os.WriteFile(abandoned, []byte("partial input"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		s, err := taskstore.Open(ctx, dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(abandoned); !os.IsNotExist(err) {
			t.Fatalf("abandoned staged input after Open = %v; want removed", err)
		}
	}
}

type blockedStagingInput struct {
	ctx     context.Context
	started chan struct{}
	release <-chan struct{}
	io.Reader
}

func (r *blockedStagingInput) Read(p []byte) (int, error) {
	if r.started != nil {
		close(r.started)
		r.started = nil
	}
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	case <-r.release:
		return r.Reader.Read(p)
	}
}

func TestOpenPreservesInFlightInputs(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	dir := statefile.Dir{Path: t.TempDir()}
	s, err := taskstore.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	input := io.MultiReader(strings.NewReader("first"), &blockedStagingInput{
		ctx: ctx, started: started, release: release, Reader: strings.NewReader("second"),
	})
	type submitted struct {
		receipt task.Receipt
		err     error
	}
	done := make(chan submitted, 1)
	go func() {
		rec, err := s.Submit(ctx, task.Submission{RequestID: "live-upload", At: time.Now(),
			Spec: task.Spec{Inputs: []task.File{{Name: "one", Size: 5}, {Name: "two", Size: 6}}}}, input)
		done <- submitted{rec, err}
	}()
	select {
	case <-ctx.Done():
		t.Fatal("upload did not reach its second input")
	case <-started:
	}
	other, err := taskstore.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	select {
	case <-ctx.Done():
		t.Fatal("upload did not accept its remaining input")
	case release <- struct{}{}:
	}
	var result submitted
	select {
	case <-ctx.Done():
		t.Fatal("upload did not finish after concurrent Open")
	case result = <-done:
	}
	if result.err != nil {
		t.Fatalf("in-flight Submit after concurrent Open = %v", result.err)
	}
	run, err := other.Run(ctx, result.receipt.Run)
	if err != nil || len(run.Spec.Inputs) != 2 {
		t.Fatalf("submitted run = %+v, %v; want both inputs", run, err)
	}
	for i, want := range []string{"first", "second"} {
		blob, err := other.Blob(run.Spec.Inputs[i].Hash)
		if err != nil {
			t.Fatal(err)
		}
		data, readErr := io.ReadAll(blob)
		closeErr := blob.Close()
		if readErr != nil || closeErr != nil || string(data) != want {
			t.Fatalf("input %d = %q, %v, %v; want %q", i, data, readErr, closeErr, want)
		}
	}
}

func TestOpenLeavesNonregularStagingEntries(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	dir := statefile.Dir{Path: t.TempDir()}
	s, err := taskstore.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	staging := filepath.Join(dir.Path, taskstore.Files, "staging")
	nested := filepath.Join(staging, "directory")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("leave this alone"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(staging, "symlink")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	other, err := taskstore.Open(ctx, dir)
	if err != nil {
		t.Fatalf("Open with stray staging entries = %v", err)
	}
	defer other.Close()
	if fi, err := os.Lstat(nested); err != nil || !fi.IsDir() {
		t.Errorf("staging directory = %v, %v; want preserved", fi, err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("staging symlink = %v, %v; want preserved", fi, err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "leave this alone" {
		t.Errorf("symlink target = %q, %v; want unchanged", data, err)
	}
}
