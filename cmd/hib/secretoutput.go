package main

import (
	"bytes"
	"encoding/json"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/solev/hibernaut/internal/termsafe"
)

func (a *app) hidingSecret(value string) (*app, func()) {
	forms := printedForms(value)
	stdout := newSecretWriter(a.stdout, forms...)
	stderr := newSecretWriter(a.stderr, forms...)
	hidden := *a
	hidden.stdout, hidden.stderr = stdout, stderr
	return &hidden, func() {
		stdout.Flush()
		stderr.Flush()
	}
}

func printedForms(value string) []string {
	var forms []string
	for _, text := range append([]string{value}, quotedForms(value)...) {
		for _, form := range []string{text, termsafe.String(text), termsafe.Line(text)} {
			if form != "" && !slices.Contains(forms, form) {
				forms = append(forms, form)
			}
		}
	}
	return forms
}

func quotedForms(value string) []string {
	quoted := []string{strconv.Quote(value)}
	for _, escapeHTML := range []bool{true, false} {
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(escapeHTML)
		if enc.Encode(value) == nil {
			quoted = append(quoted, strings.TrimSuffix(b.String(), "\n"))
		}
	}
	for i, q := range quoted {
		quoted[i] = q[1 : len(q)-1]
	}
	return quoted
}

type secretWriter struct {
	w       io.Writer
	forms   [][]byte
	pending []byte
	hidden  []bool
	inMatch bool
}

func newSecretWriter(w io.Writer, forms ...string) *secretWriter {
	sw := &secretWriter{w: w}
	for _, form := range forms {
		if form != "" {
			sw.forms = append(sw.forms, []byte(form))
		}
	}
	return sw
}

func (w *secretWriter) Write(p []byte) (int, error) {
	if len(w.forms) == 0 {
		return w.w.Write(p)
	}
	w.pending = append(w.pending, p...)
	w.hidden = append(w.hidden, make([]bool, len(p))...)
	w.hideMatches()
	return len(p), w.print(w.couldStartMatch())
}

func (w *secretWriter) Flush() error {
	return w.print(len(w.pending))
}

func (w *secretWriter) hideMatches() {
	for _, form := range w.forms {
		for start := 0; ; start++ {
			i := bytes.Index(w.pending[start:], form)
			if i < 0 {
				break
			}
			start += i
			for j := range form {
				w.hidden[start+j] = true
			}
		}
	}
}

func (w *secretWriter) couldStartMatch() int {
	for start := range w.pending {
		rest := w.pending[start:]
		for _, form := range w.forms {
			if len(rest) < len(form) && bytes.HasPrefix(form, rest) {
				return start
			}
		}
	}
	return len(w.pending)
}

func (w *secretWriter) print(n int) error {
	var out []byte
	for i, b := range w.pending[:n] {
		switch {
		case !w.hidden[i]:
			out = append(out, b)
		case !w.inMatch:
			out = append(out, "[redacted]"...)
		}
		w.inMatch = w.hidden[i]
	}
	w.pending = append(w.pending[:0], w.pending[n:]...)
	w.hidden = append(w.hidden[:0], w.hidden[n:]...)
	if len(out) == 0 {
		return nil
	}
	_, err := w.w.Write(out)
	return err
}
