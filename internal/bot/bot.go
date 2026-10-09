// Package bot is the Hibernaut bot from docs/brand/bot, ported from hibernaut-bot.js for hib's
// faces: the same shapes, springs and timings, stepped in fixed steps of 1/60 s instead of by a
// browser. It keeps the tight bot's states that hib shows, and draws frames into images. It has
// no terminal code.
package bot

import (
	"fmt"
	"math"
	"math/rand/v2"
	"time"
)

// State is what the bot is doing.
type State int

const (
	Asleep State = iota
	// Wake squeezes the eyes, opens them and settles into Idle after 1.6 s.
	Wake
	// Idle blinks and glances around.
	Idle
	// Working reads along a line and bobs.
	Working
	// Fork squashes and looks aside. The tight bot forks alone: a clone wouldn't read at its size.
	Fork
	// Hibernate grows drowsy, flashes two snapshot outlines and settles into Asleep after 3.2 s.
	Hibernate
)

var stateNames = [...]string{"asleep", "wake", "idle", "working", "fork", "hibernate"}

func (s State) String() string {
	if s < Asleep || s > Hibernate {
		return fmt.Sprintf("State(%d)", int(s))
	}
	return stateNames[s]
}

// step is what hibernaut-bot.js moves in at 60 frames a second.
const step = time.Second / 60

type eyeShape struct{ lx, rx, y, w, h float64 }

var (
	asleepEyes  = eyeShape{29, 71, 55.2, 25.6, 6.8}
	awakeEyes   = eyeShape{32, 68, 50, 13, 15}
	focusEyes   = eyeShape{32, 68, 53, 14, 9}
	squeezeEyes = eyeShape{29, 71, 55.2, 27, 2.5}
)

var glances = [][2]float64{{0, 0}, {0, 0}, {-7, 0}, {7, 0}, {-6, -4}, {6, -4}, {0, 3}}

// val approaches its target exponentially. s is 1/seconds.
type val struct{ v, t, s float64 }

func (a *val) set(x float64) { a.v, a.t, a.s = x, x, 10 }

func (a *val) to(t, s float64) {
	a.t = t
	if s != 0 {
		a.s = s
	}
}

func (a *val) step(dt float64) { a.v += (a.t - a.v) * (1 - math.Exp(-a.s*dt)) }

// body is the bot's square and eyes, in a 100 by 100 viewBox. It scales and rotates about the
// middle of its bottom edge, at 50,100.
type body struct {
	tx, ty, rot, gx, gy val
	sx, sy, lid         val
	lx, rx, ey, ew, eh  val
	lidBase             float64
}

func (m *body) vals() []*val {
	return []*val{&m.tx, &m.ty, &m.rot, &m.gx, &m.gy, &m.sx, &m.sy, &m.lid, &m.lx, &m.rx, &m.ey, &m.ew, &m.eh}
}

func (m *body) reset(sh eyeShape) {
	for _, v := range []*val{&m.tx, &m.ty, &m.rot, &m.gx, &m.gy} {
		v.set(0)
	}
	for _, v := range []*val{&m.sx, &m.sy, &m.lid} {
		v.set(1)
	}
	m.lx.set(sh.lx)
	m.rx.set(sh.rx)
	m.ey.set(sh.y)
	m.ew.set(sh.w)
	m.eh.set(sh.h)
	m.lidBase = 1
}

func (m *body) shape(sh eyeShape, s float64) {
	m.lx.to(sh.lx, s)
	m.rx.to(sh.rx, s)
	m.ey.to(sh.y, s)
	m.ew.to(sh.w, s)
	m.eh.to(sh.h, s)
}

func (m *body) gaze(x, y, s float64) { m.gx.to(x, s); m.gy.to(y, s) }

func (m *body) pose(sx, sy, ty, s float64) { m.sx.to(sx, s); m.sy.to(sy, s); m.ty.to(ty, s) }

// snapshot is an outline of the body that slides out behind it as the bot hibernates.
type snapshot struct {
	born float64
	i    int
}

const snapshotLife = 1.3

type timed struct {
	at float64
	fn func()
}

// Bot is one animated bot. It isn't safe for use by more than one goroutine.
type Bot struct {
	state      State
	body       body
	snapshots  []snapshot
	queue      []timed
	time       float64
	stateStart float64
	epoch      int
	rng        *rand.Rand
	pending    time.Duration

	nextBlink, nextGlance, nextSaccade float64
	col                                int
}

// New returns a bot settled in state s. seed picks its blinks and glances, so two bots with one
// seed move alike.
func New(s State, seed uint64) *Bot {
	b := &Bot{rng: rand.New(rand.NewPCG(seed, 7))}
	b.body.reset(asleepEyes)
	b.Become(s)
	for _, v := range b.body.vals() {
		v.v = v.t
	}
	return b
}

// State returns what the bot is doing. Wake and Hibernate become Idle and Asleep when they end.
func (b *Bot) State() State { return b.state }

// Become starts state s from wherever the bot is, so it moves there smoothly.
func (b *Bot) Become(s State) {
	if s < Asleep || s > Hibernate {
		return
	}
	b.state = s
	b.epoch++
	b.queue = nil
	b.stateStart = b.time
	b.enter()
}

