package main

import (
	"math"
	"math/rand/v2"
)

// A port of docs/brand/bot/hibernaut-bot.js: the same shapes, states and timings, stepped by dt
// instead of requestAnimationFrame. Reduced motion and pointer following are left out.

type eyeShape struct{ lx, rx, y, w, h float64 }

var shapes = map[string]eyeShape{
	"asleep":  {29, 71, 55.2, 25.6, 6.8},
	"awake":   {32, 68, 50, 13, 15},
	"think":   {34, 66, 49, 9, 9},
	"focus":   {32, 68, 53, 14, 9},
	"happy":   {32, 68, 49, 13, 15},
	"squeeze": {29, 71, 55.2, 27, 2.5},
}

var transient = map[string]string{"wake": "idle", "happy": "idle", "hibernate": "asleep"}

var glances = [][2]float64{{0, 0}, {0, 0}, {-7, 0}, {7, 0}, {-6, -4}, {6, -4}, {0, 3}}

// val approaches its target exponentially; s is 1/seconds.
type val struct{ v, t, s float64 }

func newVal(x float64) *val { return &val{v: x, t: x, s: 10} }

func (a *val) to(t, s float64) {
	a.t = t
	if s != 0 {
		a.s = s
	}
}

func (a *val) snap()           { a.v = a.t }
func (a *val) step(dt float64) { a.v += (a.t - a.v) * (1 - math.Exp(-a.s*dt)) }

type body struct {
	tx, ty, rot, gx, gy, dl, dr, arc *val
	sx, sy, op, lid                  *val
	lx, rx, ey, ew, eh               *val
	lidBase                          float64
}

func newBody(sh eyeShape) *body {
	b := &body{lidBase: 1}
	for _, p := range []**val{&b.tx, &b.ty, &b.rot, &b.gx, &b.gy, &b.dl, &b.dr, &b.arc} {
		*p = newVal(0)
	}
	for _, p := range []**val{&b.sx, &b.sy, &b.op, &b.lid} {
		*p = newVal(1)
	}
	b.lx, b.rx, b.ey, b.ew, b.eh = newVal(sh.lx), newVal(sh.rx), newVal(sh.y), newVal(sh.w), newVal(sh.h)
	return b
}

func (b *body) vals() []*val {
	return []*val{b.tx, b.ty, b.rot, b.gx, b.gy, b.dl, b.dr, b.arc, b.sx, b.sy, b.op, b.lid,
		b.lx, b.rx, b.ey, b.ew, b.eh}
}

func (b *body) shape(name string, s float64) {
	sh := shapes[name]
	b.lx.to(sh.lx, s)
	b.rx.to(sh.rx, s)
	b.ey.to(sh.y, s)
	b.ew.to(sh.w, s)
	b.eh.to(sh.h, s)
}

func (b *body) gaze(x, y, s float64) { b.gx.to(x, s); b.gy.to(y, s) }

func (b *body) pose(sx, sy, ty, s float64) { b.sx.to(sx, s); b.sy.to(sy, s); b.ty.to(ty, s) }

func (b *body) step(dt float64) {
	for _, v := range b.vals() {
		v.step(dt)
	}
}

func (b *body) snap() {
	for _, v := range b.vals() {
		v.snap()
	}
}

type fxKind int

const (
	fxZ fxKind = iota
	fxSnap
	fxClone
)

type effect struct {
	kind        fxKind
	life, born  float64
	k           float64
	live        bool
	size, drift float64
	i           int
	b           *body
}

type timed struct {
	at float64
	fn func()
}

type bot struct {
	tight      bool
	main       *body
	clone      *body
	fx         []*effect
	queue      []timed
	time       float64
	current    string
	epoch      int
	stateStart float64
	rng        *rand.Rand
	dt         float64

	nextZ, nextBlink, nextGlance, nextSaccade float64
	col, zi                                   int
}

