package hibd

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
)

func TestRecordedBuildFailureTail(t *testing.T) {
	builds := recordedBuilds(t)
	build := builds[len(builds)-1]
	a := buildEntriesApp(t, build.Entries, errors.New(build.Failure))
	if code := a.Run(t.Context(), []string{"image", "build", "dev"}); code != 1 {
		t.Fatalf("exit %d, want failure", code)
	}
	got := a.Stderr.(*bytes.Buffer).String()
	for _, text := range []string{"2/2  RUN npm i -g @openai/codex root  failed",
		"npm warn registry unavailable", "npm error code ECONNRESET", "npm error network connection reset"} {
		if !strings.Contains(got, text) {
			t.Errorf("stderr %s, missing %q", got, text)
		}
	}
	if strings.Index(got, "npm error network") > strings.Index(got, "hibd image build:") {
		t.Fatal("failure tail printed after the error")
	}
}

func TestBuildFailureTailIsBoundedAndOnlyKeepsRunningStep(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for _, lineSize := range []int{10, 400, 10000} {
		t.Run(fmt.Sprint(lineSize), func(t *testing.T) {
			entries := []engine.LogEntry{
				{Time: at, Level: "info", Message: "[builder 1/2] RUN old [a0b1]"},
				{Time: at, Level: "debug", Message: "old output must not survive"},
				{Time: at, Level: "info", Message: "[builder 2/2] RUN broken [b1c2]"},
			}
			for i := range 1000 {
				entries = append(entries, engine.LogEntry{Time: at.Add(time.Second), Level: "debug",
					Message: fmt.Sprintf("line%04d:%s", i, strings.Repeat("x", lineSize))})
			}
			a := buildEntriesApp(t, entries, errors.New("exit status 1"))
			if code := a.Run(t.Context(), []string{"image", "build", "dev"}); code != 1 {
				t.Fatalf("exit %d", code)
			}
			got := a.Stderr.(*bytes.Buffer).String()
			start := strings.Index(got, "  failed\n")
			end := strings.Index(got, "hibd image build:")
			if start < 0 || end < start {
				t.Fatalf("missing tail boundaries: %s", got)
			}
			tail := got[start+len("  failed\n") : end]
			if len(tail) > 4096 || strings.Count(tail, "\n") > 20 ||
				strings.Contains(tail, "old output") || strings.Contains(tail, "line0000") {
				t.Fatalf("unbounded or wrong step tail: %q", tail)
			}
			if lineSize == 10 && (!strings.Contains(tail, "line0980") || !strings.Contains(tail, "line0999")) {
				t.Fatalf("did not keep the last twenty lines: %q", tail)
			}
		})
	}
}
