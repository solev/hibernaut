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

func (a *app) showLive(ctx context.Context, text string) (stop func()) {
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
		model:   liveLine{text: text, since: now, now: now, clock: a.now},
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

	mu      sync.Mutex
	model   liveLine
	program *tea.Program
	done    chan struct{}
	watched <-chan struct{}
	timer   *time.Timer
	partial []byte
	stopped bool
}

func (l *live) show() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped || l.ctx.Err() != nil {
		return
	}
	width, height := l.size()
	l.model.now, l.model.width = l.model.clock(), width
	out := &firstWrite{w: l.out, written: make(chan struct{})}
	p := tea.NewProgram(l.model,
		tea.WithInput(nil),
		tea.WithOutput(out),
		tea.WithoutSignalHandler(),
		tea.WithContext(l.ctx),
		tea.WithEnvironment(l.env),
		tea.WithWindowSize(width, height),
	)
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

func (l *live) waitFor(text string) (restore func()) {
	previous := l.set(text)
	return func() { l.set(previous) }
}

func (l *live) set(text string) (previous string) {
	if l == nil {
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	previous = l.model.text
	l.model.text, l.model.since = text, l.model.clock()
	if l.running() {
		l.program.Send(liveWait{text: text, since: l.model.since})
	}
	return previous
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
}

type (
	liveTick time.Time
	liveWait struct {
		text  string
		since time.Time
	}
	liveDone struct{}
)

func (l liveLine) Init() tea.Cmd { return l.tick() }

func (l liveLine) tick() tea.Cmd {
	clock := l.clock
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return liveTick(clock()) })
}

func (l liveLine) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case liveTick:
		l.now = time.Time(msg)
		if !l.done {
			return l, l.tick()
		}
	case liveWait:
		l.text, l.since, l.now = msg.text, msg.since, msg.since
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
	text := l.text
	if l.width > 0 {
		text = shortenLine(text, l.width-1-len(elapsed))
	}
	return text + elapsed
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
