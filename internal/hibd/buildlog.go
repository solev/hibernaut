package hibd

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/termsafe"
)

type buildStep struct {
	name, text string
	at         time.Time
	cached     bool
}

func logHeader(entry engine.LogEntry) (buildStep, bool) {
	if entry.Level != "info" {
		return buildStep{}, false
	}
	message, cached := strings.CutPrefix(entry.Message, "CACHED ")
	prefix, rest, ok := strings.Cut(message, "] ")
	if !ok || !strings.HasPrefix(prefix, "[") || !strings.HasSuffix(rest, "]") {
		return buildStep{}, false
	}
	name := strings.TrimPrefix(prefix, "[")
	hash := strings.LastIndex(rest, " [")
	if hash < 1 || hash+2 == len(rest)-1 {
		return buildStep{}, false
	}
	text := rest[:hash]
	if len(text) > maxBuildLine {
		text = firstBytes(text, maxBuildLine) + "…"
	}
	return buildStep{name: name, text: strings.Clone(text), at: entry.Time, cached: cached}, true
}

func stepName(name string) (string, bool) {
	switch {
	case name == "base", name == "finalize":
		return name, true
	case strings.HasPrefix(name, "builder "):
		name = strings.TrimPrefix(name, "builder ")
		n, total, ok := strings.Cut(name, "/")
		step, stepErr := strconv.Atoi(n)
		count, countErr := strconv.Atoi(total)
		if !ok || stepErr != nil || countErr != nil || step < 1 || step > count {
			return "", false
		}
		return fmt.Sprintf("%d/%d", step, count), true
	default:
		return "", false
	}
}

func stepResult(step buildStep, end time.Time, failed bool) (hibapi.EventState, int) {
	seconds := max(0, int(end.Sub(step.at).Round(time.Second)/time.Second))
	switch {
	case failed:
		return hibapi.EventFailed, seconds
	case step.cached:
		return hibapi.EventReused, seconds
	default:
		return hibapi.EventBuilt, seconds
	}
}

func shortenLine(text string, columns int) string {
	width := 0
	for _, r := range text {
		width += runeColumns(r)
	}
	if width <= columns {
		return text
	}
	width = 0
	for i, r := range text {
		if width+runeColumns(r) > columns-1 {
			return text[:i] + "…"
		}
		width += runeColumns(r)
	}
	return text
}

func runeColumns(r rune) int {
	if r < 128 || r == '…' {
		return 1
	}
	return 2
}

func stepLine(image, size string, step buildStep, state hibapi.EventState, seconds int) string {
	prefix := fmt.Sprintf("%s (%s)  %s  ", termsafe.Line(image), termsafe.Line(size), termsafe.Line(step.name))
	result := string(state)
	if state == hibapi.EventBuilt {
		result = fmt.Sprintf("built in %ds", seconds)
	}
	suffix := "  " + result
	return prefix + shortenLine(termsafe.Line(step.text), 100-len(prefix)-len(suffix)) + suffix
}

type buildLog struct {
	image, size string
	out         io.Writer
	step        *buildStep
	last        time.Time
	verbose     bool
	tail        logTail
	events      io.Writer
	err         error
}

func (b *buildLog) entry(entry engine.LogEntry) {
	b.last = entry.Time
	if b.verbose {
		fmt.Fprintln(b.out, termsafe.String(entry.Message))
	}
	if step, ok := logHeader(entry); ok {
		b.finish(entry.Time, false)
		b.tail = logTail{}
		if name, known := stepName(step.name); known {
			step.name = name
			b.step = &step
			b.tail.add(entry.Message)
			b.event(step, hibapi.EventStarted, 0)
			return
		}
	}
	b.tail.add(entry.Message)
	if !b.verbose && (b.step == nil || strings.HasPrefix(entry.Message, "[") || strings.HasPrefix(entry.Message, "CACHED [")) {
		fmt.Fprintln(b.out, shortenLine(termsafe.Line(entry.Message), 100))
	}
}

func (b *buildLog) finish(end time.Time, failed bool) {
	if b.step != nil {
		state, seconds := stepResult(*b.step, end, failed)
		if !b.verbose && b.events == nil {
			fmt.Fprintln(b.out, stepLine(b.image, b.size, *b.step, state, seconds))
		}
		b.event(*b.step, state, seconds)
		b.step = nil
	}
	if failed {
		for _, line := range b.tail.lines {
			fmt.Fprintln(b.out, line)
		}
	}
}

func (b *buildLog) event(step buildStep, state hibapi.EventState, seconds int) {
	if b.events == nil || b.err != nil {
		return
	}
	data, err := marshalBuildEvent(hibapi.Event{
		Image: b.image, Size: b.size, Step: step.name, Text: step.text, State: state, Seconds: seconds,
	})
	if err == nil {
		_, err = b.events.Write(data)
	}
	if err != nil {
		b.err = fmt.Errorf("writing build events: %w. Check hib image ls before trying again", err)
	}
}

type logTail struct {
	lines []string
	bytes int
}

func (t *logTail) add(message string) {
	for {
		line, rest, more := strings.Cut(message, "\n")
		line = termsafe.Line(lastBytes(line, 4095))
		line = lastBytes(line, 4095)
		t.lines = append(t.lines, line)
		t.bytes += len(line) + 1
		for len(t.lines) > 20 || t.bytes > 4096 {
			t.bytes -= len(t.lines[0]) + 1
			copy(t.lines, t.lines[1:])
			t.lines[len(t.lines)-1] = ""
			t.lines = t.lines[:len(t.lines)-1]
		}
		if !more {
			return
		}
		message = rest
	}
}

func lastBytes(text string, limit int) string {
	if len(text) > limit {
		text = text[len(text)-limit:]
		for len(text) > 0 && !utf8.RuneStart(text[0]) {
			text = text[1:]
		}
	}
	return strings.Clone(text)
}
