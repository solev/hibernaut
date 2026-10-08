// PROTOTYPE, throwaway. It answers one question: how should hib show its bots in a terminal?
// Three variants replay the same hib session and ←/→ switches between them. Nothing here is
// tested or meant to ship. Delete this directory once a variant is picked.
//
//	go run ./cmd/hib/prototype-bots            # ←/→ variant, r renderer, +/- size, n/p scene, q quit
//	go run ./cmd/hib/prototype-bots -dump -t 14 -variant B -renderer half
//	go run ./cmd/hib/prototype-bots -sheet /tmp/bots.png
package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

const liveID = 1000

func main() {
	variantF := flag.String("variant", "A", "variant to start on: A, B or C")
	rendF := flag.String("renderer", "", "renderer to start on: kitty, inverse, eyes, half or sextant")
	kittyF := flag.String("kitty", "auto", "kitty images: auto, on or off")
	themeF := flag.String("theme", "", "dark or light, instead of asking the terminal")
	dump := flag.Bool("dump", false, "print one frame and exit")
	at := flag.Float64("t", 5, "story time for -dump, in seconds")
	colsF := flag.Int("cols", 110, "width for -dump")
	rowsF := flag.Int("rows", 44, "height for -dump")
	sheet := flag.String("sheet", "", "write a PNG of every renderer and state to this file and exit")
	flag.Parse()

	if *sheet != "" {
		if err := writeSheet(*sheet); err != nil {
			fmt.Fprintln(os.Stderr, "prototype-bots:", err)
			os.Exit(1)
		}
		return
	}

	v := &view{rows: map[string]int{}, cache: map[string]*sprite{}, ids: map[string]int{}, cellW: 10, cellH: 20,
		width: *colsF, height: *rowsF}
	v.kitty, v.why = kittySupport()
	switch *kittyF {
	case "on":
		v.kitty, v.why = true, "-kitty on"
	case "off":
		v.kitty, v.why = false, "-kitty off"
	}
	v.setTheme(*themeF != "light")
	for i, vr := range variants {
		if strings.EqualFold(vr.key, *variantF) {
			v.variant = i
		}
		if !v.kitty && vr.renderers[0] == "kitty" {
			v.renderer[i] = 1
		}
	}
	for i, r := range variants[v.variant].renderers {
		if r == *rendF {
			v.renderer[v.variant] = i
		}
	}

	p := newPlayer(newStory())
	if *dump {
		p.seek(*at)
		os.Stdout.WriteString(v.dump(p))
		return
	}
	if err := v.run(p, *themeF == ""); err != nil {
		fmt.Fprintln(os.Stderr, "prototype-bots:", err)
		os.Exit(1)
	}
}

// kittySupport guesses from the environment alone, so it sends the terminal no query.
func kittySupport() (bool, string) {
	tp, t := os.Getenv("TERM_PROGRAM"), os.Getenv("TERM")
	switch {
	case os.Getenv("TMUX") != "":
		return false, "inside tmux"
	case tp == "ghostty" || tp == "WezTerm":
		return true, tp
	case strings.Contains(t, "kitty") || os.Getenv("KITTY_WINDOW_ID") != "":
		return true, "kitty"
	case strings.Contains(t, "ghostty") || os.Getenv("GHOSTTY_RESOURCES_DIR") != "":
		return true, "ghostty"
	case tp != "":
		return false, tp
	}
	return false, "TERM=" + t
}

func (v *view) setTheme(dark bool) {
	v.dark = dark
	if dark {
		v.fg, v.bg, v.acc = rgb{0xf5, 0xef, 0xe5}, rgb{0x14, 0x11, 0x0e}, lamplight
	} else {
		v.fg, v.bg, v.acc = rgb{0x24, 0x23, 0x1f}, rgb{0xf5, 0xf2, 0xeb}, paper
	}
}

var oscColour = regexp.MustCompile(`\x1b\]1([01]);rgb:([0-9a-fA-F]+)/([0-9a-fA-F]+)/([0-9a-fA-F]+)`)

// askTheme asks the terminal for its colours: kitty images need the real foreground, and the
// accent depends on whether the background is dark.
func (v *view) askTheme(w *bufio.Writer, keys <-chan []byte) {
	w.WriteString("\x1b]10;?\x1b\\\x1b]11;?\x1b\\")
	w.Flush()
	var got []byte
	deadline := time.After(300 * time.Millisecond)
wait:
	for {
		select {
		case b, ok := <-keys:
			if !ok {
				break wait
			}
			got = append(got, b...)
			if len(oscColour.FindAllSubmatch(got, -1)) >= 2 {
				break wait
			}
		case <-deadline:
			break wait
		}
	}
	var fg, bg *rgb
	for _, m := range oscColour.FindAllSubmatch(got, -1) {
		c := rgb{channel(m[2]), channel(m[3]), channel(m[4])}
		if m[1][0] == '0' {
			fg = &c
		} else {
			bg = &c
		}
	}
	if bg != nil {
		v.setTheme(0.2126*float64(bg.r)+0.7152*float64(bg.g)+0.0722*float64(bg.b) < 128)
		v.bg = *bg
		v.why += ", colours asked"
	}
	if fg != nil {
		v.fg = *fg
	}
}

