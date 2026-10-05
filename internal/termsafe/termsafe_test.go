package termsafe

import (
	"bytes"
	"strings"
	"testing"
)

func TestString(t *testing.T) {
	for in, want := range map[string]string{
		"plain text":                      "plain text",
		"tabs\tand\nlines\r\n":            "tabs\tand\nlines\r\n",
		"héllo, 世界 🚀":                     "héllo, 世界 🚀",
		"\x1b]52;c;cHduZWQ=\x07":          `\x1b]52;c;cHduZWQ=\x07`,
		"\x1b[2J\x1b[H":                   `\x1b[2J\x1b[H`,
		"bell\x07 nul\x00 del\x7f":        `bell\x07 nul\x00 del\x7f`,
		"c1 csi \u009b31m":                `c1 csi \u009b31m`,
		"bad \xff\xfe utf8 \xe2\x82":      `bad \xff\xfe utf8 \xe2\x82`,
		"lone continuation \x80":          `lone continuation \x80`,
		"\u00a0 nbsp is printable \u00ff": "\u00a0 nbsp is printable \u00ff",
		"app\u202egpj.exe":                `app\u202egpj.exe`,
		"\u061c\u200e\u200f\u202a\u202b\u202c\u202d\u202e\u2066\u2067\u2068\u2069": `\u061c\u200e\u200f\u202a\u202b\u202c\u202d\u202e\u2066\u2067\u2068\u2069`,
		"\u200d zero width joiner and \u2028 stay":                                 "\u200d zero width joiner and \u2028 stay",
	} {
		if got := String(in); got != want {
			t.Errorf("String(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWriterSplitsAnywhere(t *testing.T) {
	in := "ok 🚀 é 世\x1b[31m red \xff\xe2\x82\xac\x9b \u202e rlo \u2066 end \xf0\x9f"
	want := String(in)
	for size := 1; size <= len(in); size++ {
		var out bytes.Buffer
		w := NewWriter(&out)
		for i := 0; i < len(in); i += size {
			chunk := in[i:min(i+size, len(in))]
			if n, err := w.Write([]byte(chunk)); n != len(chunk) || err != nil {
				t.Fatalf("Write = %d, %v", n, err)
			}
		}
		if err := w.Flush(); err != nil {
			t.Fatal(err)
		}
		if out.String() != want {
			t.Errorf("in chunks of %d: %q, want %q", size, out.String(), want)
		}
	}
}

func TestStringDropsColours(t *testing.T) {
	long := "\x1b[" + strings.Repeat("1", maxColour) + "m"
	for in, want := range map[string]string{
		"Welcome to Codex [v\x1b[90m0.160.0\x1b[0m]":   "Welcome to Codex [v0.160.0]",
		"   \x1b[94mDK1O-ZJVSW\x1b[0m\n":               "   DK1O-ZJVSW\n",
		"\x1b[m\x1b[1;38;5;196m\x1b[38:2::255:0:0mred": "red",
		"\x1b\x1b[0m[2J":     `\x1b[2J`,
		"unfinished \x1b[31": `unfinished \x1b[31`,
		"\x1b[31;2J":         `\x1b[31;2J`,
		long:                 `\x1b` + long[1:],
	} {
		if got := String(in); got != want {
			t.Errorf("String(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWriterDropsColoursSplitAnywhere(t *testing.T) {
	in := "Welcome to Codex [v\x1b[90m0.160.0\x1b[0m]\n   \x1b[94mDK1O-ZJVSW\x1b[0m\n"
	want := "Welcome to Codex [v0.160.0]\n   DK1O-ZJVSW\n"
	for size := 1; size <= len(in); size++ {
		var out bytes.Buffer
		w := NewWriter(&out)
		for i := 0; i < len(in); i += size {
			w.Write([]byte(in[i:min(i+size, len(in))]))
		}
		w.Flush()
		if out.String() != want {
			t.Errorf("in chunks of %d: %q, want %q", size, out.String(), want)
		}
	}
}

func TestWriterFlushEscapesAnUnfinishedColour(t *testing.T) {
	var out bytes.Buffer
	w := NewWriter(&out)
	w.Write([]byte("x\x1b[3"))
	if out.String() != "x" {
		t.Errorf("before Flush: %q, want %q", out.String(), "x")
	}
	w.Flush()
	if want := `x\x1b[3`; out.String() != want {
		t.Errorf("after Flush: %q, want %q", out.String(), want)
	}
}

func TestWriterNeverPassesEscape(t *testing.T) {
	var out bytes.Buffer
	w := NewWriter(&out)
	for _, b := range []byte("\x1b\x9b\xc2\x9b\x07") {
		w.Write([]byte{b})
	}
	w.Flush()
	if want := `\x1b\x9b\u009b\x07`; out.String() != want {
		t.Errorf("wrote %q, want %q", out.String(), want)
	}
}

func TestLine(t *testing.T) {
	for in, want := range map[string]string{
		"plain text":                 "plain text",
		"tabs\tand\nlines\r\n":       `tabs\x09and\x0alines\x0d\x0a`,
		"\x1b[31mred\x1b[0m \x1b[2J": `red \x1b[2J`,
		"name\u202e and \u009b \xff": `name\u202e and \u009b \xff`,
		"héllo, 世界 🚀":                "héllo, 世界 🚀",
	} {
		if got := Line(in); got != want {
			t.Errorf("Line(%q) = %q, want %q", in, got, want)
		}
	}
}
