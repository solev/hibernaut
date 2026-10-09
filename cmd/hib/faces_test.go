package main

import (
	"bytes"
	"encoding/base64"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/solev/hibernaut/internal/bot"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/room"
)

func TestFacesFollowTheEnvironment(t *testing.T) {
	for _, tc := range []struct {
		env  map[string]string
		want faceKind
	}{
		{map[string]string{"TERM": "xterm-256color"}, textFaces},
		{map[string]string{"TERM_PROGRAM": "ghostty", "TERM": "xterm-ghostty"}, imageFaces},
		{map[string]string{"TERM_PROGRAM": "ghostty", "TERM": "xterm-256color"}, imageFaces},
		{map[string]string{"TERM_PROGRAM": "kitty"}, imageFaces},
		{map[string]string{"TERM": "xterm-kitty", "KITTY_WINDOW_ID": "1"}, imageFaces},
		{map[string]string{"TERM": "xterm-ghostty"}, imageFaces},
		{map[string]string{"KITTY_WINDOW_ID": "1", "TERM": "xterm-256color"}, textFaces},
		{map[string]string{"TERM_PROGRAM": "WezTerm", "TERM": "xterm-kitty"}, textFaces},
		{map[string]string{"TERM_PROGRAM": "iTerm.app"}, textFaces},
		{map[string]string{"TERM_PROGRAM": "ghostty", "TMUX": "/tmp/tmux-501/default,1,0"}, textFaces},
		{map[string]string{"TERM_PROGRAM": "ghostty", "STY": "1.pts-0"}, textFaces},
		{map[string]string{"TERM_PROGRAM": "ghostty", "ZELLIJ": "0"}, textFaces},
		{map[string]string{"TERM": "screen-256color", "KITTY_WINDOW_ID": "1"}, textFaces},
		{map[string]string{"TERM": "tmux-256color", "KITTY_WINDOW_ID": "1"}, textFaces},
		{map[string]string{"TERM_PROGRAM": "ghostty", "NO_COLOR": "1"}, textFaces},
		{map[string]string{"TERM_PROGRAM": "ghostty", "HIB_FACES": "text"}, textFaces},
		{map[string]string{"TERM_PROGRAM": "ghostty", "HIB_FACES": "off"}, noFaces},
		{map[string]string{"HIB_FACES": "off"}, noFaces},
		{map[string]string{"TERM_PROGRAM": "ghostty", "HIB_FACES": "images"}, imageFaces},
	} {
		if got := facesFor(func(k string) string { return tc.env[k] }); got != tc.want {
			t.Errorf("faces in %v are %v, want %v", tc.env, got, tc.want)
		}
	}
}

// kittyChunk is one kitty graphics command: its keys and its base64 payload.
type kittyChunk struct{ keys, payload string }

func splitKitty(t *testing.T, s string) (chunks []kittyChunk, rest string) {
	t.Helper()
	for strings.HasPrefix(s, "\x1b_G") {
		end := strings.Index(s, "\x1b\\")
		if end < 0 {
			t.Fatalf("kitty command %q doesn't end", s)
		}
		keys, payload, _ := strings.Cut(s[3:end], ";")
		chunks = append(chunks, kittyChunk{keys, payload})
		s = s[end+2:]
	}
	return chunks, s
}

func TestKittyCommandsSendBigImagesInChunks(t *testing.T) {
	data := bytes.Repeat([]byte("hibernaut"), 1000)
	chunks, rest := splitKitty(t, kittyCommand("a=T,f=100,q=2", data))
	if rest != "" || len(chunks) != 3 {
		t.Fatalf("got %d chunks and %q after them, want 3 and nothing", len(chunks), rest)
	}
	want := []string{"a=T,f=100,q=2,m=1", "m=1,q=2", "m=0,q=2"}
	var payload string
	for i, c := range chunks {
		if c.keys != want[i] {
			t.Errorf("chunk %d has keys %q, want %q", i, c.keys, want[i])
		}
		if len(c.payload) > 4096 {
			t.Errorf("chunk %d carries %d bytes, want 4096 at most", i, len(c.payload))
		}
		payload += c.payload
	}
	if got, err := base64.StdEncoding.DecodeString(payload); err != nil || !bytes.Equal(got, data) {
		t.Errorf("the chunks carry %d bytes (%v), want the %d sent", len(got), err, len(data))
	}
	if chunks, _ := splitKitty(t, kittyCommand("a=T", []byte("x"))); len(chunks) != 1 || chunks[0].keys != "a=T,m=0" {
		t.Errorf("a small image went as %+v, want one command", chunks)
	}
}

