package main

import (
	"context"
	"io"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/solev/hibernaut/internal/engine"
)

const deleteQuestion = "Delete app and its disk for good? [y/N] "

func machineNames(t *testing.T, h *host) []string {
	t.Helper()
	ms, err := h.engine.List(t.Context(), engine.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, m := range ms {
		names = append(names, m.Metadata["hib.name"])
	}
	return names
}

func TestRmNeedsYesWithoutATerminal(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	if code := c.hib("new", "app"); code != 0 {
		t.Fatalf("new app = %d: %s", code, c.errOut)
	}
	h.takeCalls()
	want := "hib rm: deleting app can't be undone, so pass --yes when there's no terminal to ask on\n" +
		"usage: hib rm NAME [--yes]\nrun hib help rm\n"
	if code := c.hib("rm", "app"); code != 2 || c.out.Len() > 0 || c.errOut.String() != want {
		t.Errorf("rm app without a terminal = %d, stdout %q, stderr:\n%s\nwant 2 and:\n%s", code, c.out, c.errOut, want)
	}
	if calls := h.takeCalls(); len(calls) != 0 {
		t.Errorf("rm without --yes reached the host: %q", calls)
	}
	if code := c.hib("rm", "app", "--yes"); code != 0 || c.out.String() != "removed app\n" || c.errOut.Len() > 0 {
		t.Errorf("rm app --yes = %d, stdout %q, stderr %q", code, c.out, c.errOut)
	}
	if names := machineNames(t, h); len(names) != 0 {
		t.Errorf("machines after rm --yes: %q", names)
	}
}

func TestRmAsksOnATerminal(t *testing.T) {
	for _, tc := range []struct {
		answer  string
		deletes bool
		stderr  string
	}{
		{"y\n", true, deleteQuestion},
		{"Y\n", true, deleteQuestion},
		{"yes\n", true, deleteQuestion},
		{"YeS\r\n", true, deleteQuestion},
		{" yes \n", true, deleteQuestion},
		{"n\n", false, deleteQuestion + "kept app\n"},
		{"\n", false, deleteQuestion + "kept app\n"},
		{"no\n", false, deleteQuestion + "kept app\n"},
		{"yes please\n", false, deleteQuestion + "kept app\n"},
		{"yy\n", false, deleteQuestion + "kept app\n"},
		{"", false, deleteQuestion + "\nkept app\n"},
		{"y", false, deleteQuestion + "\nkept app\n"},
		{strings.Repeat("y", 2000) + "\n", false, deleteQuestion + "\nkept app\n"},
	} {
		h := newHost(t)
		c := initClient(t, h)
		c.interactive = true
		if code := c.hib("new", "app"); code != 0 {
			t.Fatalf("new app = %d: %s", code, c.errOut)
		}
		h.takeCalls()
		c.stdin = strings.NewReader(tc.answer)
		code := c.hib("rm", "app")
		names := machineNames(t, h)
		calls := remoteCommands(h.takeCalls())
		switch {
		case tc.deletes && (code != 0 || c.out.String() != "removed app\n" || len(names) != 0 || !slices.Equal(calls, []string{"sudo -n hibd 'rm' 'app' '--json'"})):
			t.Errorf("answer %q: rm = %d, stdout %q, machines %q, calls %q; want app deleted", tc.answer, code, c.out, names, calls)
		case !tc.deletes && (code != 1 || c.out.Len() > 0 || len(names) != 1 || len(calls) != 0):
			t.Errorf("answer %q: rm = %d, stdout %q, machines %q, calls %q; want app kept", tc.answer, code, c.out, names, calls)
		}
		if c.errOut.String() != tc.stderr {
			t.Errorf("answer %q: stderr %q, want %q", tc.answer, c.errOut, tc.stderr)
		}
	}
}

type interruptingReader struct {
	cancel  context.CancelCauseFunc
	release chan struct{}
}

func (r interruptingReader) Read([]byte) (int, error) {
	r.cancel(stopped{syscall.SIGINT})
	<-r.release
	return 0, io.EOF
}

func TestCtrlCKeepsTheMachine(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	c.interactive = true
	if code := c.hib("new", "app"); code != 0 {
		t.Fatalf("new app = %d: %s", code, c.errOut)
	}
	h.takeCalls()
	ctx, cancel := context.WithCancelCause(t.Context())
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	c.stdin = interruptingReader{cancel, release}
	if code := c.hibContext(ctx, "rm", "app"); code != 130 || c.out.Len() > 0 || c.errOut.String() != deleteQuestion+"\nkept app\n" {
		t.Errorf("rm app with Ctrl-C = %d, stdout %q, stderr %q; want 130 and kept app", code, c.out, c.errOut)
	}
	if calls := h.takeCalls(); len(calls) != 0 {
		t.Errorf("rm with Ctrl-C reached the host: %q", calls)
	}
	if names := machineNames(t, h); len(names) != 1 {
		t.Errorf("machines after Ctrl-C: %q", names)
	}
}

func TestYesSkipsTheQuestion(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	c.interactive = true
	if code := c.hib("new", "app"); code != 0 {
		t.Fatalf("new app = %d: %s", code, c.errOut)
	}
	c.stdin = strings.NewReader("n\n")
	if code := c.hib("rm", "--yes", "app"); code != 0 || c.out.String() != "removed app\n" || c.errOut.Len() > 0 {
		t.Errorf("rm --yes app on a terminal = %d, stdout %q, stderr %q", code, c.out, c.errOut)
	}
}
