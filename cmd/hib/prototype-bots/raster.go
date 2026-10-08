package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"math"
	"strings"
)

type viewBox struct{ x, y, w, h float64 }

var (
	// vbTight is hibernaut-bot.js's tight viewBox, with room for a hop.
	vbTight = viewBox{-6, -14, 112, 116}
	// vbFace frames the body alone, for bots that never move.
	vbFace = viewBox{-3, -3, 106, 106}
	// vbWide has room for the Zs, a fork and the snapshot outlines.
	vbWide = viewBox{-45, -50, 192, 154}
)

type rgb struct{ r, g, b uint8 }

func (c rgb) fg() string { return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", c.r, c.g, c.b) }
func (c rgb) bg() string { return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", c.r, c.g, c.b) }

var (
	lamplight = rgb{0xf3, 0xc7, 0x7c}
	paper     = rgb{0x17, 0x5d, 0x80}
)

// grid holds each pixel's coverage by the body ink and the accent ink, 0 to 1.
type grid struct {
	w, h         int
	body, accent []float64
}

type ink uint8

const (
	inkNone ink = iota
	inkBody
	inkAccent
)

func (g *grid) at(x, y int) ink {
	i := y*g.w + x
	b, a := g.body[i], g.accent[i]
	switch {
	case b+a < 0.5:
		return inkNone
	case a > b:
		return inkAccent
	default:
		return inkBody
	}
}

func clamp(x, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, x)) }

func easeOut(k float64) float64 { return 1 - math.Pow(1-k, 3) }

func inRoundRect(x, y, x0, y0, w, h, r float64) bool {
	if x < x0 || y < y0 || x > x0+w || y > y0+h {
		return false
	}
	dx := math.Max(math.Max(x0+r-x, x-(x0+w-r)), 0)
	dy := math.Max(math.Max(y0+r-y, y-(y0+h-r)), 0)
	return dx*dx+dy*dy <= r*r
}

// sdBody is the signed distance to the body outline, a 100 by 100 square with corner radius 13.3.
func sdBody(x, y float64) float64 {
	qx := math.Abs(x-50) - 50 + 13.3
	qy := math.Abs(y-50) - 50 + 13.3
	return math.Hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - 13.3
}

// arcPts flattens the happy eye, M-8 3.5Q0-6 8 3.5.
var arcPts = func() [][2]float64 {
	pts := make([][2]float64, 25)
	for i := range pts {
		t := float64(i) / 24
		u := 1 - t
		pts[i] = [2]float64{u*u*-8 + t*t*8, u*u*3.5 + 2*u*t*-6 + t*t*3.5}
	}
	return pts
}()

func nearArc(x, y, r float64) bool {
	if x < -8-r || x > 8+r || y < -1.25-r || y > 3.5+r {
		return false
	}
	for i := 1; i < len(arcPts); i++ {
		a, b := arcPts[i-1], arcPts[i]
		vx, vy := b[0]-a[0], b[1]-a[1]
		t := clamp(((x-a[0])*vx+(y-a[1])*vy)/(vx*vx+vy*vy), 0, 1)
		dx, dy := x-a[0]-t*vx, y-a[1]-t*vy
		if dx*dx+dy*dy <= r*r {
			return true
		}
	}
	return false
}

// hints keep features at least a pixel big when the grid is coarse, as a pixel artist would.
type hints struct{ stroke, minZ float64 }

// snapper moves eyes to whole pixels, so a coarse grid draws them as crisp blocks instead of
// whatever the edge pixels happen to round to.
type snapper struct {
	vb            viewBox
	s, ox, oy, pa float64
}

func (sn *snapper) eye(m *body, cx, cy, w, h float64) (x, y, ew, eh float64) {
	r := m.rot.v * math.Pi / 180
	c, s := math.Cos(r), math.Sin(r)
	x, y = (cx-50)*m.sx.v, (cy-100)*m.sy.v
	x, y = x*c-y*s+m.tx.v+50, x*s+y*c+m.ty.v+100
	px, py := ((x-sn.vb.x)*sn.s+sn.ox)/sn.pa, (y-sn.vb.y)*sn.s+sn.oy
	nw, nh := whole(w*m.sx.v*sn.s/sn.pa, 0), whole(h*m.sy.v*sn.s, 0.15)
	if nw == 0 || nh == 0 {
		return cx, cy, 0, 0
	}
	px, py = centre(px, nw), centre(py, nh)
	x, y = (px*sn.pa-sn.ox)/sn.s+sn.vb.x-m.tx.v-50, (py-sn.oy)/sn.s+sn.vb.y-m.ty.v-100
	x, y = x*c+y*s, -x*s+y*c
	return x/m.sx.v + 50, y/m.sy.v + 100, nw * sn.pa / (sn.s * m.sx.v), nh / (sn.s * m.sy.v)
}

