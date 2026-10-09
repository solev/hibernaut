package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/solev/hibernaut/internal/bot"
)

// faceKind is how hib draws the bot (docs/milestones/m11-bot-faces.md).
type faceKind int

const (
	noFaces faceKind = iota
	textFaces
	imageFaces
)

func (k faceKind) String() string {
	return [...]string{"no faces", "text faces", "image faces"}[k]
}

// facesFor picks the kind of face from hib's environment. It asks the terminal nothing.
func facesFor(getenv func(string) string) faceKind {
	switch getenv("HIB_FACES") {
	case "off":
		return noFaces
	case "text":
		return textFaces
	}
	term := getenv("TERM")
	switch {
	case getenv("NO_COLOR") != "":
		return textFaces
	// A multiplexer draws its own screen, and doesn't pass images on.
	case getenv("TMUX") != "" || getenv("STY") != "" || getenv("ZELLIJ") != "" ||
		strings.HasPrefix(term, "screen") || strings.HasPrefix(term, "tmux"):
		return textFaces
	}
	switch getenv("TERM_PROGRAM") {
	case "ghostty", "kitty":
		return imageFaces
	case "":
		if term == "xterm-ghostty" || term == "xterm-kitty" {
			return imageFaces
		}
	}
	return textFaces
}

// facePalette draws the bot in the brand's Lamplight colours in every terminal, since hib doesn't
// ask a terminal for its colours. The dark eyes and edge keep the cream body clear on a light
// background, and the accent is a deeper amber than Lamplight's, so it shows there too.
var facePalette = bot.Palette{
	Body:   color.NRGBA{0xf5, 0xef, 0xe5, 0xff},
	Eyes:   color.NRGBA{0x14, 0x11, 0x0e, 0xff},
	Accent: color.NRGBA{0xe8, 0xa9, 0x4c, 0xff},
	Edge:   color.NRGBA{0x14, 0x11, 0x0e, 0xc0},
}

// cellSize is how big a terminal's cells are, in pixels.
type cellSize struct{ w, h int }

// defaultCell is the size hib assumes when a terminal doesn't say.
var defaultCell = cellSize{10, 20}

// cellSizeOf reads the cell size of the first of fds that's a terminal from its window size,
// which the kernel answers, not the terminal.
func cellSizeOf(fds ...int) cellSize {
	for _, fd := range fds {
		if ws, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ); err == nil {
			return cellOf(ws)
		}
	}
	return defaultCell
}

// cellOf works out the cell size from a window size. Some terminals leave its pixels at 0.
func cellOf(ws *unix.Winsize) cellSize {
	if ws.Col == 0 || ws.Row == 0 {
		return defaultCell
	}
	w, h := int(ws.Xpixel)/int(ws.Col), int(ws.Ypixel)/int(ws.Row)
	if w < 4 || h < 8 {
		return defaultCell
	}
	return cellSize{min(w, 64), min(h, 128)}
}

// faces draws the bot's faces for one run of hib, as text or image faces. A nil *faces draws none.
type faces struct {
	kind faceKind
	// cell is the size of the terminal's cells, which image faces are drawn at so the terminal
	// doesn't scale them.
	cell cellSize
	// dim says an asleep text face may be dim. NO_COLOR turns it off.
	dim    bool
	stills map[bot.State]string
}

// newFaces returns the faces hib's environment asks for, or nil for none. cell is only called
// for image faces.
func newFaces(getenv func(string) string, cell func() cellSize) *faces {
	kind := facesFor(getenv)
	if kind == noFaces {
		return nil
	}
	f := &faces{kind: kind, dim: getenv("NO_COLOR") == ""}
	if kind == imageFaces {
		f.cell = cell()
	}
	return f
}

// printedFaces returns the faces for lines on stdout, or nil. Like colour, they need stdout to be
// a terminal, no --json and a TERM other than dumb.
func (a *app) printedFaces() *faces {
	if !a.stdoutTerminal || a.json || a.getenv("TERM") == "dumb" {
		return nil
	}
	return a.faces
}

// face returns a face for the start of a result on stdout, with a space after it, or nothing.
func (a *app) face(s bot.State) string {
	if face := a.printedFaces().still(s); face != "" {
		return face + " "
	}
	return ""
}

// faceColumns is how wide every face is.
const faceColumns = 2

const (
	openEyes = "▪▪"
	shutEyes = "▬▬"
)

// still returns a face for a line printed once: a machine settled in state s, two columns wide.
func (f *faces) still(s bot.State) string {
	if f == nil {
		return ""
	}
	if face, ok := f.stills[s]; ok {
		return face
	}
	b := bot.New(s, 1)
	var face string
	if f.kind == imageFaces {
		// C=1 leaves the cursor where it is, and the spaces move it on, in any terminal.
		face = kittyCommand("a=T,f=100,q=2,C=1,c=2,r=1", encodePNG(f.draw(b))) + strings.Repeat(" ", faceColumns)
	} else {
		face = f.eyes(b)
	}
	if f.stills == nil {
		f.stills = map[bot.State]string{}
	}
	f.stills[s] = face
	return face
}

// machine returns the face for a machine in state, a value from hibd: awake when it's running
// and asleep when it's paused. Any other state gets two blank columns.
func (f *faces) machine(state string) string {
	switch state {
	case "running":
		return f.still(bot.Idle)
	case "paused":
		return f.still(bot.Asleep)
	}
	return strings.Repeat(" ", faceColumns)
}

// table starts each row of a machine table with the machine's face and two spaces, and the
// header with as many spaces, so the columns still line up.
func (f *faces) table(table string, states []string) string {
	if f == nil {
		return table
	}
	var b strings.Builder
	for i, line := range strings.SplitAfter(table, "\n") {
		switch {
		case line == "":
			continue
		case i == 0:
			b.WriteString(strings.Repeat(" ", faceColumns+2))
		case i <= len(states):
			b.WriteString(f.machine(states[i-1]) + "  ")
		}
		b.WriteString(line)
	}
	return b.String()
}

// eyes is a text face: the bot's eyes, dim while it's asleep.
func (f *faces) eyes(b *bot.Bot) string {
	switch {
	case !b.Shut():
		return openEyes
	case f.dim && b.State() == bot.Asleep:
		return "\x1b[2m" + shutEyes + "\x1b[22m"
	}
	return shutEyes
}

// draw draws a frame two cells wide and one high.
func (f *faces) draw(b *bot.Bot) *image.NRGBA {
	return b.Draw(faceColumns*f.cell.w, f.cell.h, facePalette)
}

func encodePNG(im image.Image) []byte {
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&buf, im); err != nil {
		panic(fmt.Sprintf("encoding a face: %v", err))
	}
	return buf.Bytes()
}

// kittyMax is the most base64 one kitty graphics command may carry.
const kittyMax = 4096

// kittyCommand sends data with the kitty graphics protocol. keys go on the first command, and
// data that doesn't fit in one goes on in more, as the protocol asks.
func kittyCommand(keys string, data []byte) string {
	encoded := base64.StdEncoding.EncodeToString(data)
	var b strings.Builder
	for first := true; first || encoded != ""; first = false {
		chunk := encoded[:min(kittyMax, len(encoded))]
		encoded = encoded[len(chunk):]
		more := 0
		if encoded != "" {
			more = 1
		}
		if first {
			fmt.Fprintf(&b, "\x1b_G%s,m=%d;%s\x1b\\", keys, more, chunk)
		} else {
			fmt.Fprintf(&b, "\x1b_Gm=%d,q=2;%s\x1b\\", more, chunk)
		}
	}
	return b.String()
}
