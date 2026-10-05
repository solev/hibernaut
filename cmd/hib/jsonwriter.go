package main

import (
	"encoding/json"
	"fmt"
	"io"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/solev/hibernaut/internal/hibapi"
)

// shortEscapes are encoding/json's escapes that writeJSON spells as \u escapes instead.
var shortEscapes = map[byte]byte{'b': '\b', 'f': '\f', 'n': '\n', 'r': '\r', 't': '\t'}

func writeJSON(w io.Writer, r hibapi.Response) error {
	r.V = hibapi.Version
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	out := make([]byte, 0, len(data)+1)
	for i := 0; i < len(data); {
		b := data[i]
		switch {
		case b == '\\':
			if c, ok := shortEscapes[data[i+1]]; ok {
				out = fmt.Appendf(out, `\u%04x`, c)
			} else {
				out = append(out, b, data[i+1])
			}
			i += 2
		case b == 0x7f:
			out = append(out, `\u007f`...)
			i++
		case b < utf8.RuneSelf:
			out = append(out, b)
			i++
		default:
			r, size := utf8.DecodeRune(data[i:])
			if r > 0xffff {
				hi, lo := utf16.EncodeRune(r)
				out = fmt.Appendf(out, `\u%04x\u%04x`, hi, lo)
			} else {
				out = fmt.Appendf(out, `\u%04x`, r)
			}
			i += size
		}
	}
	_, err = w.Write(append(out, '\n'))
	return err
}
