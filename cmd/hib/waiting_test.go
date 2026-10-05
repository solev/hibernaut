package main

import (
	"context"
	"io"
	"slices"
	"strings"
	"syscall"
	"testing"
)

func interruptOn(h *host, cancel context.CancelCauseFunc, command ...string) {
	h.override = func(words []string, _, _ io.Writer) (int, bool) {
		if len(words) >= 3 && slices.Equal(words[:3], []string{"sudo", "-n", "hibd"}) {
			words = words[3:]
		}
		if len(words) < len(command) || !slices.Equal(words[:len(command)], command) {
			return 0, false
		}
		cancel(stopped{syscall.SIGINT})
		return 255, true
	}
}

func TestCtrlCSaysWhatMayBeLeftBehind(t *testing.T) {
	for _, tc := range []struct {
		args, command []string
		want          string
	}{
		{[]string{"new", "app"}, []string{"new"}, "The host may still be making app: check hib ls"},
		{[]string{"fork", "app"}, []string{"fork"}, "The host may still be making copies of app: check hib ls"},
		{[]string{"rm", "app", "--yes"}, []string{"rm"}, "The host may still be deleting app: check hib ls"},
		{[]string{"pause", "app"}, []string{"pause"}, "The host may still be pausing app: check hib ls"},
		{[]string{"image", "build", "dev"}, []string{"image", "build"},
			"The host may still finish building dev: check hib image ls in a few minutes"},
		{[]string{"image", "save", "app", "myimg"}, []string{"image", "save"},
			"The host may still be saving app as myimg: check hib image ls in a few minutes"},
		{[]string{"image", "rm", "myimg"}, []string{"image", "rm"}, "The host may still be removing myimg: check hib image ls"},
		{[]string{"secret", "add", "openai", "--env", "OPENAI_API_KEY", "--bind", "api.openai.com"}, []string{"secret", "add"},
			"The host may still be storing openai: check hib secret ls"},
		{[]string{"secret", "rm", "openai"}, []string{"secret", "rm"}, "The host may still be removing openai: check hib secret ls"},
	} {
		for _, azure := range []bool{true, false} {
			h := newHost(t)
			c := initClient(t, h, !azure)
			c.stdin = strings.NewReader("value")
			ctx, cancel := context.WithCancelCause(t.Context())
			interruptOn(h, cancel, tc.command...)
			want := "hib: stopped waiting. " + tc.want + "\n"
			if code := c.hibContext(ctx, tc.args...); code != 130 || c.out.Len() > 0 || c.errOut.String() != want {
				t.Errorf("hib %q stopped (Azure %v) = %d, stdout %q, stderr:\n%s\nwant 130 and:\n%s", tc.args, azure, code, c.out, c.errOut, want)
			}
		}
	}
}

func TestCtrlCWhileReadingSaysOnlyThatHibStopped(t *testing.T) {
	for _, args := range [][]string{{"ls"}, {"secret", "ls"}, {"log"}, {"image", "ls"}} {
		h := newHost(t)
		c := initClient(t, h, true)
		ctx, cancel := context.WithCancelCause(t.Context())
		interruptOn(h, cancel, args...)
		if code := c.hibContext(ctx, args...); code != 130 || c.out.Len() > 0 || c.errOut.String() != "hib: stopped waiting\n" {
			t.Errorf("hib %q stopped = %d, stdout %q, stderr %q", args, code, c.out, c.errOut)
		}
	}
}

func TestCtrlCWhileWakingSaysTheHostKeepsStarting(t *testing.T) {
	for _, args := range [][]string{{"new", "app"}, {"ls"}, {"image", "build", "dev"}} {
		h := newHost(t)
		c := initClient(t, h)
		h.sleep("VM deallocated")
		ctx, cancel := context.WithCancelCause(t.Context())
		h.sshInput = func(_ context.Context, args []string, _ io.Reader) {
			if slices.Contains(args, "ready") {
				cancel(stopped{syscall.SIGINT})
			}
		}
		code := c.hibContext(ctx, args...)
		lines := strings.Split(strings.TrimSpace(c.errOut.String()), "\n")
		if want := "hib: stopped waiting. The host keeps starting: check hib status in a few minutes"; code != 130 || lines[len(lines)-1] != want {
			t.Errorf("hib %q stopped while waking = %d, stderr:\n%s\nwant 130, ending with %q", args, code, c.errOut, want)
		}
	}
}

func TestFailuresWithoutASignalKeepTheirMessage(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h, true)
	ctx, cancel := context.WithCancelCause(t.Context())
	h.override = func(words []string, _, _ io.Writer) (int, bool) {
		cancel(context.Canceled)
		return 255, true
	}
	if code := c.hibContext(ctx, "new", "app"); code != 1 || !strings.Contains(c.errOut.String(), "couldn't reach "+testHost) {
		t.Errorf("new app cancelled without a signal = %d, stderr %q", code, c.errOut)
	}
}