// whole rounds a size in pixels to whole pixels, or to none when it's almost shut.
func whole(v, bias float64) float64 {
	if v < 0.35 {
		return 0
	}
	return math.Max(1, math.Round(v+bias))
}

func centre(c, n float64) float64 {
	if int(n)%2 == 1 {
		return math.Floor(c) + 0.5
	}
	return math.Round(c)
}

type prepBody struct {
	tx, ty, c, s, isx, isy, op float64
	eyes                       [2][5]float64
	arc, arcK, arcR, arcCy     float64
	arcCx                      [2]float64
}

func prep(m *body, hn hints, sn *snapper) prepBody {
	p := prepBody{tx: m.tx.v, ty: m.ty.v, op: clamp(m.op.v, 0, 1), isx: 1 / m.sx.v, isy: 1 / m.sy.v}
	r := -m.rot.v * math.Pi / 180
	p.c, p.s = math.Cos(r), math.Sin(r)
	arc := clamp(m.arc.v, 0, 1)
	w := math.Max(0.4, m.ew.v)
	h := math.Max(0.4, m.eh.v*m.lid.v*(1-arc))
	rr := math.Min(3.4, math.Min(w/2, h/2))
	for i, e := range [2][2]float64{{m.lx.v, m.dl.v}, {m.rx.v, m.dr.v}} {
		cx, cy := e[0]+m.gx.v, m.ey.v+m.gy.v+e[1]
		if sn != nil {
			x, y, ew, eh := sn.eye(m, cx, cy, w, h)
			p.eyes[i] = [5]float64{x - ew/2, y - eh/2, ew, eh, 0}
			continue
		}
		p.eyes[i] = [5]float64{cx - w/2, cy - h/2, w, h, rr}
	}
	p.arc, p.arcK, p.arcCy = arc, 0.6+0.4*arc, m.ey.v+m.gy.v
	p.arcR = math.Max(2.3, hn.stroke/p.arcK)
	p.arcCx = [2]float64{m.lx.v + m.gx.v, m.rx.v + m.gx.v}
	return p
}

// local maps a viewBox point into the body's own unscaled, unrotated frame.
func (p *prepBody) local(x, y float64) (float64, float64) {
	x -= p.tx + 50
	y -= p.ty + 100
	x, y = x*p.c-y*p.s, x*p.s+y*p.c
	return x*p.isx + 50, y*p.isy + 100
}

// dist is roughly how far a viewBox point lies outside the body, in viewBox units.
func (p *prepBody) dist(x, y float64) float64 {
	return sdBody(p.local(x, y)) / math.Max(p.isx, p.isy)
}

func (p *prepBody) cover(x, y float64) float64 {
	if p.op <= 0 {
		return 0
	}
	x, y = p.local(x, y)
	if !inRoundRect(x, y, 0, 0, 100, 100, 13.3) {
		return 0
	}
	for _, e := range p.eyes {
		if e[2] > 0 && inRoundRect(x, y, e[0], e[1], e[2], e[3], e[4]) {
			return 0
		}
	}
	if p.arc > 0.001 {
		for _, cx := range p.arcCx {
			if nearArc((x-cx)/p.arcK, (y-p.arcCy)/p.arcK, p.arcR) {
				return p.op * (1 - p.arc)
			}
		}
	}
	return p.op
}

type layer func(x, y float64) (float64, ink)

