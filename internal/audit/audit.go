// Package audit writes and reads the host audit log for secret swaps.
package audit

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
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
	mu       sync.Mutex
	path     string
	maxBytes int64
	file     *os.File
	size     int64
}

// Open opens path for audit log writes and creates it with mode 0644 if needed.
func Open(path string, maxBytes int64) (*Log, error) {
	file, size, err := openFile(path)
	if err != nil {
		return nil, err
	}
	return &Log{path: path, maxBytes: maxBytes, file: file, size: size}, nil
}

// Write appends one JSON line for e.
func (l *Log) Write(e Entry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return errors.New("audit log is closed")
	}
	e.Path = stripQuery(e.Path)
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	if l.maxBytes > 0 && l.size > 0 && l.size+int64(len(line)) > l.maxBytes {
		if err := l.rotate(); err != nil {
			return err
		}
	}
	n, err := l.file.Write(line)
	l.size += int64(n)
	if err != nil {
		return err
	}
	if n != len(line) {
		return fmt.Errorf("short audit log write: wrote %d of %d bytes", n, len(line))
	}
	return nil
}

// Close closes the log.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// Read returns the last n audit entries, oldest first.
func Read(path string, n int) (ReadResult, error) {
	var out ReadResult
	for _, p := range []string{path + ".1", path} {
		entries, corrupt, err := readFile(p)
		if err != nil {
			return ReadResult{}, err
		}
		out.Entries = append(out.Entries, entries...)
		out.Corrupt += corrupt
	}
	if n > 0 && len(out.Entries) > n {
		out.Entries = out.Entries[len(out.Entries)-n:]
	}
	return out, nil
}

func (l *Log) rotate() error {
	if err := l.file.Close(); err != nil {
		return err
	}
	l.file = nil
	if err := os.Rename(l.path, l.path+".1"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, size, err := openFile(l.path)
	if err != nil {
		return err
	}
	l.file, l.size = file, size
	return nil
}

func openFile(path string) (*os.File, int64, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, 0, err
	}
	if err := os.Chmod(path, 0o644); err != nil {
		file.Close()
		return nil, 0, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, 0, err
	}
	return file, info.Size(), nil
}

func readFile(path string) ([]Entry, int, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer file.Close()
	var entries []Entry
	corrupt := 0
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var e Entry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			corrupt++
			continue
		}
		entries = append(entries, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, err
	}
	return entries, corrupt, nil
}

func stripQuery(path string) string {
	if before, _, ok := strings.Cut(path, "?"); ok {
		return before
	}
	return path
}
