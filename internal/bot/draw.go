package bot

import (
	"image"
	"image/color"
	"math"
)

// Palette is what a frame is drawn in. Only Edge's alpha counts, and Eyes' says whether to
// fill them.
type Palette struct {
	Body color.NRGBA
	// Eyes fills the eyes. With alpha 0 they're holes, as in the brand's SVG.
	Eyes color.NRGBA
	// Accent is the snapshot outlines.
	Accent color.NRGBA
	// Edge is a pixel-wide line around the outside of the body, so the bot stands out on a
	// background as light as its body. With alpha 0 there's none.
	Edge color.NRGBA
}

// viewBox is hibernaut-bot.js's tight viewBox, with room above the body for a hop.
var viewBox = struct{ x, y, w, h float64 }{-6, -14, 112, 116}

// Draw draws the bot as it is now into a w by h image, centred and as big as fits. Below 48
// pixels high, the eyes snap to whole pixels, so a small face stays crisp.
func (b *Bot) Draw(w, h int, p Palette) *image.NRGBA {
	im := image.NewNRGBA(image.Rect(0, 0, w, h))
	if w <= 0 || h <= 0 {
		return im
	}
	s := math.Min(float64(w)/viewBox.w, float64(h)/viewBox.h)
	ox, oy := (float64(w)-viewBox.w*s)/2, (float64(h)-viewBox.h*s)/2
	var sn *snapper
	stroke := 0.0
	if h < 48 {
		sn = &snapper{s, ox, oy}
		stroke = 0.6 / s
	}

	var layers []layer
	for _, snap := range b.snapshots {
		k := (b.time - snap.born) / snapshotLife
		if k < 0 || k >= 1 {
			continue
		}
		o := -9 * float64(snap.i) * easeOut(k)
		op := math.Sin(math.Pi*k) * 0.55
		if snap.i == 1 {
			op = math.Sin(math.Pi*k) * 0.9
		}
		half := math.Max(1.5, stroke)
		layers = append(layers, func(x, y float64) (float64, int) {
			if math.Abs(sdBody(x-o, y-o)) <= half {
				return op, accentInk
			}
			return 0, 0
		})
	}
	body := prep(&b.body, sn, p.Eyes.A != 0)
	if p.Edge.A != 0 {
		edge, width := float64(p.Edge.A)/255, 1/s
		layers = append(layers, func(x, y float64) (float64, int) {
			if d := body.distance(x, y); d > 0 && d <= width {
				return edge, edgeInk
			}
			return 0, 0
		})
	}
	layers = append(layers, body.cover)

	n := 4
	if w*h > 4000 {
		n = 3
	}
	if w*h > 20000 {
		n = 2
	}
	inv := 1 / float64(n*n)
	var sum [inks]float64
	for py := range h {
		for px := range w {
			for j := range n {
				for i := range n {
					x := viewBox.x + (float64(px)+(float64(i)+0.5)/float64(n)-ox)/s
					y := viewBox.y + (float64(py)+(float64(j)+0.5)/float64(n)-oy)/s
					var c [inks]float64
					for _, l := range layers {
						o, ink := l(x, y)
						if o <= 0 {
							continue
						}
						// Each layer covers what's under it.
						for k := range c {
							c[k] *= 1 - o
						}
						c[ink] += o
					}
					for k := range sum {
						sum[k] += c[k]
					}
				}
			}
			for k := range sum {
				sum[k] *= inv
			}
			paint(im, px, py, sum, [inks]color.NRGBA{p.Body, p.Eyes, p.Accent, p.Edge})
			sum = [inks]float64{}
		}
	}
	return im
}

const (
	bodyInk = iota
	eyesInk
	accentInk
	edgeInk
	inks
)

// paint mixes the inks by how much of the pixel each covers.
func paint(im *image.NRGBA, x, y int, cover [inks]float64, ink [inks]color.NRGBA) {
	var t float64
	for _, c := range cover {
		t += c
	}
	if t <= 0.002 {
		return
	}
	mix := func(channel func(color.NRGBA) uint8) uint8 {
		var v float64
		for k, c := range cover {
			v += c * float64(channel(ink[k]))
		}
		return uint8(math.Round(v / t))
	}
	i := im.PixOffset(x, y)
	im.Pix[i] = mix(func(c color.NRGBA) uint8 { return c.R })
	im.Pix[i+1] = mix(func(c color.NRGBA) uint8 { return c.G })
	im.Pix[i+2] = mix(func(c color.NRGBA) uint8 { return c.B })
	im.Pix[i+3] = uint8(math.Round(math.Min(1, t) * 255))
}

// layer says how much of a viewBox point it covers, and in which ink.
type layer func(x, y float64) (cover float64, ink int)

func easeOut(k float64) float64 { return 1 - math.Pow(1-k, 3) }

