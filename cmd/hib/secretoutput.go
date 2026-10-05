package main

import (
	"bytes"
	"io"
	"slices"

	"github.com/solev/hibernaut/internal/termsafe"
)

func (a *app) hidingSecret(value string) (*app, func()) {
	hidden := *a
	var writers []*secretWriter
	for _, form := range printedForms(value) {
		stdout := &secretWriter{w: hidden.stdout, value: []byte(form)}
		stderr := &secretWriter{w: hidden.stderr, value: []byte(form)}
		hidden.stdout, hidden.stderr = stdout, stderr
		writers = append(writers, stdout, stderr)
	}
	return &hidden, func() {
		for _, w := range slices.Backward(writers) {
			w.Flush()
		}
	}
}

func printedForms(value string) []string {
	var forms []string
	for _, form := range []string{value, termsafe.String(value), termsafe.Line(value)} {
		if form != "" && !slices.Contains(forms, form) {
			forms = append(forms, form)
		}
	}
	return forms
}

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
