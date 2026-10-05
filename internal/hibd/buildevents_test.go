package hibd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/hibapi"
)

func buildReplies(t *testing.T, output *bytes.Buffer) []hibapi.Response {
	t.Helper()
	var replies []hibapi.Response
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 4096), 64<<10)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line)+1 > 64<<10 {
			t.Fatal("line exceeds 64 KiB")
		}
		for _, b := range line {
			if b < 32 || b > 126 {
				t.Fatalf("non-ASCII JSON byte %x", b)
			}
		}
		var reply hibapi.Response
		if err := json.Unmarshal(line, &reply); err != nil || reply.V != hibapi.Version {
			t.Fatalf("invalid response: %v, %+v", err, reply)
		}
		replies = append(replies, reply)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return replies
}

func TestBuildEventsStartEndAndFinalReply(t *testing.T) {
	build := recordedBuilds(t)[0]
	a := buildEntriesApp(t, build.Entries, nil)
	if code := a.Run(t.Context(), []string{"image", "build", "-json", "-events", "dev"}); code != 0 {
		t.Fatalf("exit %d: %s", code, a.Stderr)
	}
	replies := buildReplies(t, a.Stdout.(*bytes.Buffer))
	if len(replies) != 31 {
		t.Fatalf("got %d replies, want five step pairs per size, then final", len(replies))
	}
	for i, size := range []string{"medium", "small", "large"} {
		offset := i * 10
		for j, step := range []string{"base", "base", "1/2", "2/2", "finalize"} {
			start, end := replies[offset+j*2].Event, replies[offset+j*2+1].Event
			if start == nil || end == nil || start.Step != step || start.Image != "dev" || start.Size != size ||
				start.State != hibapi.EventStarted || end.Step != step || end.Size != size {
				t.Fatalf("wrong step pair %+v, %+v", start, end)
			}
		}
		built := replies[offset+7].Event
		if built.State != hibapi.EventBuilt || built.Seconds != 41 {
			t.Fatalf("built step %+v, want 41s", built)
		}
		if replies[offset+5].Event.State != hibapi.EventReused {
			t.Fatal("cached step was not reused")
		}
	}
	last := replies[len(replies)-1]
	if last.Event != nil || last.Template == "" || !slices.Equal(last.Sizes, []string{"medium", "small", "large"}) {
		t.Fatalf("final reply %+v", last)
	}
	if strings.Contains(a.Stderr.(*bytes.Buffer).String(), "dev (medium)  ") {
		t.Fatal("step lines printed in event mode")
	}
}

func TestBuildEventsFailureAndHugeText(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	a := buildEntriesApp(t, []engine.LogEntry{
		{Time: at, Level: "info", Message: "[builder 1/1] RUN " + strings.Repeat("界😀\t\x7f", 20000) + " [a0b1]"},
		{Time: at.Add(3 * time.Second), Level: "error", Message: "failed output\x1b[31m\nlast output"},
	}, errors.New("exit status 1"))
	if code := a.Run(t.Context(), []string{"image", "build", "dev", "-json", "-events"}); code != 1 {
		t.Fatalf("exit %d", code)
	}
	replies := buildReplies(t, a.Stdout.(*bytes.Buffer))
	if len(replies) != 3 || replies[1].Event.State != hibapi.EventFailed || replies[1].Event.Seconds != 3 ||
		replies[2].Event != nil || replies[2].Error == "" || !strings.HasSuffix(replies[0].Event.Text, "…") {
		t.Fatalf("unexpected replies: %d", len(replies))
	}
	if !strings.Contains(a.Stderr.(*bytes.Buffer).String(), "last output") {
		t.Fatal("event mode did not print the failure tail")
	}
}

func TestBuildEventsRequireJSONAndAreAdvertised(t *testing.T) {
	a := buildEntriesApp(t, nil, nil)
	if code := a.Run(t.Context(), []string{"image", "build", "dev", "-events"}); code != 2 ||
		a.Stdout.(*bytes.Buffer).Len() != 0 || !strings.Contains(a.Stderr.(*bytes.Buffer).String(), "-events requires -json") {
		t.Fatalf("missing usage error: exit %d, stderr %s", code, a.Stderr)
	}
	a.Stdout.(*bytes.Buffer).Reset()
	if code := a.Run(t.Context(), []string{"version", "-json"}); code != 0 {
		t.Fatalf("version exit %d", code)
	}
	replies := buildReplies(t, a.Stdout.(*bytes.Buffer))
	if len(replies) != 1 || !slices.Contains(replies[0].Features, hibapi.FeatureBuildEvents) {
		t.Fatal("build-events not advertised")
	}
}
