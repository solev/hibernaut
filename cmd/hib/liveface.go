package main

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/charmbracelet/colorprofile"

	"github.com/solev/hibernaut/internal/bot"
)

// faceScript is how the live line's face moves while hib waits: it starts in one state and, cue
// after that, becomes the next. A script with no cue stays in its first state.
type faceScript struct {
	start bot.State
	cue   time.Duration
	next  bot.State
}

// The live lines' scripts (docs/milestones/m11-bot-faces.md).
var (
	makingFace   = faceScript{bot.Asleep, 1600 * time.Millisecond, bot.Wake}
	wakingFace   = faceScript{start: bot.Asleep}
	forkingFace  = faceScript{bot.Idle, 400 * time.Millisecond, bot.Fork}
	pausingFace  = faceScript{bot.Idle, 300 * time.Millisecond, bot.Hibernate}
	buildingFace = faceScript{start: bot.Working}
)

const (
	// faceEvery is how often the live line's face moves.
	faceEvery = 50 * time.Millisecond
	// faceCatchUp is the most the face moves at once, when hib's ticks were late.
	faceCatchUp = time.Second
)

// liveFace is the face at the start of the live line. Once Bubble Tea runs, only the live line's
// Update and View use it, on Bubble Tea's event loop.
type liveFace struct {
	faces *faces
	// id is the image's id, so the next frame can replace it, from 1 to 2^24-1. Its placeholders'
	// colour says it.
	id     uint32
	script faceScript
	bot    *bot.Bot
	// started is when the script started, and moved is when the bot last moved.
	started, moved time.Time
	cued           bool
	// pixels are the frame last sent.
	pixels []byte
}

func newLiveFace(f *faces) *liveFace {
	if f == nil {
		return nil
	}
	return &liveFace{faces: f, id: rand.Uint32N(1<<24-1) + 1}
}

// play starts script at now. The bot moves to its first state from wherever it is.
func (lf *liveFace) play(script faceScript, now time.Time) {
	lf.script, lf.started, lf.moved, lf.cued = script, now, now, false
	if lf.bot == nil {
		lf.bot = bot.New(script.start, uint64(lf.id))
		return
	}
	lf.bot.Become(script.start)
}

// move moves the bot on to now, and into the script's next state when its cue comes.
func (lf *liveFace) move(now time.Time) {
	d := min(max(0, now.Sub(lf.moved)), faceCatchUp)
	lf.moved = now
	if lf.script.cue > 0 && !lf.cued {
		if late := now.Sub(lf.started.Add(lf.script.cue)); late >= 0 {
			late = min(late, d)
			lf.bot.Advance(d - late)
			lf.bot.Become(lf.script.next)
			lf.cued = true
			d = late
		}
	}
	lf.bot.Advance(d)
}

// cells is the face as it shows on the line, two columns wide: the eyes, or two of the kitty
// protocol's Unicode placeholders for the image. A placeholder's colour says the image's id, and
// its marks say row 0 and columns 0 and 1.
func (lf *liveFace) cells() string {
	if lf.faces.kind == textFaces {
		return lf.faces.eyes(lf.bot)
	}
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm\U0010EEEE\u0305\u0305\U0010EEEE\u0305\u030D\x1b[39m",
		byte(lf.id>>16), byte(lf.id>>8), byte(lf.id))
}

// frame returns the kitty commands that send the bot as it is now, as a virtual placement for the
// placeholders. It's empty for text faces, and when the frame is the one last sent.
func (lf *liveFace) frame() string {
	if lf.faces.kind != imageFaces {
		return ""
	}
	im := lf.faces.draw(lf.bot)
	if bytes.Equal(im.Pix, lf.pixels) {
		return ""
	}
	lf.pixels = im.Pix
	return kittyCommand(fmt.Sprintf("a=T,U=1,f=100,i=%d,p=1,c=2,r=1,q=2", lf.id), encodePNG(im))
}

// deleteImage frees the image once its placeholders are gone.
func (lf *liveFace) deleteImage() string {
	return fmt.Sprintf("\x1b_Ga=d,d=I,i=%d,q=2\x1b\\", lf.id)
}

// profile is what Bubble Tea may draw the face with: true colour keeps the placeholders' id, and
// plain text keeps an asleep face dim.
func (lf *liveFace) profile() colorprofile.Profile {
	if lf.faces.kind == imageFaces {
		return colorprofile.TrueColor
	}
	return colorprofile.Ascii
}
