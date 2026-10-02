package termsafe

import (
	"bytes"
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
	} {
		if got := String(in); got != want {
			t.Errorf("String(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWriterSplitsAnywhere(t *testing.T) {
	in := "ok 🚀 é 世\x1b[31m red \xff\xe2\x82\xac\x9b end \xf0\x9f"
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
