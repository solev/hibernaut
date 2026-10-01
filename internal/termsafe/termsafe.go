// Package termsafe makes text from elsewhere safe to print to a terminal. Escape sequences in
// it could otherwise retitle the window, rewrite what's on screen or set the clipboard.
package termsafe

import (
	"fmt"
	"io"
	"unicode/utf8"
)

// String returns s with control characters other than tab, newline and carriage return, and
// bytes that aren't UTF-8, written as Go escapes such as \x1b.
func String(s string) string {
	return string(appendSafe(nil, []byte(s)))
}

// Writer escapes what's written to it as String does, and writes the result on. Call Flush
// after the last Write: a character split across writes waits for its last bytes.
type Writer struct {
	w       io.Writer
	pending []byte
}

func NewWriter(w io.Writer) *Writer { return &Writer{w: w} }

func (w *Writer) Write(p []byte) (int, error) {
	data := append(w.pending, p...)
	n := len(data)
	// Hold back a last character that more bytes could complete.
	for i := n - 1; i >= max(0, n-utf8.UTFMax); i-- {
		if utf8.RuneStart(data[i]) {
			if !utf8.FullRune(data[i:]) {
				n = i
			}
			break
		}
	}
	w.pending = append([]byte(nil), data[n:]...)
	if _, err := w.w.Write(appendSafe(nil, data[:n])); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Flush writes bytes held back for a character that never finished, escaped.
func (w *Writer) Flush() error {
	if len(w.pending) == 0 {
		return nil
	}
	_, err := w.w.Write(appendSafe(nil, w.pending))
	w.pending = nil
	return err
}

func appendSafe(dst, p []byte) []byte {
	for len(p) > 0 {
		r, size := utf8.DecodeRune(p)
		switch {
		case r == utf8.RuneError && size == 1:
			dst = fmt.Appendf(dst, `\x%02x`, p[0])
		case r == '\t' || r == '\n' || r == '\r':
			dst = append(dst, p[:size]...)
		case r < 0x20 || r == 0x7f:
			dst = fmt.Appendf(dst, `\x%02x`, r)
		case r >= 0x80 && r < 0xa0: // C1 controls, such as CSI
			dst = fmt.Appendf(dst, `\u%04x`, r)
		default:
			dst = append(dst, p[:size]...)
		}
		p = p[size:]
	}
	return dst
}
