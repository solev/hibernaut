package main

import (
	"bytes"
	"io"
)

// secretWriter redacts a known value even when a command writes it in separate chunks.
type secretWriter struct {
	w       io.Writer
	value   []byte
	pending []byte
}

func (w *secretWriter) Write(p []byte) (int, error) {
	if len(w.value) == 0 {
		return w.w.Write(p)
	}
	n := len(p)
	data := append(w.pending, p...)
	var out []byte
	for {
		i := bytes.Index(data, w.value)
		if i < 0 {
			break
		}
		out = append(out, data[:i]...)
		out = append(out, "[redacted]"...)
		data = data[i+len(w.value):]
	}
	keep := min(len(data), len(w.value)-1)
	for keep > 0 && !bytes.Equal(data[len(data)-keep:], w.value[:keep]) {
		keep--
	}
	out = append(out, data[:len(data)-keep]...)
	w.pending = append(w.pending[:0], data[len(data)-keep:]...)
	_, err := w.w.Write(out)
	return n, err
}

func (w *secretWriter) Flush() error {
	_, err := w.w.Write(w.pending)
	w.pending = nil
	return err
}
