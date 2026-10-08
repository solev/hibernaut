package main

import (
	"image"
	"image/png"
	"math"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

// writeSheet paints every renderer in every state the way a terminal would, cell by cell from the
// encoded output, so the pixels can be checked without a terminal. Rows from the top: B's half,
// sextant and kitty, C's half, sextant and kitty, A's kitty, inverse and eyes faces.
func writeSheet(path string) error {
	const cw, ch, gap = 12, 24, 18
	fg, bg, page := rgb{0xf5, 0xef, 0xe5}, rgb{0x14, 0x11, 0x0e}, rgb{0x3a, 0x33, 0x2c}
	states := []struct {
		name string
		t    float64
	}{{"asleep", 1.3}, {"wake", 0.4}, {"idle", 0.5}, {"working", 0.6}, {"happy", 0.45}, {"fork", 1.5}, {"hibernate", 2.2}}
	type spec struct {
		rend       string
		vb         viewBox
		cols, rows int
		tight      bool
	}
	specs := []spec{
		{"half", vbWide, 0, 9, false}, {"sextant", vbWide, 0, 6, false}, {"kitty", vbWide, 0, 5, false},
		{"half", vbFace, 0, 5, true}, {"sextant", vbFace, 0, 4, true}, {"kitty", vbFace, 0, 3, true},
		{"kitty", vbTight, 2, 1, true}, {"inverse", vbTight, 2, 1, true}, {"eyes", vbTight, 2, 1, true},
	}
	tiles := make([][]*image.NRGBA, len(specs))
	colW, rowH := make([]int, len(states)), make([]int, len(specs))
	for i, sp := range specs {
		for j, st := range states {
			b := played(st.name, sp.tight, st.t)
			var im *image.NRGBA
			switch sp.rend {
			case "inverse", "eyes":
				im = paintLines([]string{textFace(sp.rend, b)}, 2, cw, ch, fg, bg)
			default:
				s := makeSprite(sp.rend, b, sp.vb, sp.cols, sp.rows, cw, ch, fg, lamplight)
				if s.img != nil {
					im = over(s.img, bg)
				} else {
					im = paintLines(s.lines, s.cols, cw, ch, fg, bg)
				}
			}
			tiles[i] = append(tiles[i], im)
			colW[j], rowH[i] = max(colW[j], im.Rect.Dx()), max(rowH[i], im.Rect.Dy())
		}
	}
	w, h := gap, gap
	for _, x := range colW {
		w += x + gap
	}
	for _, y := range rowH {
		h += y + gap
	}
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(out.Pix); i += 4 {
		out.Pix[i], out.Pix[i+1], out.Pix[i+2], out.Pix[i+3] = page.r, page.g, page.b, 255
	}
	y := gap
	for i := range specs {
		x := gap
		for j := range states {
			t := tiles[i][j]
			for ty := 0; ty < t.Rect.Dy(); ty++ {
				copy(out.Pix[out.PixOffset(x, y+ty):], t.Pix[t.PixOffset(0, ty):t.PixOffset(0, ty)+t.Rect.Dx()*4])
			}
			x += colW[j] + gap
		}
		y += rowH[i] + gap
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, out); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func over(im *image.NRGBA, bg rgb) *image.NRGBA {
	out := image.NewNRGBA(im.Rect)
	for i := 0; i < len(im.Pix); i += 4 {
		a := float64(im.Pix[i+3]) / 255
		mix := func(c, b uint8) uint8 { return uint8(float64(c)*a + float64(b)*(1-a) + 0.5) }
		out.Pix[i], out.Pix[i+1], out.Pix[i+2], out.Pix[i+3] = mix(im.Pix[i], bg.r), mix(im.Pix[i+1], bg.g), mix(im.Pix[i+2], bg.b), 255
	}
	return out
}

type paintedCell struct {
	r      rune
	fg, bg rgb
}

// parseCells reads the SGR codes the encoders write and returns each cell's glyph and colours.
func parseCells(line string, fg, bg rgb) []paintedCell {
	curFg, curBg := fg, bg
	rev, faint := false, false
	var cells []paintedCell
	for i := 0; i < len(line); {
		if line[i] == 0x1b {
			j := max(i+1, escapeEnd(line, i))
			if j-i > 2 && line[j-1] == 'm' && line[i+1] == '[' {
				ps := strings.Split(line[i+2:j-1], ";")
				for k := 0; k < len(ps); k++ {
					switch ps[k] {
					case "0", "":
						curFg, curBg, rev, faint = fg, bg, false, false
					case "39":
						curFg = fg
					case "49":
						curBg = bg
					case "7":
						rev = true
					case "27":
						rev = false
					case "2":
						faint = true
					case "22":
						faint = false
					case "38", "48":
						if k+4 < len(ps) && ps[k+1] == "2" {
							n := func(s string) uint8 { v, _ := strconv.Atoi(s); return uint8(v) }
							c := rgb{n(ps[k+2]), n(ps[k+3]), n(ps[k+4])}
							if ps[k] == "38" {
								curFg = c
							} else {
								curBg = c
							}
							k += 4
						}
					}
				}
			}
			i = j
			continue
		}
		r, size := utf8.DecodeRuneInString(line[i:])
		i += size
		f, b := curFg, curBg
		if faint {
			f = rgb{uint8((int(f.r) + int(b.r)) / 2), uint8((int(f.g) + int(b.g)) / 2), uint8((int(f.b) + int(b.b)) / 2)}
		}
		if rev {
			f, b = b, f
		}
		cells = append(cells, paintedCell{r, f, b})
	}
	return cells
}

func paintLines(lines []string, cols, cw, ch int, fg, bg rgb) *image.NRGBA {
	im := image.NewNRGBA(image.Rect(0, 0, cols*cw, len(lines)*ch))
	for li, line := range lines {
		for ci, c := range parseCells(line, fg, bg) {
			if ci >= cols {
				break
			}
			for y := 0; y < ch; y++ {
				for x := 0; x < cw; x++ {
					col := c.bg
					if covers(c.r, (float64(x)+0.5)/float64(cw), (float64(y)+0.5)/float64(ch)) {
						col = c.fg
					}
					o := im.PixOffset(ci*cw+x, li*ch+y)
					im.Pix[o], im.Pix[o+1], im.Pix[o+2], im.Pix[o+3] = col.r, col.g, col.b, 255
				}
			}
		}
	}
	return im
}

// covers says whether glyph r inks the point x, y of its cell, both 0 to 1. The text glyphs are
// rough copies of a typical monospace font.
func covers(r rune, x, y float64) bool {
	switch {
	case r == '█':
		return true
	case r == '▀':
		return y < 0.5
	case r == '▄':
		return y >= 0.5
	case r == '▌':
		return x < 0.5
	case r == '▐':
		return x >= 0.5
	case r >= 0x1FB00 && r <= 0x1FB3B:
		m := int(r-0x1FB00) + 1
		if m >= 21 {
			m++
		}
		if m >= 42 {
			m++
		}
		bit := int(y*3) * 2
		if x >= 0.5 {
			bit++
		}
		return m&(1<<bit) != 0
	case r == '▪':
		return math.Abs(x-0.5) < 0.22 && math.Abs(y-0.55) < 0.11
	case r == '▬':
		return math.Abs(x-0.5) < 0.42 && math.Abs(y-0.55) < 0.07
	case r == '^':
		return math.Min(segDist(x, y, 0.2, 0.5, 0.5, 0.25), segDist(x, y, 0.5, 0.25, 0.8, 0.5)) < 0.07
	}
	return false
}

func segDist(x, y, ax, ay, bx, by float64) float64 {
	vx, vy := bx-ax, by-ay
	t := clamp(((x-ax)*vx+(y-ay)*vy)/(vx*vx+vy*vy), 0, 1)
	return math.Hypot(x-ax-t*vx, y-ay-t*vy)
}
