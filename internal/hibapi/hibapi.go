// Package hibapi is what hib and hibd say to each other over SSH: the JSON hibd prints for
// --json, the header that starts a bridge, and what names and host keys look like. Both sides
// check everything they're given.
package hibapi

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Version is the protocol's version. hib and hibd refuse to talk to another version.
const Version = 1

// MaxHeader bounds the bridge header line.
const MaxHeader = 64 << 10

var nameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// CheckName returns an error unless name can name a machine or an image: 1 to 63 lowercase
// letters, digits and hyphens, starting and ending with a letter or digit. Such a name is safe
// in a shell word, a file name and a DNS label.
func CheckName(name string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("bad name %q: use 1-63 of a-z, 0-9 and -, starting and ending with a letter or digit", name)
	}
	return nil
}

// Machine is a machine as hibd reports it. Name is empty for machines hibd didn't create.
type Machine struct {
	Name  string `json:"name,omitempty"`
	ID    string `json:"id"`
	Image string `json:"image,omitempty"`
	// State is "running" or "paused".
	State     string    `json:"state"`
	StartedAt time.Time `json:"startedAt"`
	// EndAt is when a running machine pauses, unless something keeps it awake.
	EndAt time.Time `json:"endAt"`
	// IdleSeconds is how long the machine runs once it's idle before it pauses; 0 means never.
	// It's nil for machines hibd didn't make.
	IdleSeconds *int64 `json:"idleSeconds,omitempty"`
}

// FormatIdle formats an idle time in seconds, as in Machine.IdleSeconds, for people: "15m",
// "1h30m", "never" for 0, or "-" if it's unknown.
func FormatIdle(seconds *int64) string {
	switch {
	case seconds == nil || *seconds < 0:
		return "-"
	case *seconds == 0:
		return "never"
	}
	h, m, s := *seconds/3600, *seconds%3600/60, *seconds%60
	out := ""
	if h > 0 {
		out += fmt.Sprintf("%dh", h)
	}
	if m > 0 || h > 0 && s > 0 {
		out += fmt.Sprintf("%dm", m)
	}
	if s > 0 {
		out += fmt.Sprintf("%ds", s)
	}
	return out
}

// FormatMB formats mb MiB for people, rounding down: "106 MiB", "1.9 GiB", "79 GiB".
func FormatMB(mb int64) string {
	mb = max(mb, 0)
	switch {
	case mb < 1024:
		return fmt.Sprintf("%d MiB", mb)
	case mb < 10*1024:
		if tenths := mb * 10 / 1024; tenths%10 != 0 {
			return fmt.Sprintf("%d.%d GiB", tenths/10, tenths%10)
		}
	}
	return fmt.Sprintf("%d GiB", mb/1024)
}

// Response is what a hibd command prints with --json: one object, then the command exits. A
// failed command prints only Error, and exits 1.
type Response struct {
	V        int        `json:"v"`
	Error    string     `json:"error,omitempty"`
	Machine  *Machine   `json:"machine,omitempty"`
	Machines []Machine  `json:"machines,omitempty"`
	Secret   *Secret    `json:"secret,omitempty"`
	Secrets  []Secret   `json:"secrets,omitempty"`
	Logs     []LogEntry `json:"logs,omitempty"`
	Corrupt  int        `json:"corrupt,omitempty"`
	// Room is the room on the host, from ls. It's nil if hibd couldn't read it.
	Room *Room `json:"room,omitempty"`
	// Template is the ID of the template an image was built into.
	Template string `json:"template,omitempty"`
	// Version is hibd's release.
	Version string `json:"version,omitempty"`
}

// Secret is bound secret metadata. It never carries the real value.
type Secret struct {
	Name        string   `json:"name"`
	Env         string   `json:"env"`
	Sites       []string `json:"sites"`
	Placeholder string   `json:"placeholder"`
}

// LogEntry is one audit log entry where placeholders were swapped for real secrets.
type LogEntry struct {
	Time    time.Time `json:"time"`
	Machine string    `json:"machine,omitempty"`
	Site    string    `json:"site"`
	Method  string    `json:"method"`
	Path    string    `json:"path"`
	Secrets []string  `json:"secrets"`
}

