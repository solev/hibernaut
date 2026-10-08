package main

import (
	"fmt"
	"image"
	"math"
	"strings"
	"unicode/utf8"
)

// row is one terminal line. Images sit on spaces the text leaves for them and are placed with
// their top-left cell on this row.
type row struct {
	text string
	imgs []placed
}

type placed struct {
	col int
	sp  *sprite
}

// sprite is a drawn bot: lines of block characters, or an image for the kitty graphics protocol.
type sprite struct {
	lines      []string
	img        *image.NRGBA
	cols, rows int
	key        string
}

func makeSprite(rend string, b *bot, vb viewBox, cols, rows, cellW, cellH int, fg, acc rgb) *sprite {
	ratio := float64(cellH) / float64(cellW)
	sp := &sprite{rows: rows}
	switch rend {
	case "half":
		ph, pa := 2*rows, 2/ratio
		pw := cols
		if pw == 0 {
			pw = max(1, int(math.Round(vb.w/vb.h*float64(ph)/pa)))
		}
		sp.lines, sp.cols = halfLines(rasterize(b, vb, pw, ph, pa, true), acc), pw
	case "sextant":
		ph, pa := 3*rows, 1.5/ratio
		pw := cols * 2
		if pw == 0 {
			pw = max(2, int(math.Round(vb.w/vb.h*float64(ph)/pa/2))*2)
		}
		sp.lines, sp.cols = sextantLines(rasterize(b, vb, pw, ph, pa, true), acc), pw/2
	default:
		if cols == 0 {
			cols = max(1, int(math.Round(vb.w/vb.h*float64(rows)*ratio)))
		}
		w, h := cols*cellW, rows*cellH
		sp.img, sp.cols = kittyImage(rasterize(b, vb, w, h, 1, h < 48), fg, acc), cols
	}
	return sp
}

type variant struct {
	key, name, thesis string
	renderers         []string
	rows              map[string]int
	draw              func(v *view, f frame, p *player) []row
}

var variants []variant

func init() {
	variants = []variant{
		{"A", "Faces", "a face on every line about a machine, and only the live line's face moves",
			[]string{"kitty", "inverse", "eyes"}, nil, drawFaces},
		{"B", "Moments", "output stays as it is today, and a bot appears only while hib makes you wait",
			[]string{"kitty", "half", "sextant"}, map[string]int{"kitty": 5, "half": 9, "sextant": 6}, drawMoments},
		{"C", "Cards", "results print the machine's card and hib ls prints the fleet",
			[]string{"kitty", "half", "sextant"}, map[string]int{"kitty": 3, "half": 5, "sextant": 4}, drawCards},
	}
}

// view holds what the viewer is showing and the terminal it shows it on.
type view struct {
	variant      int
	renderer     [3]int
	rows         map[string]int
	acc, fg, bg  rgb
	dark         bool
	cellW, cellH int
	kitty        bool
	why          string
	width        int
	height       int
	cache        map[string]*sprite
	ids          map[string]int
}

func (v *view) rend() string {
	vr := variants[v.variant]
	return vr.renderers[v.renderer[v.variant]]
}

func (v *view) cycle(d int) {
	vr := variants[v.variant]
	n := len(vr.renderers)
	for i := 0; i < n; i++ {
		v.renderer[v.variant] = ((v.renderer[v.variant]+d)%n + n) % n
		if v.kitty || v.rend() != "kitty" {
			return
		}
	}
}

func (v *view) spriteRows() int {
	k := variants[v.variant].key + "/" + v.rend()
	if r, ok := v.rows[k]; ok {
		return r
	}
	return variants[v.variant].rows[v.rend()]
}

func (v *view) resize(d int) {
	if variants[v.variant].rows == nil {
		return
	}
	v.rows[variants[v.variant].key+"/"+v.rend()] = max(2, v.spriteRows()+d)
}

// sprite draws b, caching it under key when key isn't empty. A sprite without a key is redrawn
// every frame.
func (v *view) sprite(b *bot, vb viewBox, cols, rows int, key string) *sprite {
	if key != "" {
		key = fmt.Sprintf("%s/%s/%dx%d/%v/%v", v.rend(), key, cols, rows, v.acc, v.fg)
		if sp, ok := v.cache[key]; ok {
			return sp
		}
	}
	sp := makeSprite(v.rend(), b, vb, cols, rows, v.cellW, v.cellH, v.fg, v.acc)
	sp.key = key
	if key != "" {
		v.cache[key] = sp
	}
	return sp
}

var stills = map[string]*bot{}

func stillBot(m machine) (*bot, string) {
	state := "asleep"
	if m.awake() {
		state = "idle"
	}
	if stills[state] == nil {
		stills[state] = still(state)
	}
	return stills[state], state
}

func dim(s string) string { return "\x1b[2m" + s + "\x1b[22m" }

