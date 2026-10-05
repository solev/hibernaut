package main

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

var liveStart = time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)

func newTestLine(text string) liveLine {
	return liveLine{text: text, since: liveStart, now: liveStart, clock: func() time.Time { return liveStart }}
}

func update(t *testing.T, l liveLine, msgs ...tea.Msg) liveLine {
	t.Helper()
	for _, msg := range msgs {
		next, _ := l.Update(msg)
		l = next.(liveLine)
	}
	return l
}

func TestLiveLineShowsWhatAndWholeSeconds(t *testing.T) {
	l := update(t, newTestLine("making app"), liveTick(liveStart.Add(12700*time.Millisecond)))
	if got := l.View().Content; got != "making app (12s)" {
		t.Errorf("view %q, want making app (12s)", got)
	}
	l = update(t, l, liveTick(liveStart.Add(-time.Second)))
	if got := l.View().Content; got != "making app (0s)" {
		t.Errorf("view with the clock behind %q, want 0s", got)
	}
}

func TestLiveLineRestartsItsTimeForEachWait(t *testing.T) {
	l := update(t, newTestLine("making app"),
		liveTick(liveStart.Add(5*time.Second)),
		liveWait{text: "waking the host", since: liveStart.Add(5 * time.Second)},
	)
	if got := l.View().Content; got != "waking the host (0s)" {
		t.Errorf("view %q, want waking the host (0s)", got)
	}
	l = update(t, l, liveTick(liveStart.Add(45*time.Second)))
	if got := l.View().Content; got != "waking the host (40s)" {
		t.Errorf("view %q, want waking the host (40s)", got)
	}
}

func TestLiveLineFitsTheTerminalAndKeepsItsTime(t *testing.T) {
	l := update(t, newTestLine("dev (small)  4/12  RUN npm i -g @openai/codex and much more after it"),
		tea.WindowSizeMsg{Width: 40, Height: 24},
		liveTick(liveStart.Add(41*time.Second)),
	)
	got := l.View().Content
	if !strings.HasPrefix(got, "dev (small)  4/12  RUN") || !strings.HasSuffix(got, "… (41s)") || len([]rune(got)) != 39 {
		t.Errorf("view %q (%d columns), want it shortened to 39 columns with its time", got, len([]rune(got)))
	}
}

func TestLiveLineLeavesTheTerminalAlone(t *testing.T) {
	v := newTestLine("making app").View()
	if v.Cursor != nil || !v.DisableBracketedPasteMode || v.AltScreen || v.MouseMode != tea.MouseModeNone ||
		v.ReportFocus || v.WindowTitle != "" {
		t.Errorf("view %+v changes more of the terminal than the line", v)
	}
}

func TestLiveLineClearsAndQuitsWhenDone(t *testing.T) {
	next, cmd := newTestLine("making app").Update(liveDone{})
	if got := next.(liveLine).View().Content; got != "" {
		t.Errorf("view after done %q, want it empty", got)
	}
	if cmd == nil {
		t.Fatal("done didn't quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("done returned %T, want tea.QuitMsg", cmd())
	}
	if _, cmd := next.Update(liveTick(liveStart)); cmd != nil {
		t.Error("a tick after done asked for another tick")
	}
}

func newLiveApp(t *testing.T, term *terminal) *app {
	return &app{
		stderr:         term,
		stderrTerminal: true,
		getenv:         func(k string) string { return map[string]string{"TERM": "xterm-256color"}[k] },
		now:            func() time.Time { return liveStart },
	}
}

func checkLines(t *testing.T, term *terminal, want ...string) {
	t.Helper()
	if got := term.lines(); !slices.Equal(got, want) {
		t.Errorf("the terminal shows %q, want %q. It got %q", got, want, term.String())
	}
	if term.cursorHidden() {
		t.Errorf("the live line left the cursor hidden: %q", term.String())
	}
}

func TestLiveLinePrintsLinesAboveItself(t *testing.T) {
	term := newTerminal(t)
	a := newLiveApp(t, term)
	stop := a.showLive(t.Context(), "making app")
	term.waitFor("making app (0s)")
	fmt.Fprintln(a.stderr, "hibd: one")
	fmt.Fprint(a.stderr, "hibd: t")
	term.waitFor("making app (0s)")
	fmt.Fprint(a.stderr, "wo\r\nhibd: three\n")
	restore := a.live.waitFor("waking the host")
	term.waitFor("waking the host (0s)")
	restore()
	term.waitFor("making app (0s)")
	fmt.Fprint(a.stderr, "hibd: no newline")
	stop()
	fmt.Fprintln(a.stderr, "")
	fmt.Fprintln(a.stderr, "hib: made app")
	if a.stderr != term || a.live != nil {
		t.Error("stop didn't give stderr back")
	}
	checkLines(t, term, "hibd: one", "hibd: two", "hibd: three", "hibd: no newline", "hib: made app")
}

func TestLiveLineWaitsBeforeShowing(t *testing.T) {
	term := newTerminal(t)
	a := newLiveApp(t, term)
	a.liveAfter = time.Hour
	stop := a.showLive(t.Context(), "making app")
	fmt.Fprint(a.stderr, "hibd: one\nhibd: t")
	fmt.Fprint(a.stderr, "wo\n")
	stop()
	if got := term.String(); got != "hibd: one\nhibd: two\n" {
		t.Errorf("stderr %q, want hibd's lines as they were", got)
	}
}

func TestLiveLineClearsWhenCtrlCStopsIt(t *testing.T) {
	term := newTerminal(t)
	a := newLiveApp(t, term)
	ctx, cancel := context.WithCancelCause(t.Context())
	stop := a.showLive(ctx, "making app")
	term.waitFor("making app (0s)")
	cancel(stopped{syscall.SIGINT})
	fmt.Fprintln(a.stderr, "ssh: killed")
	stop()
	fmt.Fprintln(a.stderr, "hib: stopped waiting")
	checkLines(t, term, "ssh: killed", "hib: stopped waiting")
}

func TestNoLiveLineWithoutATerminal(t *testing.T) {
	for _, tc := range []struct {
		name     string
		terminal bool
		json     bool
		term     string
	}{
		{name: "pipe", term: "xterm-256color"},
		{name: "json", terminal: true, json: true, term: "xterm-256color"},
		{name: "dumb", terminal: true, term: "dumb"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			term := newTerminal(t)
			a := newLiveApp(t, term)
			a.stderrTerminal, a.json = tc.terminal, tc.json
			a.getenv = func(string) string { return tc.term }
			stop := a.showLive(t.Context(), "making app")
			fmt.Fprint(a.stderr, "hibd: one\n")
			if a.live != nil || a.stderr != term {
				t.Error("showLive started a live line")
			}
			stop()
			if got := term.String(); got != "hibd: one\n" {
				t.Errorf("stderr %q, want only hibd's line", got)
			}
		})
	}
}

