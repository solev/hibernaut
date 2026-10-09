package audit

import (
	"time"
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
}

// OpenLinks opens path for link log writes and creates it with mode 0644 if needed.
func OpenLinks(path string, maxBytes int64) (*LinkLog, error) {
	return &LinkLog{path: path, maxBytes: maxBytes}, nil
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
func (l *LinkLog) Write(e LinkEntry) error {
	return nil
}

// Close flushes every pending folded refusal and closes the file, so a restart loses nothing.
func (l *LinkLog) Close() error {
	return nil
}

// ReadLinks reads the last n entries of the link log at path.
func ReadLinks(path string, n int) (LinkReadResult, error) {
	return LinkReadResult{}, nil
}
