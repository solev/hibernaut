package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/hibapi"
)

func eventLine(t *testing.T, e hibapi.Event) string {
	t.Helper()
	data, err := json.Marshal(hibapi.Response{V: hibapi.Version, Event: &e})
	if err != nil {
		t.Fatal(err)
	}
	return string(data) + "\n"
}

func TestStepLinesMatchHibds(t *testing.T) {
	for _, tc := range []struct {
		event hibapi.Event
		want  string
	}{
		{hibapi.Event{Image: "dev", Size: "small", Step: "3/12", Text: "RUN apt-get install -y git curl", State: hibapi.EventReused},
			"dev (small)  3/12  RUN apt-get install -y git curl  reused"},
		{hibapi.Event{Image: "dev", Size: "small", Step: "4/12", Text: "RUN npm i -g @openai/codex", State: hibapi.EventBuilt, Seconds: 41},
			"dev (small)  4/12  RUN npm i -g @openai/codex  built in 41s"},
		{hibapi.Event{Image: "dev", Size: "large", Step: "base", Text: "FROM ubuntu:24.04", State: hibapi.EventFailed, Seconds: 3},
			"dev (large)  base  FROM ubuntu:24.04  failed"},
		{hibapi.Event{Image: "dev", Size: "small", Step: "1/2", Text: "RUN echo \x1b]0;owned\x07\nrm -rf /", State: "odd\x1b[2J"},
			`dev (small)  1/2  RUN echo \x1b]0;owned\x07\x0arm -rf /  odd\x1b[2J`},
		{hibapi.Event{Image: "dev", Size: "small", Step: "2/2", Text: "RUN " + strings.Repeat("x", 200), State: hibapi.EventReused},
			"dev (small)  2/2  RUN " + strings.Repeat("x", 69) + "…  reused"},
	} {
		if got := stepLine(tc.event); got != tc.want {
			t.Errorf("stepLine = %q, want %q", got, tc.want)
		}
	}
}

func TestImageBuildShowsStepsAsTheyRun(t *testing.T) {
	h := newHost(t)
	c, term := liveClient(t, h, "xterm-256color")
	at := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	h.engine = fake.New(
		fake.WithTemplates(images.Template("dev")),
		fake.WithBuildLogs(func(_ context.Context, _ engine.TemplateSpec, emit func(engine.LogEntry)) error {
			emit(engine.LogEntry{Time: at, Level: "info", Message: "[builder 1/2] RUN apt-get install -y git [aa11]"})
			term.waitFor("1/2  RUN apt-get install -y git (")
			emit(engine.LogEntry{Time: at.Add(41 * time.Second), Level: "info", Message: "CACHED [builder 2/2] RUN npm i -g codex [bb22]"})
			return nil
		}),
	)
	if code := c.hib("image", "build", "dev"); code != 0 {
		t.Fatalf("exit %d: %q", code, term)
	}
	if want := "built image dev in sizes medium, small and large\n"; c.out.String() != want {
		t.Errorf("printed %q, want %q", c.out, want)
	}
	var want []string
	for _, size := range []string{"medium", "small", "large"} {
		want = append(want,
			"dev ("+size+")  1/2  RUN apt-get install -y git  built in 41s",
			"dev ("+size+")  2/2  RUN npm i -g codex  reused",
			"built dev ("+size+") in 0s")
	}
	checkLines(t, term, want...)
	if got, want := remoteCommands(h.takeCalls()), []string{"hibd version --json", "hibd image build dev --json --events"}; !slices.Equal(got, want) {
		t.Errorf("remote commands = %q, want %q", got, want)
	}
}

func TestImageBuildFailsOnUnreadableProgress(t *testing.T) {
	started := eventLine(t, hibapi.Event{Image: "dev", Size: "medium", Step: "1/2", Text: "RUN make", State: hibapi.EventStarted})
	for _, tc := range []struct {
		name, stdout string
		code         int
		want         string
	}{
		{name: "malformed", stdout: started + "not json\n", want: "couldn't read hibd's build progress: unreadable reply from hibd"},
		{name: "too long", stdout: started + strings.Repeat("x", maxEventLine), want: "couldn't read hibd's build progress: hibd sent a line longer than 64 KiB"},
		{name: "after the reply", stdout: started + `{"v":1,"sizes":["medium"]}` + "\n" + started,
			want: "couldn't read hibd's build progress: hibd sent more after its reply"},
		{name: "no reply", stdout: started, code: 255, want: "hibd stopped before it said how the build ended"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHost(t)
			c, term := liveClient(t, h, "xterm-256color")
			h.states = []string{"VM deallocated"}
			h.override = func(words []string, stdout, _ io.Writer) (int, bool) {
				if !slices.Equal(words[:2], []string{"image", "build"}) {
					return 0, false
				}
				io.WriteString(stdout, tc.stdout)
				return tc.code, true
			}
			if code := c.hib("image", "build", "dev"); code != 1 {
				t.Errorf("exit %d, want 1", code)
			}
			mayBeRunning := "The build may still be running on the host: check hib image ls in a few minutes\n"
			if got := term.String(); !strings.Contains(got, "hib: "+tc.want) || !strings.HasSuffix(got, mayBeRunning) {
				t.Errorf("stderr %q, want %q and then %q", got, tc.want, mayBeRunning)
			}
			calls := h.takeCalls()
			if got, want := remoteCommands(calls), []string{"hibd version --json", "hibd image build dev --json --events"}; !slices.Equal(got, want) {
				t.Errorf("remote commands = %q, want one build: %q", got, want)
			}
			for _, call := range calls {
				if call[0] == "az" {
					t.Errorf("hib ran %q after the build started, want no wake", call)
				}
			}
		})
	}
}

