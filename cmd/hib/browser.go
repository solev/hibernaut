package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/termsafe"
)

const maxGitHubPromptLine = 1024

func browserOpener(platform string, getenv func(string) string, command func(string) runFunc) func(context.Context, string) error {
	var program string
	switch platform {
	case "darwin":
		program = "open"
	case "linux":
		if getenv("DISPLAY") == "" && getenv("WAYLAND_DISPLAY") == "" {
			return nil
		}
		program = "xdg-open"
	default:
		return nil
	}
	run := command(program)
	return func(ctx context.Context, url string) error {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		code, err := run(ctx, []string{url}, nil, nil, nil)
		if err != nil {
			return err
		}
		if code != 0 {
			return fmt.Errorf("%s exited %d", program, code)
		}
		return nil
	}
}

type githubBrowser struct {
	ctx    context.Context
	open   func(context.Context, string) error
	stderr *lineWriter
	once   sync.Once
	done   sync.WaitGroup
}

func (b *githubBrowser) start() {
	b.once.Do(func() {
		b.done.Go(func() {
			if err := b.open(b.ctx, hibapi.GitHubDeviceURL); err != nil {
				b.stderr.note(fmt.Sprintf("hib: note: couldn't open a browser (%s). Open %s yourself\n",
					termsafe.String(err.Error()), hibapi.GitHubDeviceURL))
			}
		})
	})
}

func (b *githubBrowser) wait() {
	b.done.Wait()
	b.stderr.flushNote()
}

type githubPromptWriter struct {
	w       io.Writer
	browser *githubBrowser
	line    []byte
	tooLong bool
}

func (w *githubPromptWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	for _, c := range p[:n] {
		if c == '\n' {
			if !w.tooLong && hibapi.IsGitHubDevicePrompt(string(w.line)) {
				w.browser.start()
			}
			w.line, w.tooLong = w.line[:0], false
		} else if !w.tooLong {
			if len(w.line) == maxGitHubPromptLine {
				w.line, w.tooLong = w.line[:0], true
			} else {
				w.line = append(w.line, c)
			}
		}
	}
	return n, err
}

type lineWriter struct {
	mu        sync.Mutex
	w         io.Writer
	lineStart bool
	pending   string
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	total := 0
	for len(p) > 0 {
		part := p
		if i := bytes.IndexByte(p, '\n'); i >= 0 {
			part = p[:i+1]
		}
		n, err := w.w.Write(part)
		total += n
		if n > 0 {
			w.lineStart = part[n-1] == '\n'
		}
		if err != nil {
			return total, err
		}
		if n != len(part) {
			return total, io.ErrShortWrite
		}
		if w.lineStart && w.pending != "" {
			io.WriteString(w.w, w.pending)
			w.pending = ""
		}
		p = p[n:]
	}
	return total, nil
}

func (w *lineWriter) note(note string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.lineStart {
		io.WriteString(w.w, note)
	} else {
		w.pending = note
	}
}

func (w *lineWriter) flushNote() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.pending != "" {
		if !w.lineStart {
			io.WriteString(w.w, "\n")
		}
		io.WriteString(w.w, w.pending)
		w.pending = ""
		w.lineStart = true
	}
}
