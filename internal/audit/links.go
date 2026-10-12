package audit

import (
	"errors"
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
	// Event is LinkOpen, LinkClose or LinkRefused. There are two lines per link, not one: a
	// line written only at close cannot answer why a machine is awake and costing money right
	// now, which is exactly the question an hour-old link raises.
	Event LinkEvent `json:"event"`
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

// LinkEvent is what a link log line records.
type LinkEvent string

const (
	// LinkOpen means a link is about to carry its first byte.
	LinkOpen LinkEvent = "open"
	// LinkClose means an open link has ended.
	LinkClose LinkEvent = "close"
	// LinkRefused means a connection has ended before carrying a byte.
	LinkRefused LinkEvent = "refused"
)

// LinkReadResult is the entries read from a link log and the corrupt lines skipped.
type LinkReadResult struct {
	Entries []LinkEntry
	Corrupt int
}

// LinkLog is an open link log. A nil *LinkLog drops entries instead of failing, so a listener
// whose log could not be opened still carries traffic.
type LinkLog struct {
	mu    sync.Mutex
	out   jsonlFile[LinkEntry]
	folds map[foldKey]*fold
	// perSource is how many of folds each source holds, by FromID. Overflow folds hold none.
	perSource map[string]int
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
	// held is whether the fold is one of its source's MaxFoldsPerSource. An overflow fold, which
	// stands for the refusals beyond them, is not.
	held bool
}

// OpenLinks opens path for link log writes and creates it with mode 0644 if needed.
func OpenLinks(path string, maxBytes int64) (*LinkLog, error) {
	out, err := openJSONL[LinkEntry](path, maxBytes, "link log")
	if err != nil {
		return nil, err
	}
	return &LinkLog{out: out, folds: map[foldKey]*fold{}, perSource: map[string]int{}}, nil
}

// Write appends one JSON line for e and flushes it before returning, as the secret log does.
//
// A refusal identical to one already written in the last FoldWindow, by source, target, port and
// reason, is not written. It is counted, and the window's one line is written with the count
// when the window ends or the log is closed. Opens and closes are never folded.
//
// A source, by FromID, has at most MaxFoldsPerSource windows open at once. A refusal that would
// open another folds instead into the source's overflow line for its reason, which has no target
// and port 0 and counts every refusal folded into it, so a machine trying every port can't flood
// the log.
func (l *LinkLog) Write(e LinkEntry) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.out.closed() {
		return errors.New("link log is closed")
	}
	e = cleanLinkEntry(e)
	if e.Event != LinkRefused {
		return l.out.write(e)
	}
	key := foldKey{from: e.FromID, to: e.ToID, port: e.Port, reason: e.Reason}
	f, ok := l.folds[key]
	full := !ok && l.perSource[e.FromID] >= links.MaxFoldsPerSource
	if full {
		e.To, e.ToID, e.Port = "?", "", 0
		key = foldKey{from: e.FromID, reason: e.Reason}
		f, ok = l.folds[key]
	}
	if ok {
		f.count++
		return nil
	}
	f = &fold{entry: e, count: 1, held: !full}
	l.folds[key] = f
	if f.held {
		l.perSource[e.FromID]++
	}
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
	if l.out.closed() {
		return nil
	}
	var errs []error
	for key, f := range l.folds {
		f.timer.Stop()
		l.forget(key, f)
		errs = append(errs, l.out.write(f.line()))
	}
	errs = append(errs, l.out.close())
	return errors.Join(errs...)
}

// ReadLinks reads the last n entries of the link log at path.
func ReadLinks(path string, n int) (LinkReadResult, error) {
	entries, corrupt, err := readJSONL[LinkEntry](path, n)
	if err != nil {
		return LinkReadResult{}, err
	}
	return LinkReadResult{Entries: entries, Corrupt: corrupt}, nil
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
	if l.out.closed() || l.folds[key] != f {
		return
	}
	l.forget(key, f)
	// A timer has nobody to return an error to. The next Write fails the same way.
	_ = l.out.write(f.line())
}

// forget drops f, pending under key, and gives its source back the place it held.
func (l *LinkLog) forget(key foldKey, f *fold) {
	delete(l.folds, key)
	if !f.held {
		return
	}
	if l.perSource[key.from]--; l.perSource[key.from] == 0 {
		delete(l.perSource, key.from)
	}
}

func cleanLinkEntry(e LinkEntry) LinkEntry {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	e.Time = e.Time.UTC()
	e.Event = LinkEvent(cleanString(string(e.Event), maxEventBytes))
	e.From = cleanString(e.From, maxMachineBytes)
	e.FromID = cleanString(e.FromID, maxMachineBytes)
	e.To = cleanString(e.To, maxMachineBytes)
	e.ToID = cleanString(e.ToID, maxMachineBytes)
	e.Reason = cleanString(e.Reason, maxReasonBytes)
	e.Count = 0
	return e
}
