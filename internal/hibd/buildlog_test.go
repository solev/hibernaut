package hibd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
)

type recordedBuild struct {
	Size    string
	Failure string
	Entries []engine.LogEntry
}

func recordedBuilds(t *testing.T) []recordedBuild {
	t.Helper()
	data, err := os.ReadFile("testdata/buildlog.json")
	if err != nil {
		t.Fatal(err)
	}
	var builds []recordedBuild
	if err := json.Unmarshal(data, &builds); err != nil {
		t.Fatal(err)
	}
	return builds
}

func TestRecordedBuildSteps(t *testing.T) {
	builds := recordedBuilds(t)
	e := fake.New(fake.WithBuildLogs(func(_ context.Context, spec engine.TemplateSpec, emit func(engine.LogEntry)) error {
		size, _ := images.SizeWithMemory(spec.MemoryMB)
		for _, build := range builds {
			if build.Size == size.Name() && build.Failure == "" {
				for _, entry := range build.Entries {
					emit(entry)
				}
				return nil
			}
		}
		return errors.New("no recording for size")
	}))
	a, _, errOut := newApp(t, e)
	if code := a.Run(t.Context(), []string{"image", "build", "dev"}); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{
		"dev (medium)  base  FROM ubuntu:24.04  reused",
		"dev (medium)  base  USER user  reused",
		"dev (medium)  1/2  RUN apt-get install -y git curl root  reused",
		"dev (medium)  2/2  RUN npm i -g @openai/codex root  built in 41s",
		"dev (medium)  finalize  Finalizing template build  built in 5s",
		"dev (small)  2/2  RUN npm i -g @openai/codex root  reused",
		"dev (large)  base  FROM ubuntu:24.04  built in 10s",
		"dev (large)  finalize  Finalizing template build  built in 8s",
	} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("stderr %s, want %q", errOut, want)
		}
	}
	if strings.Contains(errOut.String(), "added 1 package") {
		t.Errorf("ordinary step output leaked: %s", errOut)
	}
}

func buildEntriesApp(t *testing.T, entries []engine.LogEntry, buildErr error) *App {
	t.Helper()
	e := fake.New(fake.WithBuildLogs(func(_ context.Context, _ engine.TemplateSpec, emit func(engine.LogEntry)) error {
		for _, entry := range entries {
			emit(entry)
		}
		return buildErr
	}))
	a, _, _ := newApp(t, e)
	return a
}

func TestBuildStepLines(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		message string
		want    string
	}{
		{"cached", "CACHED [builder 3/12] RUN apt-get install -y git curl [a0b1]", "3/12  RUN apt-get install -y git curl  reused"},
		{"base", "[base] FROM ubuntu:24.04 [a0b1]", "base  FROM ubuntu:24.04  built in 2s"},
		{"finalize", "[finalize] Finalizing template build [a0b1]", "finalize  Finalizing template build  built in 2s"},
		{"multiline command", "[builder 1/2] RUN printf 'first\\nsecond'\nroot [a0b1]", `1/2  RUN printf 'first\nsecond'\x0aroot  built in 2s`},
		{"brackets in command", "[builder 1/2] RUN test [ -f /x ] [a0b1]", "1/2  RUN test [ -f /x ]  built in 2s"},
		{"unknown prefix", "[future] New phase [a0b1]", "[future] New phase [a0b1]"},
		{"fragment", "[builder 1/", "[builder 1/"},
		{"no headers", "Provisioning sandbox template", "Provisioning sandbox template"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := buildEntriesApp(t, []engine.LogEntry{
				{Time: at, Level: "info", Message: tc.message},
				{Time: at.Add(1500 * time.Millisecond), Level: "debug", Message: "last output"},
			}, nil)
			if code := a.Run(t.Context(), []string{"image", "build", "dev"}); code != 0 {
				t.Fatalf("exit %d: %s", code, a.Stderr)
			}

			if !strings.Contains(a.Stderr.(*bytes.Buffer).String(), tc.want) {
				t.Fatalf("stderr %s, want %q", a.Stderr, tc.want)
			}
		})
	}
}

func TestBuildVerboseReplacesStepLines(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	long := strings.Repeat("界", 2000) + "\x1b[31munsafe"
	entries := []engine.LogEntry{
		{Time: at, Level: "info", Message: "[builder 1/1] RUN " + long + " [a0b1]"},
		{Time: at.Add(time.Second), Level: "debug", Message: "command output"},
	}
	for _, verbose := range []bool{false, true} {
		t.Run(fmt.Sprint(verbose), func(t *testing.T) {
			a := buildEntriesApp(t, entries, nil)
			args := []string{"image", "build", "dev"}
			if verbose {
				args = append(args, "-verbose")
			}
			if code := a.Run(t.Context(), args); code != 0 {
				t.Fatalf("exit %d: %s", code, a.Stderr)
			}
			got := a.Stderr.(*bytes.Buffer).String()
			if strings.ContainsRune(got, '\x1b') {
				t.Fatal("raw escape in output")
			}
			if verbose {
				if !strings.Contains(got, strings.Repeat("界", 2000)) || !strings.Contains(got, "command output") ||
					strings.Contains(got, "dev (medium)  1/1") {
					t.Fatalf("verbose output was shortened or included step lines: %s", got)
				}
			} else {
				for _, line := range strings.Split(got, "\n") {
					if len([]rune(line)) > 100 || strings.Contains(line, "command output") {
						t.Fatalf("default output is too wide or prints full logs: %q", line)
					}
				}
				if !strings.Contains(got, "…  built in 1s") {
					t.Fatalf("missing shortened step: %s", got)
				}
			}
		})
	}
}