func bold(s string) string { return "\x1b[1m" + s + "\x1b[22m" }

func stateWord(s string) string {
	switch s {
	case "running":
		return "\x1b[32m" + s + "\x1b[39m"
	case "paused":
		return "\x1b[33m" + s + "\x1b[39m"
	}
	return s
}

// tableLines prints hib ls's table the way hibapi.Table does: padding 2, state words coloured
// after padding, the last column unpadded.
func tableLines(ms []machine) []string {
	cells := [][]string{{"NAME", "STATE", "IMAGE", "SIZE", "IDLE", "PAUSES IN", "STARTED", "ID", "FROM"}}
	for _, m := range ms {
		cells = append(cells, []string{m.name, m.state, m.image, m.size, m.idle, m.pausesIn, m.started, m.id, m.from})
	}
	w := make([]int, len(cells[0]))
	for _, r := range cells {
		for i, c := range r {
			w[i] = max(w[i], len(c))
		}
	}
	lines := make([]string, len(cells))
	for j, r := range cells {
		var sb strings.Builder
		for i, c := range r {
			if i == 1 && j > 0 {
				sb.WriteString(stateWord(c))
			} else {
				sb.WriteString(c)
			}
			if i < len(r)-1 {
				sb.WriteString(strings.Repeat(" ", w[i]-len(c)+2))
			}
		}
		lines[j] = sb.String()
	}
	return lines
}

// plainRows prints an item exactly as hib prints it today.
func plainRows(it item) []row {
	switch it.kind {
	case itPrompt:
		text := dim(it.ps) + " " + it.text
		if it.caret {
			text += "\x1b[7m \x1b[27m"
		}
		return []row{{text: text}}
	case itDim:
		return []row{{text: dim(it.text)}}
	case itTable:
		var out []row
		for _, l := range tableLines(it.machines) {
			out = append(out, row{text: l})
		}
		return append(out, row{}, row{text: it.room})
	}
	return []row{{text: it.text}}
}

// beside puts sp on the left and label i on its row at+i, gap columns to its right.
func beside(sp *sprite, at, gap int, labels ...string) []row {
	n := max(sp.rows, at+len(labels))
	out := make([]row, n)
	for i := range out {
		text := strings.Repeat(" ", sp.cols)
		if sp.img == nil && i < len(sp.lines) {
			text = sp.lines[i]
		}
		if j := i - at; j >= 0 && j < len(labels) && labels[j] != "" {
			text += strings.Repeat(" ", gap) + labels[j]
		}
		out[i].text = text
	}
	if sp.img != nil {
		out[0].imgs = []placed{{0, sp}}
	}
	return out
}

// hcat sets blocks side by side, each padded to width columns plus gap.
func hcat(width, gap int, blocks ...[]row) []row {
	h := 0
	for _, b := range blocks {
		h = max(h, len(b))
	}
	out := make([]row, h)
	for bi, b := range blocks {
		col := bi * (width + gap)
		for i := range out {
			var r row
			if i < len(b) {
				r = b[i]
			}
			if pad := col - visWidth(out[i].text); pad > 0 {
				out[i].text += strings.Repeat(" ", pad)
			}
			out[i].text += r.text
			for _, p := range r.imgs {
				out[i].imgs = append(out[i].imgs, placed{col + p.col, p.sp})
			}
		}
	}
	return out
}

// grid sets blocks side by side, wrapping them to fit the view's width.
func (v *view) grid(gap int, blocks ...[]row) []row {
	width := 0
	for _, b := range blocks {
		for _, r := range b {
			width = max(width, visWidth(r.text))
		}
	}
	per := max(1, (v.width+gap)/(width+gap))
	var out []row
	for i := 0; i < len(blocks); i += per {
		if i > 0 {
			out = append(out, row{})
		}
		out = append(out, hcat(width, gap, blocks[i:min(i+per, len(blocks))]...)...)
	}
	return out
}

// faceText draws a bot as two cells of eyes. The rule reads the same values the SVG draws.
func faceText(b *bot) (eyes string, shut bool) {
	m := b.main
	arc := clamp(m.arc.v, 0, 1)
	if arc > 0.5 {
		return "^^", false
	}
	h := m.eh.v * m.lid.v * (1 - arc)
	if h < 4.5 || m.ew.v/h > 2.2 {
		return "▬▬", true
	}
	return "▪▪", false
}

// textFace draws b as two cells: eyes cut out of a block in the inverse style, or the eyes
// alone, dim while asleep.
func textFace(style string, b *bot) string {
	eyes, shut := faceText(b)
	switch {
	case style == "inverse":
		return "\x1b[7m" + eyes + "\x1b[27m"
	case shut && (b.current == "asleep" || b.current == "hibernate"):
		return dim(eyes)
	}
	return eyes
}