func inRoundRect(x, y, x0, y0, w, h, r float64) bool {
	if x < x0 || y < y0 || x > x0+w || y > y0+h {
		return false
	}
	dx := math.Max(math.Max(x0+r-x, x-(x0+w-r)), 0)
	dy := math.Max(math.Max(y0+r-y, y-(y0+h-r)), 0)
	return dx*dx+dy*dy <= r*r
}

// sdBody is the signed distance to the body's outline, a 100 by 100 square with corner radius 13.3.
func sdBody(x, y float64) float64 {
	qx := math.Abs(x-50) - 50 + 13.3
	qy := math.Abs(y-50) - 50 + 13.3
	return math.Hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - 13.3
}

// snapper moves the eyes to whole pixels, so a coarse grid draws them as crisp blocks instead of
// whatever their edge pixels happen to round to.
type snapper struct{ s, ox, oy float64 }

func (sn *snapper) eye(m *body, cx, cy, w, h float64) (x, y, ew, eh float64) {
	r := m.rot.v * math.Pi / 180
	c, s := math.Cos(r), math.Sin(r)
	x, y = (cx-50)*m.sx.v, (cy-100)*m.sy.v
	x, y = x*c-y*s+m.tx.v+50, x*s+y*c+m.ty.v+100
	px, py := (x-viewBox.x)*sn.s+sn.ox, (y-viewBox.y)*sn.s+sn.oy
	nw, nh := whole(w*m.sx.v*sn.s, 0), whole(h*m.sy.v*sn.s, 0.15)
	if nw == 0 || nh == 0 {
		return cx, cy, 0, 0
	}
	px, py = centre(px, nw), centre(py, nh)
	x, y = (px-sn.ox)/sn.s+viewBox.x-m.tx.v-50, (py-sn.oy)/sn.s+viewBox.y-m.ty.v-100
	x, y = x*c+y*s, -x*s+y*c
	return x/m.sx.v + 50, y/m.sy.v + 100, nw / (sn.s * m.sx.v), nh / (sn.s * m.sy.v)
}

// whole rounds a size in pixels to whole pixels, or to none when it's almost shut.
func whole(v, bias float64) float64 {
	if v < 0.35 {
		return 0
	}
	return math.Max(1, math.Round(v+bias))
}

// centre puts n pixels' middle on a pixel's middle when n is odd, and on an edge when it's even.
func centre(c, n float64) float64 {
	if int(n)%2 == 1 {
		return math.Floor(c) + 0.5
	}
	return math.Round(c)
}

type eye struct{ x, y, w, h, r float64 }

// prepared is a body with its sines, scales and eyes worked out once a frame.
type prepared struct {
	tx, ty, c, s, isx, isy float64
	eyes                   [2]eye
	filled                 bool
}

func prep(m *body, sn *snapper, filled bool) prepared {
	p := prepared{tx: m.tx.v, ty: m.ty.v, isx: 1 / m.sx.v, isy: 1 / m.sy.v, filled: filled}
	r := -m.rot.v * math.Pi / 180
	p.c, p.s = math.Cos(r), math.Sin(r)
	w := math.Max(0.4, m.ew.v)
	h := math.Max(0.4, m.eh.v*m.lid.v)
	rr := math.Min(3.4, math.Min(w/2, h/2))
	for i, x := range [2]float64{m.lx.v, m.rx.v} {
		cx, cy := x+m.gx.v, m.ey.v+m.gy.v
		if sn != nil {
			x, y, ew, eh := sn.eye(m, cx, cy, w, h)
			p.eyes[i] = eye{x - ew/2, y - eh/2, ew, eh, 0}
			continue
		}
		p.eyes[i] = eye{cx - w/2, cy - h/2, w, h, rr}
	}
	return p
}

// local turns a viewBox point into the body's own, as it was before it moved, turned and squashed.
func (p *prepared) local(x, y float64) (float64, float64) {
	x -= p.tx + 50
	y -= p.ty + 100
	x, y = x*p.c-y*p.s, x*p.s+y*p.c
	return x*p.isx + 50, y*p.isy + 100
}

// distance is roughly how far a viewBox point is outside the body.
func (p *prepared) distance(x, y float64) float64 {
	return sdBody(p.local(x, y))
}

// cover covers the body at a viewBox point, and the eyes when they're filled.
func (p *prepared) cover(x, y float64) (float64, int) {
	x, y = p.local(x, y)
	if !inRoundRect(x, y, 0, 0, 100, 100, 13.3) {
		return 0, 0
	}
	for _, e := range p.eyes {
		if e.w > 0 && inRoundRect(x, y, e.x, e.y, e.w, e.h, e.r) {
			if p.filled {
				return 1, eyesInk
			}
			return 0, 0
		}
	}
	return 1, bodyInk
}