// Once hibd has sent anything, ssh's 255 means the connection broke rather than the host slept, so
// hib neither wakes the host nor starts the build again.
func TestImageBuildIsNotRetriedOnceHibdWasHeard(t *testing.T) {
	started := eventLine(t, hibapi.Event{Image: "dev", Size: "medium", Step: "1/2", Text: "RUN make", State: hibapi.EventStarted})
	reply := `{"v":1,"sizes":["medium"]}` + "\n"
	for _, tc := range []struct{ name, stdout string }{
		{"an event and the reply", started + reply},
		{"the reply alone", reply},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHost(t)
			c, term := liveClient(t, h, "xterm-256color")
			h.states = []string{"VM deallocated"}
			h.override = func(words []string, stdout, _ io.Writer) (int, bool) {
				if !slices.Equal(words[:2], []string{"image", "build"}) {
					return 0, false
				}
				io.WriteString(stdout, tc.stdout)
				return 255, true
			}
			if code := c.hib("image", "build", "dev"); code != 1 {
				t.Errorf("exit %d, want 1: %q", code, term)
			}
			calls := h.takeCalls()
			want := []string{"hibd version --json", "hibd image build dev --json --events"}
			if got := remoteCommands(calls); !slices.Equal(got, want) {
				t.Errorf("remote commands = %q, want one build: %q", got, want)
			}
			for _, call := range calls {
				if call[0] == "az" {
					t.Errorf("hib ran %q after hibd answered, want no wake", call)
				}
			}
		})
	}
}

func TestImageBuildPassesStderrOnWithAnOlderHibd(t *testing.T) {
	h := newHost(t)
	c, term := liveClient(t, h, "xterm-256color")
	h.override = func(words []string, stdout, _ io.Writer) (int, bool) {
		if words[0] != "version" {
			return 0, false
		}
		fmt.Fprintln(stdout, `{"v":1,"version":"0.4.0"}`)
		return 0, true
	}
	if code := c.hib("image", "build", "dev"); code != 0 {
		t.Fatalf("exit %d: %q", code, term)
	}
	if got, want := remoteCommands(h.takeCalls()), []string{"hibd version --json", "hibd image build dev --json"}; !slices.Equal(got, want) {
		t.Errorf("remote commands = %q, want %q", got, want)
	}
	if got := term.String(); strings.Contains(got, "\x1b") || !strings.Contains(got, "built dev (medium) in 0s\n") {
		t.Errorf("stderr %q, want hibd's lines as they were", got)
	}
}

func TestImageBuildVerbose(t *testing.T) {
	for _, tc := range []struct {
		name     string
		features string
		want     string
	}{
		{"newer hibd", `"features":["build-events"]`, "hibd image build dev --json --verbose"},
		{"older hibd", `"features":[]`, "hibd image build dev --json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHost(t)
			c, term := liveClient(t, h, "xterm-256color")
			h.override = func(words []string, stdout, _ io.Writer) (int, bool) {
				if words[0] != "version" {
					return 0, false
				}
				fmt.Fprintf(stdout, `{"v":1,"version":"0.5.0",%s}`+"\n", tc.features)
				return 0, true
			}
			if code := c.hib("image", "build", "dev", "--verbose"); code != 0 {
				t.Fatalf("exit %d: %q", code, term)
			}
			if got, want := remoteCommands(h.takeCalls()), []string{"hibd version --json", tc.want}; !slices.Equal(got, want) {
				t.Errorf("remote commands = %q, want %q", got, want)
			}
			if got := term.String(); strings.Contains(got, "\x1b") {
				t.Errorf("stderr %q, want no live line with --verbose", got)
			}
		})
	}
}