// face is a two-cell bot for the start of a line, with the sprite to place on it, if any.
func (v *view) face(b *bot, key string) (string, *sprite) {
	if v.rend() == "kitty" {
		return "  ", v.sprite(b, vbTight, 2, 1, key)
	}
	return textFace(v.rend(), b), nil
}

func withFace(face string, sp *sprite, text string) row {
	r := row{text: face + " " + text}
	if sp != nil {
		r.imgs = []placed{{0, sp}}
	}
	return r
}

func drawFaces(v *view, f frame, p *player) []row {
	var out []row
	for _, it := range f.items {
		switch it.kind {
		case itResult, itDim:
			text := it.text
			if it.kind == itDim {
				text = dim(text)
			}
			b, state := stillBot(it.machines[0])
			face, sp := v.face(b, "face/"+state)
			out = append(out, withFace(face, sp, text))
		case itTable:
			lines := tableLines(it.machines)
			out = append(out, row{text: "    " + lines[0]})
			for i, m := range it.machines {
				b, state := stillBot(m)
				face, sp := v.face(b, "face/"+state)
				out = append(out, withFace(face, sp, " "+lines[i+1]))
			}
			out = append(out, row{}, row{text: it.room})
		default:
			out = append(out, plainRows(it)...)
		}
	}
	if f.live != "" {
		face, sp := v.face(p.tight, "")
		out = append(out, withFace(face, sp, f.live))
	}
	return out
}

func drawMoments(v *view, f frame, p *player) []row {
	var out []row
	for _, it := range f.items {
		out = append(out, plainRows(it)...)
	}
	if f.live != "" {
		rows := v.spriteRows()
		sp := v.sprite(p.wide, vbWide, 0, rows, "")
		eye := min(rows-1, int((50-vbWide.y)/vbWide.h*float64(rows)))
		out = append(out, beside(sp, eye, 2, f.live)...)
	}
	return out
}

func (v *view) card(m machine, labels ...string) []row {
	b, state := stillBot(m)
	sp := v.sprite(b, vbFace, 0, v.spriteRows(), "card/"+state)
	at := max(0, (sp.rows-len(labels)+1)/2)
	return beside(sp, at, 2, labels...)
}

func nameLabel(v *view, name string) string {
	return bold(name) + v.acc.fg() + ".hib\x1b[39m"
}

func drawCards(v *view, f frame, p *player) []row {
	var out []row
	for _, it := range f.items {
		switch it.kind {
		case itResult:
			var cards [][]row
			for i, m := range it.machines {
				status := stateWord(m.state) + " · " + m.image + " · " + m.size
				if m.from != "-" {
					status = stateWord(m.state) + " · from " + m.from
				}
				cards = append(cards, v.card(m, nameLabel(v, m.name), status, dim(it.hints[i])))
			}
			out = append(out, v.grid(4, cards...)...)
		case itTable:
			var cards [][]row
			for _, m := range it.machines {
				status := stateWord(m.state) + " · " + m.size
				if m.from != "-" {
					status += " · from " + m.from
				}
				next := "ssh wakes it"
				if m.awake() {
					next = "pauses in " + m.pausesIn
				}
				cards = append(cards, v.card(m, nameLabel(v, m.name), status, dim(next)))
			}
			out = append(out, v.grid(4, cards...)...)
			out = append(out, row{}, row{text: it.room})
		default:
			out = append(out, plainRows(it)...)
		}
	}
	if f.live != "" {
		out = append(out, row{text: f.live})
	}
	return out
}

// visWidth counts the columns s takes, skipping escape sequences. Every glyph the viewer draws
// is one column wide.
func visWidth(s string) int {
	n := 0
	for i := 0; i < len(s); {
		if j := escapeEnd(s, i); j > i {
			i = j
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
		n++
	}
	return n
}

// truncate keeps the first w columns of s, and every escape sequence.
func truncate(s string, w int) string {
	var sb strings.Builder
	n := 0
	for i := 0; i < len(s); {
		if j := escapeEnd(s, i); j > i {
			sb.WriteString(s[i:j])
			i = j
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		if n < w {
			sb.WriteString(s[i : i+size])
		}
		n++
		i += size
	}
	return sb.String()
}

// escapeEnd returns the end of the escape sequence at s[i], or i when there's none there.
func escapeEnd(s string, i int) int {
	if s[i] != 0x1b || i+1 >= len(s) {
		return i
	}
	switch s[i+1] {
	case '[':
		for j := i + 2; j < len(s); j++ {
			if s[j] >= 0x40 && s[j] <= 0x7e {
				return j + 1
			}
		}
	case '_', ']', 'P':
		if j := strings.Index(s[i+2:], "\x1b\\"); j >= 0 {
			return i + 2 + j + 2
		}
	}
	return len(s)
}