// rasterize draws b into a w by h grid of pixels that are pa times as wide as they are tall,
// with vb centred and uniformly scaled. Layers follow the SVG: clones and snapshots behind the
// main body, Zs in front.
func rasterize(b *bot, vb viewBox, w, h int, pa float64, hint bool) *grid {
	g := &grid{w: w, h: h, body: make([]float64, w*h), accent: make([]float64, w*h)}
	s := math.Min(float64(w)*pa/vb.w, float64(h)/vb.h)
	ox, oy := (float64(w)*pa-vb.w*s)/2, (float64(h)-vb.h*s)/2
	// unit is the size of one pixel in viewBox units, along its longer side.
	unit := math.Max(pa/s, 1/s)
	var hn hints
	var sn *snapper
	if hint {
		hn = hints{stroke: 0.6 * unit, minZ: 1.3 * unit}
		sn = &snapper{vb, s, ox, oy, pa}
	}

	var layers []layer
	addBody := func(m *body) {
		p := prep(m, hn, sn)
		layers = append(layers, func(x, y float64) (float64, ink) { return p.cover(x, y), inkBody })
	}
	clones := 0
	if b.clone != nil {
		addBody(b.clone)
		clones++
	}
	for _, f := range b.fx {
		if f.live && f.kind == fxClone {
			addBody(f.b)
			clones++
		}
	}
	if hint && clones > 0 {
		// A coarse grid fuses a fork into one wide bot, so leave a pixel of air around the main one.
		mp := prep(b.main, hn, sn)
		layers = append(layers, func(x, y float64) (float64, ink) {
			if d := mp.dist(x, y); d > 0 && d <= unit {
				return 1, inkNone
			}
			return 0, inkNone
		})
	}
	for _, f := range b.fx {
		if !f.live || f.kind != fxSnap {
			continue
		}
		o := -9 * float64(f.i) * easeOut(f.k)
		op := math.Sin(math.Pi*f.k) * 0.55
		if f.i == 1 {
			op = math.Sin(math.Pi*f.k) * 0.9
		}
		half := math.Max(1.5, hn.stroke)
		layers = append(layers, func(x, y float64) (float64, ink) {
			if math.Abs(sdBody(x-o, y-o)) <= half {
				return op, inkAccent
			}
			return 0, inkNone
		})
	}
	addBody(b.main)
	for _, f := range b.fx {
		if !f.live || f.kind != fxZ {
			continue
		}
		k, size := f.k, f.size
		sc := 0.6 + 0.6*easeOut(k)
		if size*sc < hn.minZ {
			sc = hn.minZ / size
		}
		zx, zy := 96+14*k+math.Sin(k*6+f.drift)*3, 2-46*easeOut(k)
		a := -(-12 + 24*k) * math.Pi / 180
		c, sn := math.Cos(a), math.Sin(a)
		rr := math.Min(3.4, size/3)
		op := math.Min(1, k/0.15) * (1 - math.Max(0, (k-0.5)/0.5))
		layers = append(layers, func(x, y float64) (float64, ink) {
			dx, dy := x-zx, y-zy
			lx, ly := (dx*c-dy*sn)/sc+size/2, (dx*sn+dy*c)/sc+size/2
			if inRoundRect(lx, ly, 0, 0, size, size, rr) {
				return op, inkAccent
			}
			return 0, inkNone
		})
	}

	n := 4
	if w*h > 4000 {
		n = 3
	}
	if w*h > 20000 {
		n = 2
	}
	inv := 1 / float64(n*n)
	for py := 0; py < h; py++ {
		for px := 0; px < w; px++ {
			var sb, sa float64
			for j := 0; j < n; j++ {
				for i := 0; i < n; i++ {
					x := vb.x + ((float64(px)+(float64(i)+0.5)/float64(n))*pa-ox)/s
					y := vb.y + (float64(py)+(float64(j)+0.5)/float64(n)-oy)/s
					var cb, ca float64
					for _, l := range layers {
						o, k := l(x, y)
						if o <= 0 {
							continue
						}
						// Each layer covers what's under it, and an inkNone layer only erases.
						cb, ca = cb*(1-o), ca*(1-o)
						switch k {
						case inkBody:
							cb += o
						case inkAccent:
							ca += o
						}
					}
					sb += cb
					sa += ca
				}
			}
			g.body[py*w+px], g.accent[py*w+px] = sb*inv, sa*inv
		}
	}
	return g
}

// pen writes cells with the fewest colour changes. Colour 0 is the terminal's own, 1 the accent.
type pen struct {
	sb     strings.Builder
	fg, bg int
	acc    rgb
}

func (p *pen) put(r rune, fg, bg int) {
	if bg != p.bg {
		if bg == 1 {
			p.sb.WriteString(p.acc.bg())
		} else {
			p.sb.WriteString("\x1b[49m")
		}
		p.bg = bg
	}
	if fg >= 0 && fg != p.fg {
		if fg == 1 {
			p.sb.WriteString(p.acc.fg())
		} else {
			p.sb.WriteString("\x1b[39m")
		}
		p.fg = fg
	}
	p.sb.WriteRune(r)
}

func (p *pen) end() string {
	if p.fg != 0 || p.bg != 0 {
		p.sb.WriteString("\x1b[39;49m")
	}
	return p.sb.String()
}

