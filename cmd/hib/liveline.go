package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
)

const (
	liveAfter             = 2 * time.Second
	liveWidth, liveHeight = 80, 24
)

func (a *app) liveTerminal() bool {
	return a.stderrTerminal && !a.json && a.getenv("TERM") != "dumb"
}

// showLive shows text on the live line while hib waits, after a while, with a face that moves as
// script says.
func (a *app) showLive(ctx context.Context, text string, script faceScript) (stop func()) {
	if !a.liveTerminal() {
		return func() {}
	}
	now := a.now()
	l := &live{
		out:     a.stderr,
		ctx:     ctx,
		env:     []string{"TERM=" + a.getenv("TERM")},
		size:    a.liveSize,
		resizes: a.resizes,
		model:   liveLine{text: text, since: now, now: now, clock: a.now, face: newLiveFace(a.faces)},
		script:  script,
	}
	a.stderr, a.live = l, l
	if a.liveAfter > 0 {
		l.timer = time.AfterFunc(a.liveAfter, l.show)
	} else {
		l.show()
	}
	return func() {
		l.stop()
		a.stderr, a.live = l.out, nil
	}
}

func (a *app) liveSize() (width, height int) {
	if a.stderrSize != nil {
		if width, height, err := a.stderrSize(); err == nil && width > 0 && height > 0 {
			return width, height
		}
	}
	return liveWidth, liveHeight
}

func watchResizes() (<-chan os.Signal, func()) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGWINCH)
	return signals, func() { signal.Stop(signals) }
}

// live runs a liveLine in an inline Bubble Tea program that reads no input, so it sends the
// terminal no queries and leaves its modes and Ctrl-C alone.
type live struct {
	out     io.Writer
	ctx     context.Context
	env     []string
	size    func() (width, height int)
	resizes func() (<-chan os.Signal, func())

	mu    sync.Mutex
	model liveLine
	// script is the face's script as last set. Once Bubble Tea runs, the face is its own.
	script  faceScript
	program *tea.Program
	done    chan struct{}
	watched <-chan struct{}
	timer   *time.Timer
	partial []byte
	stopped bool
	// imageSent says the terminal has the face's image, to delete when the line goes.
	imageSent bool
	// fps is how often Bubble Tea draws, or 0 for its default. Tests slow it down.
	fps int
}

func (l *live) show() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped || l.ctx.Err() != nil {
		return
	}
	width, height := l.size()
	now := l.model.clock()
	l.model.now, l.model.width = now, width
	options := []tea.ProgramOption{
		tea.WithInput(nil),
		tea.WithoutSignalHandler(),
		tea.WithContext(l.ctx),
		tea.WithEnvironment(l.env),
		tea.WithWindowSize(width, height),
	}
	if l.fps > 0 {
		options = append(options, tea.WithFPS(l.fps))
	}
	if face := l.model.face; face != nil {
		face.play(l.script, now)
		// The first frame goes before Bubble Tea starts, so that Bubble Tea's first write is its
		// first frame.
		if frame := face.frame(); frame != "" {
			io.WriteString(l.out, frame)
			l.imageSent = true
		}
		options = append(options, tea.WithColorProfile(face.profile()))
	}
	out := &firstWrite{w: &syncWriter{w: l.out}, written: make(chan struct{})}
	p := tea.NewProgram(l.model, append(options, tea.WithOutput(out))...)
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Run()
	}()
	watched := l.follow(p, done)
	// Until Bubble Tea has drawn its first frame, it places printed lines as if the frame filled
	// the window, at the top of the screen.
	select {
	case <-out.written:
		// The face moves from now on. A frame of it any sooner could be Bubble Tea's first write.
		if l.model.face != nil {
			p.Send(faceTick(l.model.clock()))
		}
	case <-done:
	}
	l.program, l.done, l.watched = p, done, watched
}

func (l *live) follow(p *tea.Program, done <-chan struct{}) <-chan struct{} {
	watched := make(chan struct{})
	if l.resizes == nil {
		close(watched)
		return watched
	}
	signals, unwatch := l.resizes()
	go func() {
		defer close(watched)
		defer unwatch()
		for {
			select {
			case <-done:
				return
			case <-signals:
				width, height := l.size()
				p.Send(tea.WindowSizeMsg{Width: width, Height: height})
			}
		}
	}()
	return watched
}

// syncWriter writes one write at a time. Bubble Tea writes the face's frames and the lines it
// prints above the live line from different goroutines.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

type firstWrite struct {
	w       io.Writer
	once    sync.Once
	written chan struct{}
}

func (f *firstWrite) Write(p []byte) (int, error) {
	defer f.once.Do(func() { close(f.written) })
	return f.w.Write(p)
}

func (l *live) running() bool {
	if l.program == nil {
		return false
	}
	if l.ctx.Err() != nil {
		<-l.done
		return false
	}
	select {
	case <-l.done:
		return false
	default:
		return true
	}
}