func TestImageFacesAreAPNGTwoCellsWide(t *testing.T) {
	f := &faces{kind: imageFaces, cell: cellSize{9, 19}}
	chunks, rest := splitKitty(t, f.still(bot.Idle))
	if len(chunks) != 1 || rest != "  " {
		t.Fatalf("got %d commands and %q after them, want 1 and two spaces", len(chunks), rest)
	}
	if want := "a=T,f=100,q=2,C=1,c=2,r=1,m=0"; chunks[0].keys != want {
		t.Errorf("keys %q, want %q", chunks[0].keys, want)
	}
	data, err := base64.StdEncoding.DecodeString(chunks[0].payload)
	if err != nil {
		t.Fatal(err)
	}
	im, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if b := im.Bounds(); b.Dx() != 18 || b.Dy() != 19 {
		t.Errorf("the face is %dx%d, want 18x19 to fill two 9x19 cells", b.Dx(), b.Dy())
	}
	var body bool
	for y := range 19 {
		for x := range 18 {
			if color.NRGBAModel.Convert(im.At(x, y)) == facePalette.Body {
				body = true
			}
		}
	}
	if !body {
		t.Error("the face has no pixels in the body's colour")
	}
	if idle := f.still(bot.Idle); f.still(bot.Idle) != idle || idle == f.still(bot.Asleep) {
		t.Error("faces for one state differ, or awake and asleep faces match")
	}
}

func TestTextFacesShowTheEyes(t *testing.T) {
	for _, tc := range []struct {
		dim   bool
		state bot.State
		want  string
	}{
		{false, bot.Idle, "▪▪"},
		{false, bot.Asleep, "▬▬"},
		{true, bot.Idle, "▪▪"},
		{true, bot.Asleep, "\x1b[2m▬▬\x1b[22m"},
	} {
		f := &faces{kind: textFaces, dim: tc.dim}
		if got := f.still(tc.state); got != tc.want {
			t.Errorf("a %v text face with dim %v is %q, want %q", tc.state, tc.dim, got, tc.want)
		}
	}
	if got := (*faces)(nil).still(bot.Idle); got != "" {
		t.Errorf("no faces gave %q, want nothing", got)
	}
}

func TestCellSizeComesFromTheWindowSize(t *testing.T) {
	for _, tc := range []struct {
		ws   unix.Winsize
		want cellSize
	}{
		{unix.Winsize{Row: 24, Col: 80, Xpixel: 1440, Ypixel: 912}, cellSize{18, 38}},
		{unix.Winsize{Row: 24, Col: 80}, defaultCell},
		{unix.Winsize{}, defaultCell},
		{unix.Winsize{Row: 1, Col: 1, Xpixel: 4000, Ypixel: 4000}, cellSize{64, 128}},
	} {
		if got := cellOf(&tc.ws); got != tc.want {
			t.Errorf("a %+v window has %v cells, want %v", tc.ws, got, tc.want)
		}
	}
	if got := cellSizeOf(-1); got != defaultCell {
		t.Errorf("no terminal has %v cells, want %v", got, defaultCell)
	}
}

// facedClient is a client whose stdout is a terminal that shows text faces.
func facedClient(t *testing.T, h *host) *client {
	t.Helper()
	c := initClient(t, h)
	c.stdoutTerminal, c.faces = true, &faces{kind: textFaces, dim: true}
	return c
}

func TestResultsStartWithTheMachinesFace(t *testing.T) {
	h := imageHost(t)
	c := facedClient(t, h)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"new", "web"}, awakeFace + "made web; connect with: ssh web.hib\n"},
		{[]string{"fork", "web"}, awakeFace + "made web-1 from web. connect with: ssh web-1.hib\n"},
		{[]string{"pause", "web"}, asleepFace + "paused web\n"},
		{[]string{"rm", "web-1", "--yes"}, "removed web-1\n"},
	} {
		if code := c.hib(tc.args...); code != 0 || c.out.String() != tc.want {
			t.Errorf("hib %s: exit %d, printed %q, want %q: %s", strings.Join(tc.args, " "), code, c.out, tc.want, c.errOut)
		}
	}
}

