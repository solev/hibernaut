// Package secretencoding builds patterns for secrets embedded in encoded data.
package secretencoding

import "encoding/base64"

// Base64Core returns the characters determined only by value at the given byte alignment.
// Boundary characters that also contain bits from adjacent bytes are excluded. Alignment is taken
// modulo 3, the bytes in a whole base64 group. An empty value has no characters.
func Base64Core(enc *base64.Encoding, value []byte, alignment int) []byte {
	if len(value) == 0 {
		return nil
	}
	text, start, end := encodeAligned(enc, value, alignment, 2)
	return []byte(text[start:end])
}

// Base64End includes the last partial group and padding when value ends the encoded data.
// The first mixed character is excluded as in Base64Core.
func Base64End(enc *base64.Encoding, value []byte, alignment int) []byte {
	if len(value) == 0 {
		return nil
	}
	text, start, _ := encodeAligned(enc, value, alignment, 0)
	return []byte(text[start:])
}

func encodeAligned(enc *base64.Encoding, value []byte, alignment, trailing int) (text string, start, end int) {
	alignment = (alignment%3 + 3) % 3
	data := make([]byte, alignment+len(value)+trailing)
	copy(data[alignment:], value)
	return enc.EncodeToString(data), (alignment*8 + 5) / 6, (alignment + len(value)) * 8 / 6
}