func liveClient(t *testing.T, h *host, termName string) (*client, *terminal) {
	t.Helper()
	c := initClient(t, h)
	term := newTerminal(t)
	getenv := c.getenv
	c.stderr, c.stderrTerminal = term, true
	c.getenv = func(k string) string {
		if k == "TERM" {
			return termName
		}
		return getenv(k)
	}
	return c, term
}

func TestNewShowsWhatItWaitsFor(t *testing.T) {
	h := newHost(t)
	c, term := liveClient(t, h, "xterm-256color")
	h.override = func(words []string, _, stderr io.Writer) (int, bool) {
		if words[0] == "new" {
			term.waitFor("making app (")
			fmt.Fprintln(stderr, "hibd: a line from the host")
		}
		return 0, false
	}
	if code := c.hib("new", "app"); code != 0 {
		t.Fatalf("new exit %d: %q", code, term)
	}
	if want := "made app; connect with: ssh app.hib\n"; c.out.String() != want {
		t.Errorf("new printed %q, want %q", c.out, want)
	}
	checkLines(t, term, "hibd: a line from the host")
	calls := h.takeCalls()
	if len(calls) != 1 || !slices.Contains(calls[0], "BatchMode=yes") {
		t.Errorf("new ran ssh %q, want one call with BatchMode=yes so no prompt hides under the live line", calls)
	}
}

func TestNewShowsTheHostWaking(t *testing.T) {
	h := newHost(t)
	c, term := liveClient(t, h, "xterm-256color")
	h.sleep("VM deallocated")
	c.az = func(ctx context.Context, args ...string) ([]byte, error) {
		if args[1] == "start" {
			term.waitFor("waking the host (")
		}
		return h.az(ctx, args...)
	}
	h.override = func(words []string, _, _ io.Writer) (int, bool) {
		if words[0] == "new" {
			term.waitFor("making app (")
		}
		return 0, false
	}
	if code := c.hib("new", "app"); code != 0 {
		t.Fatalf("new exit %d: %q", code, term)
	}
	checkLines(t, term,
		"ssh: connect to host 203.0.113.7 port 22: Operation timed out",
		"hib: the host is asleep (VM deallocated); starting it, which takes a few minutes",
		"hib: the host is up after 0s")
}

func TestCtrlCClearsTheLiveLine(t *testing.T) {
	h := newHost(t)
	c, term := liveClient(t, h, "xterm-256color")
	ctx, cancel := context.WithCancelCause(t.Context())
	h.override = func(words []string, _, _ io.Writer) (int, bool) {
		term.waitFor("making app (")
		cancel(stopped{syscall.SIGINT})
		return 255, true
	}
	if code := c.hibContext(ctx, "new", "app"); code != 130 {
		t.Errorf("new exit %d, want 130", code)
	}
	checkLines(t, term, "hib: stopped waiting. The host may still be making app: check hib ls")
}

func TestNewShowsNoLiveLineWithoutATerminal(t *testing.T) {
	for _, tc := range []struct {
		name     string
		terminal bool
		term     string
		args     []string
	}{
		{name: "pipe", term: "xterm-256color", args: []string{"new", "app"}},
		{name: "json", terminal: true, term: "xterm-256color", args: []string{"new", "app", "--json"}},
		{name: "dumb", terminal: true, term: "dumb", args: []string{"new", "app"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHost(t)
			c, term := liveClient(t, h, tc.term)
			c.stderrTerminal = tc.terminal
			h.sleep("VM deallocated")
			h.override = func(_ []string, _, stderr io.Writer) (int, bool) {
				fmt.Fprintln(stderr, "hibd: a line from the host")
				return 0, false
			}
			if code := c.hib(tc.args...); code != 0 {
				t.Fatalf("new exit %d: %q", code, term)
			}
			if got := term.String(); strings.Contains(got, "\x1b") || strings.Contains(got, "making app") {
				t.Errorf("stderr %q, want no live line", got)
			}
		})
	}
}