func inkColour(k ink) int {
	if k == inkAccent {
		return 1
	}
	return 0
}

// halfLines draws two pixels per cell with ▀ ▄ █. The body is the terminal's foreground, so it
// follows the theme the way the SVG's currentColor does.
func halfLines(g *grid, acc rgb) []string {
	var out []string
	for y := 0; y+1 < g.h; y += 2 {
		p := pen{acc: acc}
		for x := 0; x < g.w; x++ {
			t, b := g.at(x, y), g.at(x, y+1)
			switch {
			case t == inkNone && b == inkNone:
				p.put(' ', -1, 0)
			case t == b:
				p.put('█', inkColour(t), 0)
			case b == inkNone:
				p.put('▀', inkColour(t), 0)
			case t == inkNone:
				p.put('▄', inkColour(b), 0)
			case t == inkBody:
				p.put('▀', 0, 1)
			default:
				p.put('▄', 0, 1)
			}
		}
		out = append(out, p.end())
	}
	return out
}

func sextant(mask int) rune {
	switch mask {
	case 0:
		return ' '
	case 63:
		return '█'
	case 21:
		return '▌'
	case 42:
		return '▐'
	}
	r := 0x1FB00 + mask - 1
	if mask > 21 {
		r--
	}
	if mask > 42 {
		r--
	}
	return rune(r)
}

// sextantLines draws 2 by 3 pixels per cell. A cell holds two colours, so a cell with body,
// accent and background drops its smaller ink.
func sextantLines(g *grid, acc rgb) []string {
	var out []string
	for y := 0; y+2 < g.h; y += 3 {
		p := pen{acc: acc}
		for x := 0; x+1 < g.w; x += 2 {
			var mb, ma, nb, na int
			for j := 0; j < 3; j++ {
				for i := 0; i < 2; i++ {
					bit := 1 << (j*2 + i)
					switch g.at(x+i, y+j) {
					case inkBody:
						mb |= bit
						nb++
					case inkAccent:
						ma |= bit
						na++
					}
				}
			}
			switch {
			case nb == 0 && na == 0:
				p.put(' ', -1, 0)
			case na == 0:
				p.put(sextant(mb), 0, 0)
			case nb == 0:
				p.put(sextant(ma), 1, 0)
			case nb+na == 6:
				p.put(sextant(mb), 0, 1)
			case nb >= na:
				p.put(sextant(mb), 0, 0)
			default:
				p.put(sextant(ma), 1, 0)
			}
		}
		out = append(out, p.end())
	}
	return out
}

func kittyImage(g *grid, body, acc rgb) *image.NRGBA {
	im := image.NewNRGBA(image.Rect(0, 0, g.w, g.h))
	for i := range g.body {
		b, a := g.body[i], g.accent[i]
		t := b + a
		if t <= 0.002 {
			continue
		}
		mix := func(x, y uint8) uint8 { return uint8((b*float64(x) + a*float64(y)) / t) }
		im.Pix[i*4], im.Pix[i*4+1], im.Pix[i*4+2] = mix(body.r, acc.r), mix(body.g, acc.g), mix(body.b, acc.b)
		im.Pix[i*4+3] = uint8(math.Min(1, t) * 255)
	}
	return im
}

func encodePNG(im image.Image) []byte {
	var buf bytes.Buffer
	(&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&buf, im)
	return buf.Bytes()
}

// kittyTransmit stores an image in the terminal under id without showing it.
func kittyTransmit(sb *strings.Builder, id int, im image.Image) {
	data := base64.StdEncoding.EncodeToString(encodePNG(im))
	for first := true; len(data) > 0; first = false {
		chunk := data[:min(len(data), 4096)]
		data = data[len(chunk):]
		more := 0
		if len(data) > 0 {
			more = 1
		}
		if first {
			fmt.Fprintf(sb, "\x1b_Ga=t,f=100,i=%d,q=2,m=%d;%s\x1b\\", id, more, chunk)
		} else {
			fmt.Fprintf(sb, "\x1b_Gm=%d;%s\x1b\\", more, chunk)
		}
	}
}

// kittyPlace shows image id scaled into cols by rows cells at the cursor, leaving the cursor.
func kittyPlace(sb *strings.Builder, id, cols, rows int) {
	fmt.Fprintf(sb, "\x1b_Ga=p,i=%d,c=%d,r=%d,C=1,q=2\x1b\\", id, cols, rows)
}
