package main

import (
	"bytes"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

type screen struct {
	t             *testing.T
	width, height int
	history       []string
	rows          [][]rune
	x, y          int
	cursorHidden  bool
	pending       []byte
}

func newScreen(t *testing.T, width, height int) *screen {
	s := &screen{t: t, width: width, height: height, rows: make([][]rune, height)}
	for i := range s.rows {
		s.rows[i] = s.blank()
	}
	return s
}

func (s *screen) blank() []rune { return []rune(strings.Repeat(" ", s.width)) }

func (s *screen) Write(p []byte) (int, error) {
	s.pending = append(s.pending, p...)
	for len(s.pending) > 0 {
		n := s.step(s.pending)
		if n == 0 {
			break
		}
		s.pending = s.pending[n:]
	}
	return len(p), nil
}

func (s *screen) step(p []byte) int {
	switch p[0] {
	case '\r':
		s.x = 0
		return 1
	case '\n':
		// A terminal that isn't in raw mode starts a new line at its first column.
		s.x = 0
		s.lineFeed()
		return 1
	case '\x1b':
		return s.escape(p)
	}
	if p[0] < 0x20 || p[0] == 0x7f {
		s.t.Errorf("screen got control byte %q", p[0])
		return 1
	}
	if !utf8.FullRune(p) {
		return 0
	}
	r, n := utf8.DecodeRune(p)
	if s.x >= s.width {
		s.x = 0
		s.lineFeed()
	}
	s.rows[s.y][s.x] = r
	s.x++
	return n
}

func (s *screen) lineFeed() {
	if s.y < s.height-1 {
		s.y++
		return
	}
	s.history = append(s.history, s.row(0))
	s.rows = append(s.rows[1:], s.blank())
}

func (s *screen) escape(p []byte) int {
	if len(p) < 2 {
		return 0
	}
	if p[1] != '[' {
		s.t.Errorf("screen got escape %q", p[:2])
		return 2
	}
	end := 2
	for end < len(p) && (p[end] < 0x40 || p[end] > 0x7e) {
		end++
	}
	if end == len(p) {
		return 0
	}
	params, final := string(p[2:end]), p[end]
	if strings.HasPrefix(params, "?") {
		s.mode(params, final)
		return end + 1
	}
	n := 1
	var args []int
	for _, field := range strings.Split(params, ";") {
		v, err := strconv.Atoi(field)
		if field != "" && err != nil {
			s.t.Errorf("screen got %q", p[:end+1])
		}
		args = append(args, v)
	}
	if args[0] > 0 {
		n = args[0]
	}
	switch final {
	case 'A':
		s.y = max(0, s.y-n)
	case 'B':
		s.y = min(s.height-1, s.y+n)
	case 'C':
		s.x = min(s.width-1, s.x+n)
	case 'D':
		s.x = max(0, s.x-n)
	case 'G':
		s.x = min(s.width-1, n-1)
	case 'd':
		s.y = min(s.height-1, n-1)
	case 'H':
		s.y = min(s.height-1, n-1)
		s.x = 0
		if len(args) > 1 && args[1] > 0 {
			s.x = min(s.width-1, args[1]-1)
		}
	case 'J':
		s.eraseRight()
		for y := s.y + 1; y < s.height; y++ {
			s.rows[y] = s.blank()
		}
	case 'K':
		s.eraseRight()
	case 'X':
		for x := s.x; x < min(s.width, s.x+n); x++ {
			s.rows[s.y][x] = ' '
		}
	case 'L':
		s.x = 0
		for range n {
			s.rows = append(s.rows[:s.y], append([][]rune{s.blank()}, s.rows[s.y:s.height-1]...)...)
		}
	case 'M':
		s.x = 0
		for range n {
			s.rows = append(append(s.rows[:s.y:s.y], s.rows[s.y+1:]...), s.blank())
		}
	default:
		s.t.Errorf("screen got unexpected sequence %q", p[:end+1])
	}
	return end + 1
}

func (s *screen) eraseRight() {
	for x := s.x; x < s.width; x++ {
		s.rows[s.y][x] = ' '
	}
}

func (s *screen) mode(params string, final byte) {
	switch {
	case params == "?25" && final == 'l':
		s.cursorHidden = true
	case params == "?25" && final == 'h':
		s.cursorHidden = false
	default:
		s.t.Errorf("screen got mode %q%c: the live line must leave the terminal's modes alone", params, final)
	}
}

func (s *screen) row(y int) string { return strings.TrimRight(string(s.rows[y]), " ") }

func (s *screen) lines() []string {
	lines := append([]string(nil), s.history...)
	for y := range s.rows {
		lines = append(lines, s.row(y))
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

type terminal struct {
	t       *testing.T
	mu      sync.Mutex
	raw     bytes.Buffer
	screen  *screen
	changed chan struct{}
}

func newTerminal(t *testing.T) *terminal {
	return &terminal{t: t, screen: newScreen(t, liveWidth, liveHeight), changed: make(chan struct{})}
}

func (term *terminal) Write(p []byte) (int, error) {
	term.mu.Lock()
	defer term.mu.Unlock()
	term.raw.Write(p)
	term.screen.Write(p)
	close(term.changed)
	term.changed = make(chan struct{})
	return len(p), nil
}

func (term *terminal) waitFor(want string) bool {
	term.t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		term.mu.Lock()
		row, changed := term.screen.row(term.screen.y), term.changed
		term.mu.Unlock()
		if strings.Contains(row, want) {
			return true
		}
		select {
		case <-changed:
		case <-deadline:
			term.t.Errorf("the live line never showed %q. It shows %q", want, row)
			return false
		}
	}
}

func (term *terminal) lines() []string {
	term.mu.Lock()
	defer term.mu.Unlock()
	return term.screen.lines()
}

func (term *terminal) String() string {
	term.mu.Lock()
	defer term.mu.Unlock()
	return term.raw.String()
}

func (term *terminal) cursorHidden() bool {
	term.mu.Lock()
	defer term.mu.Unlock()
	return term.screen.cursorHidden
}

func TestScreen(t *testing.T) {
	s := newScreen(t, 20, 3)
	s.Write([]byte("one\r\ntwo\r\nthree\r\nlive"))
	s.Write([]byte("\r\n\x1b[A\x1b[Lins\x1b[K\r\n"))
	s.Write([]byte("\x1b[2Cx\x1b[J"))
	want := []string{"one", "two", "three", "ins", "lix"}
	if got := s.lines(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("lines %q, want %q", got, want)
	}
}
