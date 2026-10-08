package main

import (
	"bytes"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"
)

// cell is one column of the screen: a character, the combining marks on it, and how it's drawn.
type cell struct {
	r     rune
	marks string
	fg    string
	dim   bool
}

type screen struct {
	t             *testing.T
	width, height int
	history       []string
	rows          [][]cell
	x, y          int
	cursorHidden  bool
	pending       []byte
	fg            string
	dim           bool
	// graphics is every kitty graphics command, without its ESC _ G and ST.
	graphics []string
}

func newScreen(t *testing.T, width, height int) *screen {
	s := &screen{t: t, width: width, height: height, rows: make([][]cell, height)}
	for i := range s.rows {
		s.rows[i] = s.blank()
	}
	return s
}

func (s *screen) blank() []cell {
	row := make([]cell, s.width)
	for i := range row {
		row[i] = cell{r: ' '}
	}
	return row
}

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
	if unicode.Is(unicode.Mn, r) {
		if s.x == 0 {
			s.t.Errorf("screen got combining mark %U at the start of a line", r)
			return n
		}
		s.rows[s.y][s.x-1].marks += string(r)
		return n
	}
	if s.x >= s.width {
		s.x = 0
		s.lineFeed()
	}
	s.rows[s.y][s.x] = cell{r: r, fg: s.fg, dim: s.dim}
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
	if p[1] == '_' {
		end := bytes.Index(p, []byte("\x1b\\"))
		if end < 0 {
			return 0
		}
		if !bytes.HasPrefix(p, []byte("\x1b_G")) {
			s.t.Errorf("screen got APC %q", p[:end+2])
		}
		s.graphics = append(s.graphics, string(p[3:end]))
		return end + 2
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
	if final == 'm' {
		s.sgr(params)
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
			s.rows[s.y][x] = cell{r: ' '}
		}
	case 'L':
		s.x = 0
		for range n {
			s.rows = append(s.rows[:s.y], append([][]cell{s.blank()}, s.rows[s.y:s.height-1]...)...)
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
		s.rows[s.y][x] = cell{r: ' '}
	}
}

// sgr follows the attributes the live line may use: dim, and foreground colours.
func (s *screen) sgr(params string) {
	fields := strings.Split(params, ";")
	for i := 0; i < len(fields); i++ {
		switch f := fields[i]; {
		case f == "" || f == "0":
			s.fg, s.dim = "", false
		case f == "2":
			s.dim = true
		case f == "22":
			s.dim = false
		case f == "39":
			s.fg = ""
		case f == "38" && i+4 < len(fields) && fields[i+1] == "2":
			s.fg = strings.Join(fields[i:i+5], ";")
			i += 4
		default:
			s.t.Errorf("screen got attribute %q in %q", f, params)
		}
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

func (s *screen) row(y int) string {
	var b strings.Builder
	for _, c := range s.rows[y] {
		b.WriteRune(c.r)
		b.WriteString(c.marks)
	}
	return strings.TrimRight(b.String(), " ")
}

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
	return newSizedTerminal(t, liveWidth, liveHeight)
}

func newSizedTerminal(t *testing.T, width, height int) *terminal {
	return &terminal{t: t, screen: newScreen(t, width, height), changed: make(chan struct{})}
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

// state returns the cells of the row the cursor is on, and every kitty graphics command so far.
func (term *terminal) state() (row []cell, graphics []string) {
	term.mu.Lock()
	defer term.mu.Unlock()
	return slices.Clone(term.screen.rows[term.screen.y]), slices.Clone(term.screen.graphics)
}

// until waits for the screen to be as ok says.
func (term *terminal) until(what string, ok func(*screen) bool) bool {
	term.t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		term.mu.Lock()
		done, changed := ok(term.screen), term.changed
		term.mu.Unlock()
		if done {
			return true
		}
		select {
		case <-changed:
		case <-deadline:
			term.t.Errorf("the terminal never showed %s", what)
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

func TestScreenKeepsMarksColoursAndImages(t *testing.T) {
	s := newScreen(t, 20, 2)
	s.Write([]byte("\x1b_Ga=T,i=5;AA"))
	s.Write([]byte("AA\x1b\\\x1b[38;2;0;0;5m\U0010EEEE\u0305\u0305\U0010EEEE\u0305\u030D\x1b[39m \x1b[2mzz\x1b[m!"))
	if got, want := s.row(0), "\U0010EEEE\u0305\u0305\U0010EEEE\u0305\u030D zz!"; got != want {
		t.Errorf("row %q, want %q", got, want)
	}
	row := s.rows[0]
	if row[0].fg != "38;2;0;0;5" || row[1].fg != "38;2;0;0;5" || row[2].fg != "" || !row[3].dim || row[5].dim {
		t.Errorf("cells %+v, want the colour on the first two and dim on zz", row[:6])
	}
	if len(s.graphics) != 1 || s.graphics[0] != "a=T,i=5;AAAA" {
		t.Errorf("graphics %q, want the one command", s.graphics)
	}
}