// Room is the room on the host for more running machines.
type Room struct {
	// Fits is how many more machines of Image fit next to the running machines.
	Fits  int    `json:"fits"`
	Image string `json:"image"`
	// MemoryFreeMB is the RAM free for more running machines, of the MemoryMB they all share.
	MemoryFreeMB int64 `json:"memoryFreeMB"`
	MemoryMB     int64 `json:"memoryMB"`
	// DiskFreeMB is the free disk for the snapshots of paused machines.
	DiskFreeMB int64 `json:"diskFreeMB"`
}

// ErrVersion is returned for a response or header of another protocol version.
var ErrVersion = errors.New("hib and hibd speak different versions")

// RemoteError is an error hibd reported.
type RemoteError struct{ Msg string }

func (e *RemoteError) Error() string { return e.Msg }

// DecodeResponse parses hibd's output. It returns a *RemoteError for a response that reports
// one, and an error wrapping ErrVersion for a response of another version.
func DecodeResponse(data []byte) (Response, error) {
	var r Response
	if err := json.Unmarshal(data, &r); err != nil {
		return Response{}, fmt.Errorf("unreadable reply from hibd: %w (%q)", err, clip(data))
	}
	if r.V != Version {
		return Response{}, versionError(r.V)
	}
	if r.Error != "" {
		return Response{}, &RemoteError{r.Error}
	}
	return r, nil
}

func versionError(v int) error {
	return fmt.Errorf("%w: hibd speaks %d, hib %d; install the same release of both", ErrVersion, v, Version)
}

// clip shortens data for an error message.
func clip(data []byte) []byte {
	if len(data) > 200 {
		return data[:200]
	}
	return data
}

// bridgeHeader is the line hibd bridge writes before the machine's SSH bytes.
type bridgeHeader struct {
	V int `json:"v"`
	// HostKey is the machine's SSH host key, in authorized_keys format without a comment.
	HostKey string `json:"hostKey"`
}

// WriteBridgeHeader starts a bridge by telling hib the machine's host key.
func WriteBridgeHeader(w io.Writer, hostKey ssh.PublicKey) error {
	line, err := json.Marshal(bridgeHeader{V: Version, HostKey: FormatHostKey(hostKey)})
	if err != nil {
		return err
	}
	_, err = w.Write(append(line, '\n'))
	return err
}

// ReadBridgeHeader reads what WriteBridgeHeader wrote. r then continues with the machine's
// bytes. Make r at least MaxHeader bytes big, so a header can't be cut off.
func ReadBridgeHeader(r *bufio.Reader) (ssh.PublicKey, error) {
	line, err := r.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		return nil, errors.New("bridge header too long")
	}
	if err != nil {
		return nil, fmt.Errorf("reading the bridge header: %w", err)
	}
	var h bridgeHeader
	if err := json.Unmarshal(line, &h); err != nil {
		return nil, fmt.Errorf("bad bridge header: %w (%q)", err, clip(line))
	}
	if h.V != Version {
		return nil, versionError(h.V)
	}
	return ParseHostKey(h.HostKey)
}

// ParseHostKey parses an SSH host key in the form FormatHostKey prints: "ssh-ed25519 AAAA…",
// and nothing else. Machines only have Ed25519 host keys.
func ParseHostKey(s string) (ssh.PublicKey, error) {
	fields := strings.Split(s, " ")
	if len(fields) != 2 || fields[0] != ssh.KeyAlgoED25519 {
		return nil, fmt.Errorf("bad host key %q: want %s and base64", clip([]byte(s)), ssh.KeyAlgoED25519)
	}
	blob, err := base64.StdEncoding.Strict().DecodeString(fields[1])
	if err != nil {
		return nil, fmt.Errorf("bad host key: %w", err)
	}
	key, err := ssh.ParsePublicKey(blob)
	if err != nil {
		return nil, fmt.Errorf("bad host key: %w", err)
	}
	if key.Type() != fields[0] {
		return nil, fmt.Errorf("bad host key: says %s, is %s", fields[0], key.Type())
	}
	// The decoder skips newlines, which mustn't get into a known_hosts file.
	if FormatHostKey(key) != s {
		return nil, fmt.Errorf("bad host key %q: not as FormatHostKey prints it", clip([]byte(s)))
	}
	return key, nil
}

// FormatHostKey formats key as a known_hosts line does after the host name.
func FormatHostKey(key ssh.PublicKey) string {
	return key.Type() + " " + base64.StdEncoding.EncodeToString(key.Marshal())
}