func newBot(state string, tight bool, seed uint64) *bot {
	b := &bot{tight: tight, zi: -1, rng: rand.New(rand.NewPCG(seed, 7))}
	b.main = newBody(shapes["asleep"])
	b.become(state)
	b.main.snap()
	return b
}

func (b *bot) rand(lo, hi float64) float64 { return lo + b.rng.Float64()*(hi-lo) }

func (b *bot) become(name string) {
	st, ok := states[name]
	if !ok {
		return
	}
	b.current = name
	b.epoch++
	b.queue = nil
	b.stateStart = b.time
	if name != "fork" {
		b.mergeClone(false)
	}
	b.main.arc.to(0, 12)
	st.enter(b, b.main)
}

func (b *bot) settle() {
	if next := transient[b.current]; next != "" {
		b.become(next)
	}
}

func (b *bot) at(delay float64, fn func()) { b.queue = append(b.queue, timed{b.time + delay, fn}) }

func (b *bot) blink(m *body) {
	m.lid.to(0.06, 45)
	b.at(0.09, func() { m.lid.to(m.lidBase, 22) })
}

func (b *bot) spawnClone() *body {
	b.mergeClone(true)
	c := newBody(shapes["awake"])
	c.op.v = 0
	b.clone = c
	return c
}

func (b *bot) mergeClone(instant bool) {
	c := b.clone
	if c == nil {
		return
	}
	b.clone = nil
	b.main.tx.to(0, 7)
	if instant {
		return
	}
	c.tx.to(0, 8)
	c.sx.to(0.6, 8)
	c.sy.to(0.6, 8)
	c.op.to(0, 6)
	b.fx = append(b.fx, &effect{kind: fxClone, life: 0.7, born: b.time, b: c})
}

func (b *bot) spawnZ() {
	b.zi = (b.zi + 1) % 3
	size := []float64{6, 8, 10}[b.zi]
	b.fx = append(b.fx, &effect{kind: fxZ, life: 3.2, born: b.time, size: size, drift: b.rand(-2, 2)})
}

func (b *bot) snapshot() {
	for _, i := range []int{1, 2} {
		b.fx = append(b.fx, &effect{kind: fxSnap, life: 1.3, born: b.time + float64(i)*0.08, i: i})
	}
}

func (b *bot) step(dt float64) {
	b.dt = dt
	b.time += dt
	var due, rest []timed
	for _, q := range b.queue {
		if q.at <= b.time {
			due = append(due, q)
		} else {
			rest = append(rest, q)
		}
	}
	if len(due) > 0 {
		b.queue = rest
		epoch := b.epoch
		for _, q := range due {
			if b.epoch == epoch {
				q.fn()
			}
		}
	}
	if st := states[b.current]; st.tick != nil {
		st.tick(b, b.main, b.time-b.stateStart, dt)
	}
	b.main.step(dt)
	if b.clone != nil {
		b.clone.step(dt)
	}
	kept := b.fx[:0]
	for _, f := range b.fx {
		k := (b.time - f.born) / f.life
		f.live = false
		if k >= 1 {
			continue
		}
		if k >= 0 {
			f.k, f.live = k, true
			if f.kind == fxClone {
				f.b.step(dt)
			}
		}
		kept = append(kept, f)
	}
	b.fx = kept
}

type stateDef struct {
	enter func(b *bot, m *body)
	tick  func(b *bot, m *body, t, dt float64)
}

var states map[string]stateDef