func (l *live) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.partial = append(l.partial, p...)
	end := bytes.LastIndexByte(l.partial, '\n') + 1
	if end == 0 {
		return len(p), nil
	}
	lines, rest := l.partial[:end], bytes.Clone(l.partial[end:])
	l.partial = rest
	if l.running() {
		text := bytes.ReplaceAll(lines[:end-1], []byte("\r\n"), []byte("\n"))
		l.program.Send(tea.Println(string(bytes.TrimSuffix(text, []byte("\r"))))())
		return len(p), nil
	}
	if _, err := l.out.Write(lines); err != nil {
		return 0, err
	}
	return len(p), nil
}

// waitFor shows text, with a face that moves as script says, until restore puts back the text
// and script before and starts that script again.
func (l *live) waitFor(text string, script faceScript) (restore func()) {
	previous, previousScript := l.change(text, &script)
	return func() { l.change(previous, &previousScript) }
}

// set shows text from now on, and leaves the face moving as it is.
func (l *live) set(text string) {
	l.change(text, nil)
}

// change shows text and starts script, unless it's nil. Before the line shows, it only keeps the
// script, which starts when the line shows.
func (l *live) change(text string, script *faceScript) (previous string, previousScript faceScript) {
	if l == nil {
		return "", faceScript{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	previous, previousScript = l.model.text, l.script
	l.model.text, l.model.since = text, l.model.clock()
	msg := liveWait{text: text, since: l.model.since}
	if script != nil {
		l.script = *script
		// Bubble Tea's copy, which l doesn't change.
		played := *script
		msg.script = &played
	}
	if l.running() {
		l.program.Send(msg)
	}
	return previous, previousScript
}

func (l *live) stop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.stopped = true
	if l.timer != nil {
		l.timer.Stop()
	}
	if l.program != nil {
		l.program.Send(liveDone{})
		<-l.done
		<-l.watched
		l.program = nil
	}
	// Bubble Tea has cleared the line and written the frames it held, so the delete comes last.
	if l.imageSent {
		io.WriteString(l.out, l.model.face.deleteImage())
		l.imageSent = false
	}
	if len(l.partial) > 0 {
		l.out.Write(l.partial)
		l.partial = nil
	}
}

type liveLine struct {
	text  string
	since time.Time
	now   time.Time
	width int
	clock func() time.Time
	done  bool
	// face is nil without faces.
	face *liveFace
}

type (
	liveTick time.Time
	faceTick time.Time
	liveWait struct {
		text  string
		since time.Time
		// script is a new script for the face, which starts at since, or nil.
		script *faceScript
	}
	liveDone struct{}
)

func (l liveLine) Init() tea.Cmd { return l.tick() }

func (l liveLine) tick() tea.Cmd {
	clock := l.clock
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return liveTick(clock()) })
}

func (l liveLine) faceTick() tea.Cmd {
	if l.face == nil {
		return nil
	}
	clock := l.clock
	return tea.Tick(faceEvery, func(time.Time) tea.Msg { return faceTick(clock()) })
}

func (l liveLine) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case liveTick:
		l.now = time.Time(msg)
		if !l.done {
			return l, l.tick()
		}
	case faceTick:
		if l.done || l.face == nil {
			return l, nil
		}
		l.face.move(time.Time(msg))
		next := l.faceTick()
		if frame := l.face.frame(); frame != "" {
			// In sequence, so a frame is written before the next tick can make a newer one.
			return l, tea.Sequence(tea.Raw(frame), next)
		}
		return l, next
	case liveWait:
		l.text, l.since, l.now = msg.text, msg.since, msg.since
		if msg.script != nil && l.face != nil {
			l.face.play(*msg.script, msg.since)
		}
	case tea.WindowSizeMsg:
		l.width = msg.Width
	case liveDone:
		l.done = true
		return l, tea.Quit
	}
	return l, nil
}

func (l liveLine) View() tea.View {
	v := tea.NewView("")
	v.DisableBracketedPasteMode = true
	if !l.done {
		v.SetContent(l.line())
	}
	return v
}

func (l liveLine) line() string {
	elapsed := fmt.Sprintf(" (%v)", max(0, l.now.Sub(l.since)).Truncate(time.Second))
	text, face := l.text, ""
	columns := l.width - 1 - len(elapsed)
	// The face shows when it leaves room for some of the text. Its width is known: shortenLine
	// can't measure it.
	if l.face != nil && (l.width <= 0 || columns-faceColumns-1 >= 2) {
		face = l.face.cells() + " "
		columns -= faceColumns + 1
	}
	if l.width > 0 {
		text = shortenLine(text, columns)
	}
	return face + text + elapsed
}

func shortenLine(text string, columns int) string {
	width := 0
	for _, r := range text {
		width += runeColumns(r)
	}
	if width <= columns {
		return text
	}
	width = 0
	for i, r := range text {
		if width+runeColumns(r) > columns-1 {
			return text[:i] + "…"
		}
		width += runeColumns(r)
	}
	return text
}

func runeColumns(r rune) int {
	if r < 128 || r == '…' {
		return 1
	}
	return 2
}
