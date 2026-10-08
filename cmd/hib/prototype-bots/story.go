package main

import (
	"fmt"
	"time"
)

// simStep is the fixed step every bot and the story advance by, so a seek replays exactly.
const simStep = 1.0 / 60

type itemKind int

const (
	itPrompt itemKind = iota
	itNote
	itResult
	itTable
	itRemote
	itDim
)

type machine struct {
	name, state, image, size, idle, pausesIn, started, id, from string
}

func (m machine) awake() bool { return m.state == "running" }

// item is one thing hib prints. text is the line exactly as hib prints it today, so a variant
// that keeps today's output prints text and ignores the rest.
type item struct {
	kind     itemKind
	ps, text string
	caret    bool
	machines []machine
	room     string
	hints    []string
}

type stepKind int

const (
	stPause stepKind = iota
	stType
	stLive
	stPrint
)

type cue struct {
	at    float64
	state string
	snap  bool
}

type step struct {
	kind       stepKind
	start, dur float64
	item       item
	text       string
	scale      float64
	scene      bool
}

type story struct {
	steps  []step
	total  float64
	scenes []float64
	cues   []cue
}

var (
	api   = machine{"api", "paused", "dev", "small", "30m", "-", "2026-05-07 09:12", "iq7k2m9x4w1n5c8b3z6v", "-"}
	app   = machine{"app", "running", "dev", "small", "30m", "29m", "2026-05-08 14:02", "i5h8d2k7q1m4x9w3n6c0", "-"}
	docs  = machine{"docs", "paused", "dev", "small", "2h", "-", "2026-05-05 10:40", "ib3n6c9w2k5m8q1x4z7d", "-"}
	app1  = machine{"app-1", "running", "dev", "small", "30m", "29m", "2026-05-08 14:09", "ix2c5v8b1n4m7k0q3w6e", "app"}
	app2  = machine{"app-2", "running", "dev", "small", "30m", "29m", "2026-05-08 14:09", "ir9t2y5u8i1o4p7a0s3d", "app"}
	woken = with(api, "running", "29m")
	slept = with(app, "paused", "-")
)

func with(m machine, state, pausesIn string) machine {
	m.state, m.pausesIn = state, pausesIn
	return m
}

func newStory() *story {
	s := &story{}
	t := 0.0
	add := func(st step) {
		st.start = t
		t += st.dur
		s.steps = append(s.steps, st)
	}
	pause := func(d float64) { add(step{kind: stPause, dur: d}) }
	typ := func(ps, cmd string, scene bool) {
		if scene {
			s.scenes = append(s.scenes, t)
		}
		add(step{kind: stType, dur: float64(len(cmd))*0.06 + 0.35, item: item{kind: itPrompt, ps: ps, text: cmd}, scene: scene})
	}
	// live waits dur seconds of story time, showing scale seconds of hib's elapsed time for each.
	// The scene starts where hib's live line appears, 2s into the wait.
	live := func(text string, dur, scale float64, cues ...cue) {
		for _, c := range cues {
			c.at += t
			s.cues = append(s.cues, c)
		}
		add(step{kind: stLive, dur: dur, text: text, scale: scale})
	}
	print := func(it item) { add(step{kind: stPrint, item: it}) }

	pause(0.8)
	typ("~ $", "hib new app", true)
	live("making app", 3.6, 1, cue{0, "asleep", true}, cue{1.6, "wake", false})
	print(item{kind: itResult, text: "made app; connect with: ssh app.hib", machines: []machine{app},
		hints: []string{"connect with: ssh app.hib"}})

	pause(1.4)
	typ("~ $", "hib ls", true)
	print(item{kind: itTable, machines: []machine{api, app, docs},
		room: "Room for more machines: 3 small or 1 medium. 5.2 GiB of 8 GiB memory free, 41 GiB disk free."})

	pause(2.8)
	typ("~ $", "ssh api.hib", true)
	print(item{kind: itNote, text: "hib: the host is asleep (VM deallocated); starting it, which takes a few minutes"})
	const wakeDur, wakeScale = 4.8, 20
	live("waking the host", wakeDur, wakeScale, cue{0, "asleep", true}, cue{3.0, "wake", false})
	print(item{kind: itNote, text: fmt.Sprintf("hib: the host is up after %v", elapsed(wakeDur, wakeScale))})
	print(item{kind: itDim, text: "woke api in 0.4s", machines: []machine{woken}})
	pause(1.0)
	typ("api:~/rocket $", "exit", false)
	print(item{kind: itRemote, text: "Connection to api.hib closed."})

	pause(1.2)
	typ("~ $", "hib fork app --count 2", true)
	live("forking app", 3.4, 1, cue{0, "idle", true}, cue{0.4, "fork", false})
	print(item{kind: itResult, text: "made app-1, app-2 from app. connect with: ssh app-1.hib", machines: []machine{app1, app2},
		hints: []string{"ssh app-1.hib", "ssh app-2.hib"}})

	pause(1.2)
	typ("~ $", "hib pause app", true)
	live("pausing app", 3.8, 1, cue{0, "idle", true}, cue{0.3, "hibernate", false})
	print(item{kind: itResult, text: "paused app", machines: []machine{slept}, hints: []string{"wakes when you connect"}})

	pause(1.2)
	typ("~ $", "hib ls", true)
	print(item{kind: itTable, machines: []machine{woken, slept, app1, app2, docs},
		room: "Room for more machines: 1 small. 2.1 GiB of 8 GiB memory free, 39 GiB disk free."})
	pause(5)

	s.total = t
	return s
}