// Advance moves the bot on by d, in fixed steps. What's left over carries into the next call. A d
// of 0 or less does nothing.
func (b *Bot) Advance(d time.Duration) {
	if d <= 0 {
		return
	}
	b.pending += d
	for b.pending >= step {
		b.pending -= step
		b.step(step.Seconds())
	}
}

// Shut reports whether the eyes read as shut: too thin, or too wide for their height.
func (b *Bot) Shut() bool {
	m := &b.body
	h := m.eh.v * m.lid.v
	return h < 4.5 || m.ew.v/h > 2.2
}

func (b *Bot) rand(lo, hi float64) float64 { return lo + b.rng.Float64()*(hi-lo) }

func (b *Bot) at(delay float64, fn func()) { b.queue = append(b.queue, timed{b.time + delay, fn}) }

func (b *Bot) settle() {
	switch b.state {
	case Wake:
		b.Become(Idle)
	case Hibernate:
		b.Become(Asleep)
	}
}

func (b *Bot) blink() {
	m := &b.body
	m.lid.to(0.06, 45)
	b.at(0.09, func() { m.lid.to(m.lidBase, 22) })
}

func (b *Bot) step(dt float64) {
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
	b.tick(b.time - b.stateStart)
	for _, v := range b.body.vals() {
		v.step(dt)
	}
	kept := b.snapshots[:0]
	for _, s := range b.snapshots {
		if b.time-s.born < snapshotLife {
			kept = append(kept, s)
		}
	}
	b.snapshots = kept
}

func (b *Bot) enter() {
	m := &b.body
	switch b.state {
	case Asleep:
		m.shape(asleepEyes, 5)
		m.gaze(0, 0, 5)
		m.lidBase = 1
		m.lid.to(1, 6)
		m.rot.to(0, 5)
		m.tx.to(0, 5)
	case Wake:
		m.lidBase = 1
		m.lid.to(1, 10)
		m.gaze(0, 0, 9)
		m.shape(squeezeEyes, 14)
		m.pose(1.05, 0.92, 0, 12)
		b.at(0.32, func() { m.shape(awakeEyes, 16); m.pose(0.95, 1.08, -10, 16) })
		b.at(0.58, func() { m.pose(1.03, 0.97, 0, 12) })
		b.at(0.78, func() { m.pose(1, 1, 0, 10) })
		b.at(1.0, b.blink)
		b.at(1.6, b.settle)
	case Idle:
		m.shape(awakeEyes, 10)
		m.lidBase = 1
		m.lid.to(1, 10)
		m.pose(1, 1, 0, 8)
		b.nextBlink = b.rand(1.2, 2.5)
		b.nextGlance = b.rand(0.8, 2)
	case Working:
		m.shape(focusEyes, 10)
		m.rot.to(0, 0)
		m.tx.to(0, 0)
		m.lidBase = 1
		m.lid.to(1, 0)
		b.col = 0
		b.nextSaccade = 0.2
		b.nextBlink = b.rand(2, 4)
	case Fork:
		m.shape(awakeEyes, 10)
		m.lidBase = 1
		m.lid.to(1, 0)
		m.rot.to(0, 0)
		m.pose(1.06, 0.92, 0, 12)
		b.at(0.28, func() { m.pose(1, 1, 0, 10); m.gaze(7, 0, 8) })
		b.at(1.4, b.blink)
		b.at(2.2, func() { m.gaze(0, 0, 9) })
		b.nextBlink = 3.5
	case Hibernate:
		m.shape(awakeEyes, 8)
		m.rot.to(0, 0)
		m.tx.to(0, 0)
		m.gaze(0, 3, 4)
		m.lidBase = 0.55
		m.lid.to(0.55, 3)
		b.at(0.9, func() { m.lid.to(0.08, 5) })
		b.at(1.35, func() { m.lid.to(0.5, 4) })
		b.at(1.75, func() {
			for _, i := range []int{1, 2} {
				b.snapshots = append(b.snapshots, snapshot{born: b.time + float64(i)*0.08, i: i})
			}
			m.pose(1.03, 0.96, 0, 8)
		})
		b.at(2.0, func() { m.shape(asleepEyes, 4); m.lidBase = 1; m.lid.to(1, 4); m.gaze(0, 0, 4) })
		b.at(2.6, func() { m.pose(1, 1, 0, 6) })
		b.at(3.2, b.settle)
	}
}

// tick runs every step with the time t since the state began.
func (b *Bot) tick(t float64) {
	m := &b.body
	switch b.state {
	case Asleep:
		br := (1 - math.Cos(2*math.Pi*t/4.2)) / 2
		m.pose(1+0.014*br, 1-0.03*br, 0, 4)
	case Idle:
		if t >= b.nextBlink {
			b.blink()
			if b.rng.Float64() < 0.25 {
				b.at(0.22, b.blink)
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
	case Working:
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
			b.blink()
			b.nextBlink = t + b.rand(2.5, 5)
		}
		m.ty.to(-math.Abs(math.Sin(t*11))*1.2, 30)
	case Fork:
		if t >= b.nextBlink {
			b.blink()
			b.nextBlink = t + b.rand(2.2, 4)
		}
	}
}