func init() {
	states = map[string]stateDef{
		"asleep": {
			enter: func(b *bot, m *body) {
				m.shape("asleep", 5)
				m.gaze(0, 0, 5)
				m.lidBase = 1
				m.lid.to(1, 6)
				m.dl.to(0, 6)
				m.dr.to(0, 6)
				m.rot.to(0, 5)
				m.tx.to(0, 5)
				b.nextZ = 0.6
			},
			tick: func(b *bot, m *body, t, _ float64) {
				br := (1 - math.Cos(2*math.Pi*t/4.2)) / 2
				m.pose(1+0.014*br, 1-0.03*br, 0, 4)
				if !b.tight && t >= b.nextZ {
					b.spawnZ()
					b.nextZ = t + 1.5
				}
			},
		},
		"wake": {
			enter: func(b *bot, m *body) {
				m.lidBase = 1
				m.lid.to(1, 10)
				m.gaze(0, 0, 9)
				m.shape("squeeze", 14)
				m.pose(1.05, 0.92, 0, 12)
				b.at(0.32, func() { m.shape("awake", 16); m.pose(0.95, 1.08, -10, 16) })
				b.at(0.58, func() { m.pose(1.03, 0.97, 0, 12) })
				b.at(0.78, func() { m.pose(1, 1, 0, 10) })
				b.at(1.0, func() { b.blink(m) })
				b.at(1.6, b.settle)
			},
		},
		"idle": {
			enter: func(b *bot, m *body) {
				m.shape("awake", 10)
				m.lidBase = 1
				m.lid.to(1, 10)
				m.dl.to(0, 0)
				m.dr.to(0, 0)
				m.pose(1, 1, 0, 8)
				b.nextBlink = b.rand(1.2, 2.5)
				b.nextGlance = b.rand(0.8, 2)
			},
			tick: func(b *bot, m *body, t, _ float64) {
				if t >= b.nextBlink {
					b.blink(m)
					if b.rng.Float64() < 0.25 {
						b.at(0.22, func() { b.blink(m) })
					}
					b.nextBlink = t + b.rand(2.2, 5)
				}
				if t >= b.nextGlance {
					g := glances[b.rng.IntN(len(glances))]
					m.gaze(g[0], g[1], 14)
					m.tx.to(g[0]*0.35, 6)
					m.rot.to(g[0]*0.25, 6)
					b.nextGlance = t + b.rand(1.4, 3.6)
				}
				m.ty.to(math.Sin(t*1.6)*0.8, 6)
			},
		},
		"thinking": {
			enter: func(b *bot, m *body) {
				m.shape("think", 10)
				m.lidBase = 1
				m.lid.to(1, 0)
				m.gaze(6, -9, 8)
				m.tx.to(1.5, 6)
				m.rot.to(2.5, 6)
				b.nextGlance = 2.6
			},
			tick: func(b *bot, m *body, t, _ float64) {
				p := 2 * math.Pi * t / 1.1
				m.dl.to(-3.2*math.Max(0, math.Sin(p)), 20)
				m.dr.to(-3.2*math.Max(0, math.Sin(p-1.2)), 20)
				if t >= b.nextGlance {
					x, r := 6.0, 2.5
					if m.gx.t > 0 {
						x, r = -6, -2.5
					}
					m.gaze(x, -9, 6)
					m.rot.to(r, 5)
					b.nextGlance = t + 2.6
				}
			},
		},
		"working": {
			enter: func(b *bot, m *body) {
				m.shape("focus", 10)
				m.dl.to(0, 0)
				m.dr.to(0, 0)
				m.rot.to(0, 0)
				m.tx.to(0, 0)
				m.lidBase = 1
				m.lid.to(1, 0)
				b.col = 0
				b.nextSaccade = 0.2
				b.nextBlink = b.rand(2, 4)
			},
			tick: func(b *bot, m *body, t, _ float64) {
				if t >= b.nextSaccade {
					b.col = (b.col + 1) % 5
					if b.col == 0 {
						m.gaze(-8, 7, 10)
						b.nextSaccade = t + 0.5
					} else {
						m.gaze(-8+float64(b.col)*4, 5, 24)
						b.nextSaccade = t + b.rand(0.22, 0.4)
					}
				}
				if t >= b.nextBlink {
					b.blink(m)
					b.nextBlink = t + b.rand(2.5, 5)
				}
				m.ty.to(-math.Abs(math.Sin(t*11))*1.2, 30)
			},
		},
		"happy": {
			enter: func(b *bot, m *body) {
				m.shape("happy", 14)
				m.arc.to(1, 14)
				m.gaze(0, 0, 12)
				m.dl.to(0, 0)
				m.dr.to(0, 0)
				m.rot.to(0, 0)
				m.tx.to(0, 0)
				m.lidBase = 1
				m.lid.to(1, 0)
				hop := func(d float64) {
					b.at(d, func() { m.pose(1.05, 0.93, 0, 16) })
					b.at(d+0.12, func() { m.pose(0.96, 1.06, -12, 14) })
					b.at(d+0.32, func() { m.pose(1.03, 0.97, 0, 14) })
				}
				hop(0)
				hop(0.46)
				b.at(0.95, func() { m.pose(1, 1, 0, 10) })
				b.at(1.35, func() { m.arc.to(0, 12) })
				b.at(1.6, b.settle)
			},
		},
		"fork": {
			enter: func(b *bot, m *body) {
				m.shape("awake", 10)
				m.lidBase = 1
				m.lid.to(1, 0)
				m.dl.to(0, 0)
				m.dr.to(0, 0)
				m.rot.to(0, 0)
				m.pose(1.06, 0.92, 0, 12)
				b.at(0.28, func() {
					m.pose(1, 1, 0, 10)
					// A tight clone would hide behind main and fill its eye holes, so tight bots
					// fork alone. hibernaut-bot.js spawns it anyway.
					if b.tight {
						m.gaze(7, 0, 8)
						return
					}
					c := b.spawnClone()
					m.tx.to(-40, 7)
					m.gaze(7, 0, 8)
					c.tx.to(52, 7)
					c.sx.to(0.78, 7)
					c.sy.to(0.78, 7)
					c.op.to(1, 8)
					b.at(0.45, func() { c.gaze(-7, 0, 8) })
				})
				b.at(1.4, func() {
					b.blink(m)
					if b.clone != nil {
						b.blink(b.clone)
					}
				})
				b.at(2.2, func() {
					m.gaze(0, 0, 9)
					if b.clone != nil {
						b.clone.gaze(0, 0, 9)
					}
				})
				b.nextBlink = 3.5
			},
			tick: func(b *bot, m *body, t, _ float64) {
				if t >= b.nextBlink {
					b.blink(m)
					if b.clone != nil {
						b.at(b.rand(0.05, 0.3), func() {
							if b.clone != nil {
								b.blink(b.clone)
							}
						})
					}
					b.nextBlink = t + b.rand(2.2, 4)
				}
			},
		},
		"hibernate": {
			enter: func(b *bot, m *body) {
				m.shape("awake", 8)
				m.dl.to(0, 0)
				m.dr.to(0, 0)
				m.rot.to(0, 0)
				m.tx.to(0, 0)
				m.gaze(0, 3, 4)
				m.lidBase = 0.55
				m.lid.to(0.55, 3)
				b.at(0.9, func() { m.lid.to(0.08, 5) })
				b.at(1.35, func() { m.lid.to(0.5, 4) })
				b.at(1.75, func() { b.snapshot(); m.pose(1.03, 0.96, 0, 8) })
				b.at(2.0, func() { m.shape("asleep", 4); m.lidBase = 1; m.lid.to(1, 4); m.gaze(0, 0, 4) })
				b.at(2.6, func() { m.pose(1, 1, 0, 6) })
				b.at(3.2, b.settle)
			},
		},
	}
}

// still returns a bot frozen in a settled pose, for printed output that never animates.
func still(state string) *bot {
	return newBot(state, true, 1)
}

// played returns a bot that has played state for t seconds.
func played(state string, tight bool, t float64) *bot {
	b := newBot(state, tight, 3)
	for ; t > 0; t -= simStep {
		b.step(simStep)
	}
	return b
}
