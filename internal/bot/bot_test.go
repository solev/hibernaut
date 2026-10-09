package bot

import (
	"bytes"
	"image"
	"image/color"
	"testing"
	"time"
)

var (
	ink    = color.NRGBA{0xf5, 0xef, 0xe5, 0xff}
	accent = color.NRGBA{0xf3, 0xc7, 0x7c, 0xff}
	dark   = Palette{Body: ink, Accent: accent}
)

func TestSettledBotsReadOpenOrShut(t *testing.T) {
	for _, tc := range []struct {
		state State
		shut  bool
	}{
		{Asleep, true},
		{Idle, false},
		{Working, false},
		{Fork, false},
	} {
		if got := New(tc.state, 1).Shut(); got != tc.shut {
			t.Errorf("a settled %v bot reads shut %v, want %v", tc.state, got, tc.shut)
		}
	}
}

func TestIdleBotBlinks(t *testing.T) {
	b := New(Idle, 1)
	var shut, opened bool
	for range 6 * 60 {
		b.Advance(time.Second / 60)
		if b.Shut() {
			shut = true
		} else if shut {
			opened = true
			break
		}
	}
	if !shut || !opened {
		t.Errorf("in 6 s an idle bot shut its eyes %v and opened them again %v, want both", shut, opened)
	}
}

func TestTransientStatesSettle(t *testing.T) {
	for _, tc := range []struct {
		from, to, start State
		after           time.Duration
		shut            bool
	}{
		{Wake, Idle, Asleep, 1700 * time.Millisecond, false},
		{Hibernate, Asleep, Idle, 4 * time.Second, true},
	} {
		b := New(tc.start, 1)
		b.Become(tc.from)
		if b.State() != tc.from {
			t.Fatalf("Become(%v) left the bot %v", tc.from, b.State())
		}
		b.Advance(tc.after)
		if b.State() != tc.to {
			t.Errorf("%v after %v is %v, want %v", tc.from, tc.after, b.State(), tc.to)
		}
		b.Advance(time.Second)
		if b.Shut() != tc.shut {
			t.Errorf("%v then %v reads shut %v, want %v", tc.from, tc.to, b.Shut(), tc.shut)
		}
	}
}

func TestWakingOpensTheEyes(t *testing.T) {
	b := New(Asleep, 1)
	b.Become(Wake)
	b.Advance(100 * time.Millisecond)
	if !b.Shut() {
		t.Error("a bot 0.1 s into waking has its eyes open, want them squeezed shut")
	}
	b.Advance(600 * time.Millisecond)
	if b.Shut() {
		t.Error("a bot 0.7 s into waking has its eyes shut, want them open")
	}
}

func TestFramesAreTheSameForTheSameSeedAndTime(t *testing.T) {
	a, b := New(Idle, 7), New(Idle, 7)
	a.Advance(5 * time.Second)
	for range 500 {
		b.Advance(10 * time.Millisecond)
	}
	if !bytes.Equal(a.Draw(40, 40, dark).Pix, b.Draw(40, 40, dark).Pix) {
		t.Error("two bots with one seed differ after 5 s in one step and in 500, want the same frame")
	}
}

func TestFramesChangeAsTheBotMoves(t *testing.T) {
	for _, tc := range []struct{ start, state State }{
		{Idle, Idle}, {Idle, Working}, {Asleep, Wake}, {Idle, Fork}, {Idle, Hibernate},
	} {
		b := New(tc.start, 3)
		b.Become(tc.state)
		frames := map[string]bool{}
		for range 60 {
			b.Advance(50 * time.Millisecond)
			frames[string(b.Draw(32, 34, dark).Pix)] = true
		}
		if len(frames) < 5 {
			t.Errorf("%v drew %d different frames in 3 s, want it to move", tc.state, len(frames))
		}
	}
}

func TestDrawUsesThePaletteAndLeavesTheEyesOut(t *testing.T) {
	im := New(Idle, 1).Draw(40, 40, dark)
	if im.Bounds() != image.Rect(0, 0, 40, 40) {
		t.Fatalf("drew %v, want 40 by 40", im.Bounds())
	}
	if got := im.NRGBAAt(20, 34); got != ink {
		t.Errorf("the body's lower middle is %v, want the body ink %v", got, ink)
	}
	if got := im.NRGBAAt(0, 0).A; got != 0 {
		t.Errorf("the corner has alpha %d, want it clear", got)
	}
	var holes int
	for y := 10; y < 30; y++ {
		for x := 5; x < 35; x++ {
			if im.NRGBAAt(x, y).A == 0 {
				holes++
			}
		}
	}
	if holes < 4 {
		t.Errorf("an awake bot's face has %d clear pixels, want eye holes", holes)
	}
}

func TestDrawCanFillTheEyesAndEdgeTheBody(t *testing.T) {
	night := color.NRGBA{0x14, 0x11, 0x0e, 0xff}
	sticker := Palette{Body: ink, Eyes: night, Accent: accent, Edge: color.NRGBA{0x14, 0x11, 0x0e, 0xc0}}
	b := New(Idle, 1)
	plain, filled := b.Draw(40, 40, dark), b.Draw(40, 40, sticker)
	var eyes, edge int
	for y := range 40 {
		for x := range 40 {
			before, after := plain.NRGBAAt(x, y), filled.NRGBAAt(x, y)
			switch {
			case before.A != 0:
			case after.A == 0:
			case after.R != night.R || after.G != night.G || after.B != night.B:
				t.Fatalf("pixel %d,%d is %v, want the eyes' and edge's %v", x, y, after, night)
			case after.A == 0xff:
				eyes++
			default:
				edge++
			}
		}
	}
	if eyes < 4 || edge < 40 {
		t.Errorf("filled %d eye pixels and %d edge pixels, want the eyes filled and a line around the body", eyes, edge)
	}
}

func TestAdvancingByNothingOrLessDoesNothing(t *testing.T) {
	b, want := New(Idle, 3), New(Idle, 3)
	b.Become(Fork)
	want.Become(Fork)
	b.Advance(0)
	b.Advance(-time.Second)
	b.Advance(100 * time.Millisecond)
	want.Advance(100 * time.Millisecond)
	if !bytes.Equal(b.Draw(40, 40, dark).Pix, want.Draw(40, 40, dark).Pix) {
		t.Error("advancing by 0 and -1s changed where the bot is")
	}
}

func near(a, b uint8) bool { return max(a, b)-min(a, b) < 3 }

func TestHibernatingFlashesSnapshotsInTheAccent(t *testing.T) {
	b := New(Idle, 1)
	b.Become(Hibernate)
	var saw bool
	for range 60 {
		b.Advance(50 * time.Millisecond)
		im := b.Draw(64, 64, dark)
		for i := 0; i < len(im.Pix); i += 4 {
			if near(im.Pix[i], accent.R) && near(im.Pix[i+1], accent.G) && near(im.Pix[i+2], accent.B) && im.Pix[i+3] > 0 {
				saw = true
			}
		}
	}
	if !saw {
		t.Error("hibernating drew no accent pixels, want its snapshot outlines")
	}
}

func TestSmallFacesSnapEyesToWholePixels(t *testing.T) {
	im := New(Idle, 1).Draw(20, 20, dark)
	for y := 7; y < 14; y++ {
		for x := 5; x < 15; x++ {
			if a := im.NRGBAAt(x, y).A; a != 0 && a != 255 {
				t.Fatalf("pixel %d,%d near the eyes has alpha %d, want the eyes snapped to whole pixels", x, y, a)
			}
		}
	}
}