func channel(h []byte) uint8 {
	n, _ := strconv.ParseUint(string(h), 16, 64)
	return uint8(n * 255 / (1<<(4*len(h)) - 1))
}

func (v *view) measure(fd int) {
	ws, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ)
	if err != nil || ws.Col == 0 || ws.Row == 0 {
		return
	}
	v.width, v.height = int(ws.Col), int(ws.Row)
	if ws.Xpixel > 0 && ws.Ypixel > 0 {
		v.cellW, v.cellH = int(ws.Xpixel)/v.width, int(ws.Ypixel)/v.height
	}
}

// forget deletes every image the viewer sent, so the next frame draws them again.
func (v *view) forget() string {
	var sb strings.Builder
	if v.kitty {
		for _, id := range v.ids {
			fmt.Fprintf(&sb, "\x1b_Ga=d,d=I,i=%d,q=2\x1b\\", id)
		}
		fmt.Fprintf(&sb, "\x1b_Ga=d,d=I,i=%d,q=2\x1b\\", liveID)
	}
	v.ids, v.cache = map[string]int{}, map[string]*sprite{}
	return sb.String()
}

func (v *view) imageID(sb *strings.Builder, sp *sprite) int {
	if sp.key == "" {
		kittyTransmit(sb, liveID, sp.img)
		return liveID
	}
	if id, ok := v.ids[sp.key]; ok {
		return id
	}
	id := len(v.ids) + 1
	v.ids[sp.key] = id
	kittyTransmit(sb, id, sp.img)
	return id
}

func parseKeys(b []byte) []string {
	var keys []string
	for i := 0; i < len(b); i++ {
		c := b[i]
		if c == 0x1b && i+1 < len(b) {
			switch b[i+1] {
			case '[', 'O':
				j := i + 2
				for j < len(b) && (b[j] < 0x40 || b[j] > 0x7e) {
					j++
				}
				if j < len(b) {
					switch b[j] {
					case 'A':
						keys = append(keys, "up")
					case 'B':
						keys = append(keys, "down")
					case 'C':
						keys = append(keys, "right")
					case 'D':
						keys = append(keys, "left")
					}
				}
				i = j
			case ']', '_', 'P':
				if j := bytes.Index(b[i:], []byte("\x1b\\")); j >= 0 {
					i += j + 1
				} else if j := bytes.IndexByte(b[i:], 7); j >= 0 {
					i += j
				} else {
					i = len(b)
				}
			}
			continue
		}
		switch {
		case c == 3:
			keys = append(keys, "ctrl-c")
		case c >= 0x20 && c < 0x7f:
			keys = append(keys, string(c))
		}
	}
	return keys
}

func (v *view) run(p *player, ask bool) error {
	in, out := int(os.Stdin.Fd()), int(os.Stdout.Fd())
	if !term.IsTerminal(in) || !term.IsTerminal(out) {
		return errors.New("needs a terminal, or try -dump")
	}
	old, err := term.MakeRaw(in)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(os.Stdout, 1<<16)
	var once sync.Once
	restore := func() {
		once.Do(func() {
			w.WriteString(v.forget())
			w.WriteString("\x1b[0m\x1b[?7h\x1b[?25h\x1b[?1049l")
			w.Flush()
			term.Restore(in, old)
		})
	}
	defer restore()
	defer func() {
		if r := recover(); r != nil {
			restore()
			panic(r)
		}
	}()
	w.WriteString("\x1b[?1049h\x1b[?25l\x1b[?7l\x1b[2J")
	w.Flush()

	keys := make(chan []byte, 64)
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				keys <- bytes.Clone(buf[:n])
			}
			if err != nil {
				close(keys)
				return
			}
		}
	}()
	if ask {
		v.askTheme(w, keys)
	}
	v.measure(out)

	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGWINCH, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	tick := time.NewTicker(33 * time.Millisecond)
	defer tick.Stop()
	paused, last := false, time.Now()
	for {
		select {
		case b, ok := <-keys:
			if !ok {
				return nil
			}
			for _, k := range parseKeys(b) {
				switch k {
				case "q", "ctrl-c":
					return nil
				case "left", "h":
					v.variant = (v.variant + len(variants) - 1) % len(variants)
				case "right", "l":
					v.variant = (v.variant + 1) % len(variants)
				case "r":
					v.cycle(1)
				case "R":
					v.cycle(-1)
				case "t":
					v.acc = map[rgb]rgb{lamplight: paper, paper: lamplight}[v.acc]
					w.WriteString(v.forget())
				case "+", "=", "up":
					v.resize(1)
				case "-", "_", "down":
					v.resize(-1)
				case "n":
					p.scene(1)
				case "p":
					p.scene(-1)
				case " ":
					paused = !paused
				}
			}
		case s := <-sigs:
			if s != syscall.SIGWINCH {
				return nil
			}
			v.measure(out)
			w.WriteString(v.forget())
			w.WriteString("\x1b[2J")
		case now := <-tick.C:
			if !paused {
				p.advance(now.Sub(last).Seconds())
			}
			last = now
		}
		w.WriteString(v.draw(p, paused))
		w.Flush()
	}
}

