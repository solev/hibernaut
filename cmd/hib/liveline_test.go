package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
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
	stop := a.showLive(t.Context(), "making app", makingFace)
	term.waitFor("making app (0s)")
	fmt.Fprintln(a.stderr, "hibd: one")
	fmt.Fprint(a.stderr, "hibd: t")
	term.waitFor("making app (0s)")
	fmt.Fprint(a.stderr, "wo\r\nhibd: three\n")
	restore := a.live.waitFor("waking the host", wakingFace)
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
	stop := a.showLive(t.Context(), "making app", makingFace)
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
	stop := a.showLive(ctx, "making app", makingFace)
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
			stop := a.showLive(t.Context(), "making app", makingFace)
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

const longWait = "making app from the image called a-rather-long-image-name, on a host that has never seen it before"

type fakeResizes struct {
	signals chan os.Signal
	stopped chan struct{}
}

func watchFakeResizes(a *app) *fakeResizes {
	r := &fakeResizes{signals: make(chan os.Signal), stopped: make(chan struct{})}
	a.resizes = func() (<-chan os.Signal, func()) {
		return r.signals, func() { close(r.stopped) }
	}
	return r
}

func (r *fakeResizes) checkUnwatched(t *testing.T) {
	t.Helper()
	select {
	case <-r.stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("the live line never stopped watching for resizes")
	}
	select {
	case r.signals <- syscall.SIGWINCH:
		t.Error("a goroutine still waits for resizes")
	default:
	}
}

func TestLiveLineFitsTheTerminalsWidth(t *testing.T) {
	term := newSizedTerminal(t, 40, 24)
	a := newLiveApp(t, term)
	a.stderrSize = func() (int, int, error) { return 40, 24, nil }
	stop := a.showLive(t.Context(), longWait, makingFace)
	term.waitFor("making app from the image called … (0s)")
	if strings.Contains(term.String(), "a-rather") {
		t.Errorf("a frame was wider than the terminal's 40 columns: %q", term)
	}
	stop()
	checkLines(t, term)
}

func TestLiveLineFollowsResizes(t *testing.T) {
	term := newSizedTerminal(t, 40, 24)
	a := newLiveApp(t, term)
	var width atomic.Int64
	width.Store(40)
	a.stderrSize = func() (int, int, error) { return int(width.Load()), 24, nil }
	r := watchFakeResizes(a)
	stop := a.showLive(t.Context(), longWait, makingFace)
	term.waitFor("making app from the image called … (0s)")
	width.Store(30)
	r.signals <- syscall.SIGWINCH
	term.waitFor("making app from the ima… (0s)")
	if got := term.lines(); len(got) != 1 || got[0] != "making app from the ima… (0s)" {
		t.Errorf("after the resize the terminal shows %q, want the line shortened to 29 columns. It got %q", got, term)
	}
	stop()
	r.checkUnwatched(t)
	checkLines(t, term)
}

func TestLiveLineAssumes80ColumnsWithoutASize(t *testing.T) {
	term := newTerminal(t)
	a := newLiveApp(t, term)
	a.stderrSize = func() (int, int, error) { return 0, 0, errors.New("inappropriate ioctl for device") }
	stop := a.showLive(t.Context(), longWait, makingFace)
	term.waitFor("making app from the image called a-rather-long-image-name, on a host that… (0s)")
	stop()
	checkLines(t, term)
}

