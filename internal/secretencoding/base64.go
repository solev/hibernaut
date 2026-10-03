// Package secretencoding builds patterns for secrets embedded in encoded data.
package secretencoding

import (
	"bytes"
	"encoding/base64"
)

// Base64Core returns the characters determined only by value at the given byte alignment.
// Boundary characters that also contain bits from adjacent bytes are excluded.
func Base64Core(enc *base64.Encoding, value []byte, alignment int) []byte {
	data := append(bytes.Repeat([]byte{0}, alignment), value...)
	data = append(data, 0, 0)
	text := enc.EncodeToString(data)
	start := (alignment*8 + 5) / 6
	end := (alignment*8 + len(value)*8) / 6
	return []byte(text[start:end])
}

// Base64End includes the last partial group and padding when value ends the encoded data.
// The first mixed character is excluded as in Base64Core.
func Base64End(enc *base64.Encoding, value []byte, alignment int) []byte {
	data := append(bytes.Repeat([]byte{0}, alignment), value...)
	text := enc.EncodeToString(data)
	start := (alignment*8 + 5) / 6
	return []byte(text[start:])
}