func (v *view) bar(p *player, f frame, paused bool) []row {
	vr := variants[v.variant]
	title := fmt.Sprintf(" ◀ %s · %s ▶   %s", vr.key, vr.name, vr.thesis)
	if pad := v.width - visWidth(title); pad > 0 {
		title += strings.Repeat(" ", pad)
	}
	clock := "playing"
	if paused {
		clock = "paused"
	}
	size := ""
	if vr.rows != nil {
		size = fmt.Sprintf(" · %d rows", v.spriteRows())
	}
	images := "kitty images on: " + v.why
	if !v.kitty {
		images = "kitty images off: " + v.why
	}
	info := fmt.Sprintf(" scene %d/%d · %.1fs %s · bot %s · %s%s · %s",
		max(1, f.scene), len(p.st.scenes), p.t, clock, p.wide.current, v.rend(), size, images)
	keys := " ←/→ variant · r renderer · +/- size · n/p scene · t accent · space pause · q quit"
	return []row{{text: "\x1b[7m" + title + "\x1b[27m"}, {text: info}, {text: dim(keys)}}
}

// draw paints the whole screen. A terminal shows the end of what was printed, so when the
// transcript is taller than the screen its top scrolls off, and images crop with it.
func (v *view) draw(p *player, paused bool) string {
	f := p.st.at(p.t)
	bar := v.bar(p, f, paused)
	content := variants[v.variant].draw(v, f, p)
	top := max(0, len(content)-max(0, v.height-len(bar)))
	screen := append(bar, content[top:]...)

	var sb strings.Builder
	sb.WriteString("\x1b[?2026h")
	if v.kitty {
		sb.WriteString("\x1b_Ga=d,d=a,q=2\x1b\\")
	}
	for i := 0; i < v.height; i++ {
		fmt.Fprintf(&sb, "\x1b[%d;1H\x1b[0m", i+1)
		if i < len(screen) {
			sb.WriteString(truncate(screen[i].text, v.width))
		}
		sb.WriteString("\x1b[0m\x1b[K")
	}
	if v.kitty {
		for i, r := range content {
			for _, pl := range r.imgs {
				first := len(bar) + i - top
				lo, hi := max(first, len(bar)), min(first+pl.sp.rows, v.height)
				if hi <= lo || pl.col+pl.sp.cols > v.width {
					continue
				}
				id := v.imageID(&sb, pl.sp)
				fmt.Fprintf(&sb, "\x1b[%d;%dH", lo+1, pl.col+1)
				if lo == first && hi == first+pl.sp.rows {
					kittyPlace(&sb, id, pl.sp.cols, pl.sp.rows)
				} else {
					fmt.Fprintf(&sb, "\x1b_Ga=p,i=%d,y=%d,h=%d,c=%d,r=%d,C=1,q=2\x1b\\",
						id, (lo-first)*v.cellH, (hi-lo)*v.cellH, pl.sp.cols, hi-lo)
				}
			}
		}
	}
	sb.WriteString("\x1b[?2026l")
	return sb.String()
}

// dump prints one frame as plain lines, placing images inline, for checking without a viewer.
func (v *view) dump(p *player) string {
	f := p.st.at(p.t)
	rows := append(v.bar(p, f, false), variants[v.variant].draw(v, f, p)...)
	var sb strings.Builder
	for _, r := range rows {
		if v.kitty {
			for _, pl := range r.imgs {
				sb.WriteString("\r")
				if pl.col > 0 {
					fmt.Fprintf(&sb, "\x1b[%dC", pl.col)
				}
				kittyPlace(&sb, v.imageID(&sb, pl.sp), pl.sp.cols, pl.sp.rows)
			}
			sb.WriteString("\r")
		}
		sb.WriteString(truncate(r.text, v.width))
		sb.WriteString("\x1b[0m\n")
	}
	return sb.String()
}
