package audit

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/solev/hibernaut/internal/links"
)

const (
	maxEventBytes  = 16
	maxReasonBytes = 256
)

// LinkEntry is one event on one link between two machines.
//
// Link events are kept in their own log, not audit.jsonl. Entry's fields are request-shaped —
// Site, Method, Path and Secrets have no omitempty — and hib log --json is a stable shape, so a
// link record written there would print four empty columns in every hib log.
type LinkEntry struct {
	// Time is when the event happened, UTC.
	Time time.Time `json:"time"`
	// Event is "open", "close" or "refused". There are two lines per link, not one: a line
	// written only at close cannot answer why a machine is awake and costing money right now,
	// which is exactly the question an hour-old link raises.
	Event string `json:"event"`
	// From and To are machine names, or sandbox IDs when a name is unknown. An endpoint that
	// cannot be resolved at all is "?" with its ID still set.
	From   string `json:"from"`
	FromID string `json:"fromId,omitempty"`
	To     string `json:"to"`
	ToID   string `json:"toId,omitempty"`
	// Port is the published port on the target.
	Port int `json:"port"`
	// Reason says why a refusal was refused, or why a link was aborted rather than closing on
	// its own. It is the Error text of one of internal/links's errors.
	Reason string `json:"reason,omitempty"`
	// Woke is true on the open entry that resumed the target.
	Woke bool `json:"woke,omitempty"`
	// Count is how many identical refusals this line stands for, when they were folded.
	Count int `json:"count,omitempty"`
	// BytesIn is source to target, BytesOut is target to source, and both are written on
	// close.
	BytesIn     int64 `json:"bytesIn,omitempty"`
	BytesOut    int64 `json:"bytesOut,omitempty"`
	SecondsOpen int64 `json:"secondsOpen,omitempty"`
}

// LinkReadResult is the entries read from a link log and the corrupt lines skipped.
type LinkReadResult struct {
	Entries []LinkEntry
	Corrupt int
}

// LinkLog is an open link log. A nil *LinkLog drops entries instead of failing, so a listener
// whose log could not be opened still carries traffic.
type LinkLog struct {
	path     string
	maxBytes int64

	mu    sync.Mutex
	file  *os.File
	size  int64
	folds map[foldKey]*fold
}

// foldKey is what makes two refusals identical.
type foldKey struct {
	from, to string
	port     int
	reason   string
}

// fold is a refusal waiting for its window to end, with how many it stands for.
type fold struct {
	entry LinkEntry
	count int
	timer *time.Timer
}

// OpenLinks opens path for link log writes and creates it with mode 0644 if needed.
func OpenLinks(path string, maxBytes int64) (*LinkLog, error) {
	file, size, err := openFile(path)
	if err != nil {
		return nil, err
	}
	return &LinkLog{path: path, maxBytes: maxBytes, file: file, size: size, folds: map[foldKey]*fold{}}, nil
}

// Path is where the log is being written.
func (l *LinkLog) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// MaxBytes is the size the log rotates at, keeping one older file.
func (l *LinkLog) MaxBytes() int64 {
	if l == nil {
		return 0
	}
	return l.maxBytes
}

// Write appends one JSON line for e and flushes it before returning, as the secret log does.
//
// A refusal identical to one already written in the last FoldWindow, by source, target, port and
// reason, is not written. It is counted, and the window's one line is written with the count
// when the window ends or the log is closed. Opens and closes are never folded.
func (l *LinkLog) Write(e LinkEntry) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return errors.New("link log is closed")
	}
	e = cleanLinkEntry(e)
	if e.Event != "refused" {
		return l.writeLocked(e)
	}
	key := foldKey{from: e.FromID, to: e.ToID, port: e.Port, reason: e.Reason}
	if f, ok := l.folds[key]; ok {
		f.count++
		return nil
	}
	f := &fold{entry: e, count: 1}
	l.folds[key] = f
	f.timer = time.AfterFunc(links.FoldWindow, func() { l.flush(key, f) })
	return nil
}

// Close flushes every pending folded refusal and closes the file, so a restart loses nothing.
func (l *LinkLog) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	var errs []error
	for key, f := range l.folds {
		f.timer.Stop()
		delete(l.folds, key)
		errs = append(errs, l.writeLocked(f.line()))
	}
	errs = append(errs, l.file.Close())
	l.file = nil
	return errors.Join(errs...)
}

// ReadLinks reads the last n entries of the link log at path.
func ReadLinks(path string, n int) (LinkReadResult, error) {
	var out LinkReadResult
	for _, p := range []string{path + ".1", path} {
		entries, corrupt, err := readLinkFile(p)
		if err != nil {
			return LinkReadResult{}, err
		}
		out.Entries = append(out.Entries, entries...)
		out.Corrupt += corrupt
	}
	if n > 0 && len(out.Entries) > n {
		out.Entries = out.Entries[len(out.Entries)-n:]
	}
	return out, nil
}

func (f *fold) line() LinkEntry {
	e := f.entry
	if f.count > 1 {
		e.Count = f.count
	}
	return e
}

// flush writes f once its window has ended, unless Close already has.
func (l *LinkLog) flush(key foldKey, f *fold) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil || l.folds[key] != f {
		return
	}
	delete(l.folds, key)
	// A timer has nobody to return an error to. The next Write fails the same way.
	_ = l.writeLocked(f.line())
}

func (l *LinkLog) writeLocked(e LinkEntry) error {
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
		return fmt.Errorf("short link log write: wrote %d of %d bytes", n, len(line))
	}
	return nil
}

func (l *LinkLog) rotate() error {
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

func cleanLinkEntry(e LinkEntry) LinkEntry {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	e.Time = e.Time.UTC()
	e.Event = cleanString(e.Event, maxEventBytes)
	e.From = cleanString(e.From, maxMachineBytes)
	e.FromID = cleanString(e.FromID, maxMachineBytes)
	e.To = cleanString(e.To, maxMachineBytes)
	e.ToID = cleanString(e.ToID, maxMachineBytes)
	e.Reason = cleanString(e.Reason, maxReasonBytes)
	e.Count = 0
	return e
}

func readLinkFile(path string) ([]LinkEntry, int, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer file.Close()
	var entries []LinkEntry
	corrupt := 0
	reader := bufio.NewReader(file)
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			var e LinkEntry
			if len(line) > maxAuditLineBytes {
				corrupt++
			} else if err := json.Unmarshal(line, &e); err != nil {
				corrupt++
			} else {
				entries = append(entries, e)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, 0, readErr
		}
	}
	return entries, corrupt, nil
}
