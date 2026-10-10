// Package audit writes and reads the host audit log for secret swaps.
package audit

import (
	"errors"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	maxPathBytes      = 1024
	maxSiteBytes      = 253
	maxMethodBytes    = 32
	maxMachineBytes   = 64
	maxSecretBytes    = 64
	maxSecrets        = 32
	maxAuditLineBytes = 64 << 10
)

// Entry is one request where the proxy swapped placeholders for real secrets.
type Entry struct {
	Time      time.Time `json:"time"`
	Machine   string    `json:"machine,omitempty"`
	MachineID string    `json:"machineId,omitempty"`
	Site      string    `json:"site"`
	Method    string    `json:"method"`
	Path      string    `json:"path"`
	Secrets   []string  `json:"secrets"`
}

// ReadResult is the entries read from the audit log and the corrupt lines skipped.
type ReadResult struct {
	Entries []Entry
	Corrupt int
}

// Log is an open audit log.
type Log struct {
	mu  sync.Mutex
	out jsonlFile[Entry]
}

// Open opens path for audit log writes and creates it with mode 0644 if needed.
func Open(path string, maxBytes int64) (*Log, error) {
	out, err := openJSONL[Entry](path, maxBytes, "audit log")
	if err != nil {
		return nil, err
	}
	return &Log{out: out}, nil
}

// Write appends one JSON line for e.
func (l *Log) Write(e Entry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.out.closed() {
		return errors.New("audit log is closed")
	}
	return l.out.write(cleanEntry(e))
}

// Close closes the log.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.out.closed() {
		return nil
	}
	return l.out.close()
}

// Read returns the last n audit entries, oldest first.
func Read(path string, n int) (ReadResult, error) {
	entries, corrupt, err := readJSONL[Entry](path, n)
	if err != nil {
		return ReadResult{}, err
	}
	return ReadResult{Entries: entries, Corrupt: corrupt}, nil
}

func cleanEntry(e Entry) Entry {
	e.Machine = cleanString(e.Machine, maxMachineBytes)
	e.MachineID = cleanString(e.MachineID, maxMachineBytes)
	e.Site = cleanString(e.Site, maxSiteBytes)
	e.Method = cleanString(e.Method, maxMethodBytes)
	e.Path = cleanString(stripQuery(e.Path), maxPathBytes)
	limit := min(len(e.Secrets), maxSecrets)
	secrets := make([]string, limit)
	for i := range limit {
		secrets[i] = cleanString(e.Secrets[i], maxSecretBytes)
	}
	e.Secrets = secrets
	return e
}

func cleanString(s string, maxBytes int) string {
	var b strings.Builder
	b.Grow(min(len(s), maxBytes))
	for len(s) > 0 && b.Len() < maxBytes {
		r, size := utf8.DecodeRuneInString(s)
		if r == utf8.RuneError && size == 1 || r < 0x20 || r == 0x7f {
			b.WriteByte('?')
			s = s[size:]
			continue
		}
		if b.Len()+size > maxBytes {
			break
		}
		b.WriteString(s[:size])
		s = s[size:]
	}
	return b.String()
}

func stripQuery(path string) string {
	if before, _, ok := strings.Cut(path, "?"); ok {
		return before
	}
	return path
}