func TestLiveLineStopsWatchingResizesWhenItEnds(t *testing.T) {
	term := newTerminal(t)
	a := newLiveApp(t, term)
	r := watchFakeResizes(a)
	ctx, cancel := context.WithCancelCause(t.Context())
	stop := a.showLive(ctx, "making app", makingFace)
	term.waitFor("making app (0s)")
	cancel(stopped{syscall.SIGINT})
	r.checkUnwatched(t)
	stop()
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

func newFaceLine(text string, kind faceKind, script faceScript) liveLine {
	l := newTestLine(text)
	l.face = newLiveFace(&faces{kind: kind, dim: true, cell: cellSize{9, 19}})
	l.face.play(script, liveStart)
	return l
}

// faceTicks moves l's face on from one time to another, a tick every 50ms, as Bubble Tea would.
func faceTicks(t *testing.T, l liveLine, from, to time.Duration) liveLine {
	t.Helper()
	for at := from; at <= to; at += faceEvery {
		l = update(t, l, faceTick(liveStart.Add(at)))
	}
	return l
}

const (
	asleepFace = "\x1b[2m▬▬\x1b[22m "
	awakeFace  = "▪▪ "
)

func checkView(t *testing.T, l liveLine, want string) {
	t.Helper()
	if got := l.View().Content; got != want {
		t.Errorf("view %q, want %q", got, want)
	}
}

func TestLiveFaceWakesWhenItsScriptSays(t *testing.T) {
	l := newFaceLine("making app", textFaces, makingFace)
	checkView(t, l, asleepFace+"making app (0s)")
	l = faceTicks(t, l, 0, 1500*time.Millisecond)
	checkView(t, l, asleepFace+"making app (0s)")
	l = faceTicks(t, l, 1550*time.Millisecond, 2400*time.Millisecond)
	checkView(t, l, awakeFace+"making app (0s)")
}

func TestLiveFacePlaysAWaitsScriptAndThenItsOwnAgain(t *testing.T) {
	l := newFaceLine("making app", textFaces, makingFace)
	l = faceTicks(t, l, 0, time.Second)
	l = update(t, l, liveWait{text: "waking the host", since: liveStart.Add(time.Second), script: &wakingFace})
	l = faceTicks(t, l, 1050*time.Millisecond, 4*time.Second)
	checkView(t, l, asleepFace+"waking the host (0s)")
	l = update(t, l, liveWait{text: "making app", since: liveStart.Add(4 * time.Second), script: &makingFace})
	l = faceTicks(t, l, 4050*time.Millisecond, 5500*time.Millisecond)
	checkView(t, l, asleepFace+"making app (0s)")
	l = faceTicks(t, l, 5550*time.Millisecond, 6400*time.Millisecond)
	checkView(t, l, awakeFace+"making app (0s)")
}

func TestLiveFaceKeepsMovingWhenOnlyTheTextChanges(t *testing.T) {
	term := newTerminal(t)
	a := newLiveApp(t, term)
	a.faces = &faces{kind: textFaces, dim: true}
	var clock liveClock
	a.now = clock.now
	stop := a.showLive(t.Context(), "making app", makingFace)
	term.waitFor("▬▬ making app")
	clock.moveTo(time.Second)
	a.live.set("making app, still")
	// Had set started the script again, the face would wake at 2.6s.
	clock.moveTo(2400 * time.Millisecond)
	term.waitFor("▪▪ making app, still")
	stop()
	checkLines(t, term)
}

func TestLiveFaceLeavesRoomForItself(t *testing.T) {
	for _, tc := range []struct {
		width int
		want  string
	}{
		{80, asleepFace + "making app (0s)"},
		{12, asleepFace + "ma… (0s)"},
		{9, "ma… (0s)"},
	} {
		l := update(t, newFaceLine("making app", textFaces, makingFace), tea.WindowSizeMsg{Width: tc.width, Height: 24})
		checkView(t, l, tc.want)
	}
}

func TestLiveFaceSendsOnlyNewFrames(t *testing.T) {
	l := newFaceLine("making app", imageFaces, makingFace)
	first := l.face.frame()
	chunks, rest := splitKitty(t, first)
	want := fmt.Sprintf("a=T,U=1,f=100,i=%d,p=1,c=2,r=1,q=2,m=0", l.face.id)
	if len(chunks) != 1 || rest != "" || chunks[0].keys != want {
		t.Fatalf("the first frame is %+v and %q, want one command with keys %q", chunks, rest, want)
	}
	if frame := l.face.frame(); frame != "" {
		t.Errorf("a face that didn't move sent a frame again: %.40q", frame)
	}
	l.face.move(liveStart.Add(2 * time.Second))
	if frame := l.face.frame(); frame == "" {
		t.Error("a waking face sent no frame")
	}
	if got, want := l.View().Content, fmt.Sprintf("\x1b[38;2;%d;%d;%dm\U0010EEEE\u0305\u0305\U0010EEEE\u0305\u030D\x1b[39m making app (0s)",
		byte(l.face.id>>16), byte(l.face.id>>8), byte(l.face.id)); got != want {
		t.Errorf("view %q, want %q", got, want)
	}
}

// liveClock is a clock for a live line that a test moves on.
type liveClock struct{ offset atomic.Int64 }

func (c *liveClock) now() time.Time              { return liveStart.Add(time.Duration(c.offset.Load())) }
func (c *liveClock) moveTo(offset time.Duration) { c.offset.Store(int64(offset)) }

func TestLiveLineDrawsItsImageFaceAndDeletesIt(t *testing.T) {
	term := newTerminal(t)
	a := newLiveApp(t, term)
	a.faces = &faces{kind: imageFaces, cell: cellSize{64, 128}}
	var clock liveClock
	a.now = clock.now
	stop := a.showLive(t.Context(), "making app", makingFace)
	term.waitFor("making app (0s)")
	row, graphics := term.state()
	id := imageID(t, graphics)
	colour := fmt.Sprintf("38;2;%d;%d;%d", byte(id>>16), byte(id>>8), byte(id))
	if row[0].r != '\U0010EEEE' || row[0].marks != "\u0305\u0305" || row[0].fg != colour ||
		row[1].r != '\U0010EEEE' || row[1].marks != "\u0305\u030D" || row[1].fg != colour || row[2].fg != "" {
		t.Errorf("the line starts %+v, want two placeholders coloured %s", row[:3], colour)
	}
	image := checkFrame(t, graphics, id)
	if b := image.Bounds(); b.Dx() != 128 || b.Dy() != 128 {
		t.Errorf("the frame is %dx%d, want 128x128 to fill two 64x128 cells", b.Dx(), b.Dy())
	}

	clock.moveTo(2500 * time.Millisecond)
	term.until("a new frame", func(s *screen) bool { return len(s.graphics) > len(graphics) })
	stop()
	_, graphics = term.state()
	if last := graphics[len(graphics)-1]; last != fmt.Sprintf("a=d,d=I,i=%d,q=2", id) {
		t.Errorf("the last graphics command is %q, want the image deleted", last)
	}
	for _, g := range graphics[:len(graphics)-1] {
		if !strings.HasPrefix(g, fmt.Sprintf("a=T,U=1,f=100,i=%d,", id)) && !strings.HasPrefix(g, "m=") {
			t.Errorf("graphics command %.40q, want only frames of image %d", g, id)
		}
	}
	checkLines(t, term)
}

// imageID returns the id of the image the first graphics command sends.
func imageID(t *testing.T, graphics []string) uint32 {
	t.Helper()
	if len(graphics) > 0 {
		keys, _, _ := strings.Cut(graphics[0], ";")
		for key := range strings.SplitSeq(keys, ",") {
			if value, ok := strings.CutPrefix(key, "i="); ok {
				if id, err := strconv.ParseUint(value, 10, 24); err == nil {
					return uint32(id)
				}
			}
		}
	}
	t.Fatalf("graphics %.60q, want an image with an id first", graphics)
	return 0
}

// checkFrame checks graphics start with one frame of image id, in as many commands as it takes,
// and returns it.
func checkFrame(t *testing.T, graphics []string, id uint32) image.Image {
	t.Helper()
	var payload string
	for i, g := range graphics {
		keys, data, _ := strings.Cut(g, ";")
		if i == 0 && !strings.HasPrefix(keys, fmt.Sprintf("a=T,U=1,f=100,i=%d,p=1,c=2,r=1,q=2,", id)) {
			t.Fatalf("the first graphics command has keys %q, want a frame of image %d", keys, id)
		}
		payload += data
		if strings.Contains(keys, "m=0") {
			data, err := base64.StdEncoding.DecodeString(payload)
			if err != nil {
				t.Fatal(err)
			}
			im, err := png.Decode(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			return im
		}
	}
	t.Fatalf("graphics %q never finish a frame", graphics)
	return nil
}

func TestLiveFaceMovesOnlyAfterBubbleTeasFirstFrame(t *testing.T) {
	term := newTerminal(t)
	a := newLiveApp(t, term)
	a.faces = &faces{kind: imageFaces, cell: cellSize{32, 64}}
	// Each look at the clock is half a second later, so each tick moves the face.
	var looks atomic.Int64
	a.now = func() time.Time { return liveStart.Add(time.Duration(looks.Add(1)) * 500 * time.Millisecond) }
	a.liveAfter = time.Hour
	stop := a.showLive(t.Context(), "forking app", forkingFace)
	// Bubble Tea draws its first frame after 200 ms, long after the face's first tick would come.
	a.live.fps = 5
	a.live.show()
	shown := term.until("a second frame of the face", func(s *screen) bool {
		frames := 0
		for _, g := range s.graphics {
			if strings.HasPrefix(g, "a=T") {
				frames++
			}
		}
		return frames >= 2
	})
	stop()
	if !shown {
		return
	}
	if parts := strings.SplitN(term.String(), "\x1b_Ga=T", 3); len(parts) < 3 || !strings.Contains(parts[1], "forking app") {
		t.Error("the line didn't come between the face's first two frames, want Bubble Tea's first frame before the face moves")
	}
	checkLines(t, term)
}

func TestLiveLineThatNeverShowsSendsNoImage(t *testing.T) {
	term := newTerminal(t)
	a := newLiveApp(t, term)
	a.faces = &faces{kind: imageFaces, cell: cellSize{9, 19}}
	a.liveAfter = time.Hour
	stop := a.showLive(t.Context(), "making app", makingFace)
	stop()
	if got := term.String(); got != "" {
		t.Errorf("stderr %q, want nothing", got)
	}
}

func TestLiveLineDeletesItsImageWhenCtrlCStopsIt(t *testing.T) {
	term := newTerminal(t)
	a := newLiveApp(t, term)
	a.faces = &faces{kind: imageFaces, cell: cellSize{9, 19}}
	ctx, cancel := context.WithCancelCause(t.Context())
	stop := a.showLive(ctx, "making app", makingFace)
	term.waitFor("making app (0s)")
	_, graphics := term.state()
	id := imageID(t, graphics)
	cancel(stopped{syscall.SIGINT})
	stop()
	_, graphics = term.state()
	if last := graphics[len(graphics)-1]; last != fmt.Sprintf("a=d,d=I,i=%d,q=2", id) {
		t.Errorf("the last graphics command is %q, want the image deleted", last)
	}
	checkLines(t, term)
}

func TestLiveFaceStartsTheNewestScriptWhenItShows(t *testing.T) {
	term := newTerminal(t)
	a := newLiveApp(t, term)
	a.faces = &faces{kind: textFaces, dim: true}
	var clock liveClock
	a.now = clock.now
	a.liveAfter = time.Hour
	stop := a.showLive(t.Context(), "forking app", forkingFace)
	restore := a.live.waitFor("waking the host", wakingFace)
	a.live.show()
	term.waitFor("▬▬ waking the host (0s)")
	if row, _ := term.state(); !row[0].dim {
		t.Errorf("the face %+v isn't dim, want it asleep", row[:2])
	}
	restore()
	clock.moveTo(time.Second)
	term.waitFor("▪▪ forking app")
	stop()
	checkLines(t, term)
}