// elapsed is what hib's live line shows after local seconds of a wait, truncated like
// liveLine.line. It counts from the start of the wait, 2s before the line appears.
func elapsed(local, scale float64) time.Duration {
	return time.Duration(2+local*scale) * time.Second
}

// frame is what the terminal shows at one moment: everything printed so far, and the live
// line if hib is waiting.
type frame struct {
	items []item
	live  string
	scene int
}

func (s *story) at(t float64) frame {
	var f frame
	for i, st := range s.steps {
		if st.dur == 0 && t >= st.start || st.dur > 0 && t >= st.start+st.dur {
			switch st.kind {
			case stType, stPrint:
				f.items = append(f.items, st.item)
			}
			continue
		}
		if t < st.start {
			break
		}
		local := t - st.start
		switch st.kind {
		case stPause:
			if i+1 < len(s.steps) && s.steps[i+1].kind == stType {
				f.items = append(f.items, item{kind: itPrompt, ps: s.steps[i+1].item.ps, caret: int(local*2)%2 == 0})
			}
		case stType:
			it := st.item
			it.text = it.text[:min(len(it.text), int(local/0.06))]
			it.caret = true
			f.items = append(f.items, it)
		case stLive:
			f.live = fmt.Sprintf("%s (%v)", st.text, elapsed(local, st.scale))
		}
		break
	}
	for _, sc := range s.scenes {
		if t >= sc {
			f.scene++
		}
	}
	return f
}

// player runs the story's clock and the two live bots, a tight one for faces and a wide one
// with room for Zs and a fork.
type player struct {
	st          *story
	t, acc      float64
	tight, wide *bot
	next        int
}

func newPlayer(st *story) *player {
	p := &player{st: st}
	p.reset()
	return p
}

func (p *player) reset() {
	p.t, p.acc, p.next = 0, 0, 0
	p.tight, p.wide = newBot("asleep", true, 11), newBot("asleep", false, 12)
}

func (p *player) advance(dt float64) {
	p.acc += min(dt, 0.25)
	for p.acc >= simStep {
		p.acc -= simStep
		p.tick()
	}
}

func (p *player) tick() {
	p.t += simStep
	if p.t >= p.st.total {
		p.reset()
		return
	}
	for p.next < len(p.st.cues) && p.st.cues[p.next].at <= p.t {
		c := p.st.cues[p.next]
		p.next++
		if c.snap {
			p.tight = newBot(c.state, true, uint64(20+p.next))
			p.wide = newBot(c.state, false, uint64(40+p.next))
		} else {
			p.tight.become(c.state)
			p.wide.become(c.state)
		}
	}
	p.tight.step(simStep)
	p.wide.step(simStep)
}

func (p *player) seek(t float64) {
	p.reset()
	for p.t+simStep <= t {
		p.tick()
	}
}

// scene moves to the start of the scene d away from the current one.
func (p *player) scene(d int) {
	cur := p.st.at(p.t).scene
	n := len(p.st.scenes)
	k := ((cur-1+d)%n + n) % n
	p.seek(p.st.scenes[k] + simStep)
}