func TestForkShowsAFaceOnlyWhenItMadeCopies(t *testing.T) {
	h := imageHost(t)
	c := facedClient(t, h)
	if code := c.hib("new", "web"); code != 0 {
		t.Fatalf("new = %d; stderr %s", code, c.errOut)
	}
	h.engine.FailCreateAfterStart(map[string]string{"hib.name": "web-2"})
	if code := c.hib("fork", "web", "-count", "2"); code != 1 || c.out.String() != awakeFace+"made web-1 from web. connect with: ssh web-1.hib\n" {
		t.Errorf("a fork that made one copy of two: exit %d, printed %q", code, c.out)
	}
	h.engine.FailCreateAfterStart(map[string]string{"hib.name": "web-2"})
	if code := c.hib("fork", "web"); code != 1 || c.out.String() != "" {
		t.Errorf("a fork that made nothing: exit %d, printed %q", code, c.out)
	}
}

func TestLsStartsEachMachineWithItsFace(t *testing.T) {
	h := newHost(t)
	c := facedClient(t, h)
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	for _, m := range []struct {
		name  string
		state engine.State
	}{{"app", engine.Running}, {"nap", engine.Paused}} {
		h.engine.Add(engine.Machine{ID: fake.NewID(), State: m.state, StartedAt: now.Add(-time.Hour), EndAt: now.Add(time.Hour),
			Metadata: map[string]string{"hib.name": m.name, "hib.image": "dev"}})
	}
	h.readRoom = func() (room.Host, error) {
		return room.Host{PageSize: 2 << 20, Pages: 3200, FreePages: 2101, DiskFree: 79 << 30}, nil
	}
	f := c.faces
	c.faces = nil
	if code := c.hib("ls"); code != 0 {
		t.Fatalf("ls exit %d: %s", code, c.errOut)
	}
	wantTable, wantRoom, _ := strings.Cut(c.out.String(), "\n\n")
	c.faces = f
	if code := c.hib("ls"); code != 0 {
		t.Fatalf("ls exit %d: %s", code, c.errOut)
	}
	table, room, _ := strings.Cut(c.out.String(), "\n\n")
	if room != wantRoom {
		t.Errorf("the room line is %q, want %q", room, wantRoom)
	}
	lines, wantLines := strings.Split(table, "\n"), strings.Split(wantTable, "\n")
	if len(lines) != 3 || len(wantLines) != 3 {
		t.Fatalf("ls =\n%s\nwant a header and two machines", c.out)
	}
	starts := map[string]string{"NAME": "    ", "app": "▪▪  ", "nap": "\x1b[2m▬▬\x1b[22m  "}
	for i, line := range lines {
		if want := starts[strings.Fields(wantLines[i])[0]] + wantLines[i]; line != want {
			t.Errorf("ls line %q, want %q", line, want)
		}
	}
}

func TestFacesNeedStdoutToBeATerminal(t *testing.T) {
	h := newHost(t)
	c := facedClient(t, h)
	c.stdoutTerminal = false
	if code := c.hib("new", "app"); code != 0 || c.out.String() != "made app; connect with: ssh app.hib\n" {
		t.Errorf("new into a pipe: exit %d, printed %q", code, c.out)
	}
	c.stdoutTerminal = true
	getenv := c.getenv
	c.getenv = func(k string) string {
		if k == "TERM" {
			return "dumb"
		}
		return getenv(k)
	}
	if code := c.hib("pause", "app"); code != 0 || c.out.String() != "paused app\n" {
		t.Errorf("pause with TERM=dumb: exit %d, printed %q", code, c.out)
	}
	c.getenv = getenv
	if code := c.hib("ls", "--json"); code != 0 || strings.ContainsAny(c.out.String(), "▪▬\x1b") {
		t.Errorf("ls --json: exit %d, printed %q", code, c.out)
	}
}
