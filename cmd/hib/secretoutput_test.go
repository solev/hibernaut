package main

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/solev/hibernaut/internal/hibapi"
)

func chunkings(s string) map[string][]string {
	chunks := map[string][]string{"one write": {s}}
	var bytewise []string
	for i := range len(s) {
		bytewise = append(bytewise, s[i:i+1])
	}
	chunks["byte by byte"] = bytewise
	for i := 1; i < len(s); i++ {
		chunks[fmt.Sprintf("split at %d", i)] = []string{s[:i], s[i:]}
	}
	return chunks
}

func writeChunks(w io.Writer, chunks []string) {
	for _, chunk := range chunks {
		io.WriteString(w, chunk)
	}
}

func TestSecretWriterHidesAQuotedFormThatOverlapsTheValue(t *testing.T) {
	const value = `sk-review"1234567890s`
	quoted := strconv.Quote(value)
	quoted = quoted[1 : len(quoted)-1]
	reflected := quoted[:len(quoted)-1] + value
	for _, input := range []string{reflected, "before " + reflected + " after"} {
		want := strings.Replace(input, reflected, "[redacted]", 1)
		for name, chunks := range chunkings(input) {
			var stdout, stderr bytes.Buffer
			hidden, flush := (&app{stdout: &stdout, stderr: &stderr}).hidingSecret(value)
			writeChunks(hidden.stdout, chunks)
			writeChunks(hidden.stderr, chunks)
			flush()
			for stream, got := range map[string]string{"stdout": stdout.String(), "stderr": stderr.String()} {
				if got != want {
					t.Errorf("%s, %s: printed %q, want %q", name, stream, got, want)
				}
				if strings.Contains(got, value[1:]) || strings.Contains(got, quoted[1:]) {
					t.Errorf("%s, %s: part of the value reached the output: %q", name, stream, got)
				}
			}
		}
	}
}

func TestSecretAddHidesAValueRightAfterItsQuotedForm(t *testing.T) {
	const value = `sk-review"1234567890s`
	quoted := strconv.Quote(value)
	reflected := "token refused: " + quoted[1:len(quoted)-2] + value
	h := newHost(t)
	c := initClient(t, h)
	h.override = func(_ []string, stdout, stderr io.Writer) (int, bool) {
		io.WriteString(stderr, reflected+"\n")
		return writeTestResponse(stdout, hibapi.Response{Error: reflected}), true
	}
	c.app.stdin = strings.NewReader(value)
	c.hib("secret", "add", "api", "--env", "API_KEY", "--bind", "api.example.com")
	output := c.out.String() + c.errOut.String()
	if strings.Contains(output, "review") || strings.Contains(output, "1234567890") {
		t.Fatalf("part of the value reached the output: %q", output)
	}
	if got := strings.Count(output, "token refused: [redacted]\n"); got != 2 {
		t.Errorf("%d redacted lines, want 2: %q", got, output)
	}
}

func TestSecretWriterMergesMatchesThatOverlapOrTouch(t *testing.T) {
	for _, tc := range []struct {
		name  string
		forms []string
		input string
		want  string
	}{
		{"a form inside a longer one", []string{"abc", "xxabcxx"}, "xxabcxx", "[redacted]"},
		{"the longer form first", []string{"xxabcxx", "abc"}, "xxabcxx", "[redacted]"},
		{"only the shorter form", []string{"xxabcxx", "abc"}, "-xxabc-", "-xx[redacted]-"},
		{"two overlapping occurrences", []string{"abab"}, "ababab", "[redacted]"},
		{"overlapping occurrences and a tail", []string{"abab"}, "ababa!", "[redacted]a!"},
		{"two touching occurrences", []string{"abc"}, "abcabc", "[redacted]"},
		{"touching different forms", []string{"abc", "xyz"}, "<xyzabc>", "<[redacted]>"},
		{"separate occurrences", []string{"abc"}, "abc-abc", "[redacted]-[redacted]"},
		{"a run of one letter", []string{"aaa"}, "aaaaa b aa", "[redacted] b aa"},
		{"no match", []string{"abc"}, "ab ac bc", "ab ac bc"},
	} {
		for name, chunks := range chunkings(tc.input) {
			var out bytes.Buffer
			w := newSecretWriter(&out, tc.forms...)
			writeChunks(w, chunks)
			w.Flush()
			if out.String() != tc.want {
				t.Errorf("%s, %s: printed %q, want %q", tc.name, name, out.String(), tc.want)
			}
		}
	}
}

func TestSecretWriterHoldsBackOnlyWhatCouldStartAMatch(t *testing.T) {
	var out bytes.Buffer
	w := newSecretWriter(&out, "secret")
	io.WriteString(w, "the sec")
	if out.String() != "the " {
		t.Fatalf("printed %q before the match could finish, want %q", out.String(), "the ")
	}
	w.Flush()
	if out.String() != "the sec" {
		t.Errorf("printed %q after Flush, want the unfinished match as is", out.String())
	}
}

func TestSecretWriterFlushKeepsRedactingAMatchThatBeganEarlier(t *testing.T) {
	var out bytes.Buffer
	w := newSecretWriter(&out, "abab")
	io.WriteString(w, "ababa")
	if out.String() != "[redacted]" {
		t.Fatalf("printed %q, want the match so far", out.String())
	}
	w.Flush()
	if out.String() != "[redacted]a" {
		t.Errorf("printed %q after Flush, want %q", out.String(), "[redacted]a")
	}
}

func TestSecretWriterWithoutAValuePassesWritesThrough(t *testing.T) {
	var out bytes.Buffer
	w := newSecretWriter(&out, "")
	io.WriteString(w, "anything at all")
	if out.String() != "anything at all" {
		t.Errorf("printed %q, want the write as is", out.String())
	}
}
