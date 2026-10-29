// Package hibapi is what hib and hibd say to each other over SSH: the JSON hibd prints for
// --json, the header that starts a bridge, and what names and host keys look like.
package hibapi

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
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
	// Size is the machine's size, such as "small", or "" if it's none of the sizes.
	Size string `json:"size,omitempty"`
	// ForkOf is the name of the machine this one was forked from, if it was.
	ForkOf string `json:"forkOf,omitempty"`
	// State is "running" or "paused".
	State     string    `json:"state"`
	StartedAt time.Time `json:"startedAt"`
	// EndAt is when a running machine pauses, unless something keeps it awake.
	EndAt time.Time `json:"endAt"`
	// IdleSeconds is how long the machine runs once it's idle before it pauses; 0 means never.
	// It's nil for machines hibd didn't make.
	IdleSeconds *int64 `json:"idleSeconds,omitempty"`
	// Ports are the TCP ports this machine publishes to your other machines, ascending. It is
	// omitzero so that an allocated empty slice can mean "publishes nothing".
	Ports []int `json:"ports,omitzero"`
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

// FormatPorts formats the ports a machine publishes, as in Machine.Ports, for people: joined
// with commas, or "-" when it publishes none or hibd has links off.
func FormatPorts(ports []int) string {
	if len(ports) == 0 {
		return "-"
	}
	nums := make([]string, len(ports))
	for i, p := range ports {
		nums[i] = strconv.Itoa(p)
	}
	return strings.Join(nums, ",")
}

// ParsePorts reads a batch of port numbers, as hib port add and hib port rm take them, and drops
// repeats, keeping the order they came in.
func ParsePorts(args []string) ([]int, error) {
	var ports []int
	for _, arg := range args {
		n, err := strconv.ParseUint(arg, 10, 16)
		if err != nil || n == 0 {
			return nil, fmt.Errorf("%s isn't a port. Ports are 1 to 65535", arg)
		}
		if !slices.Contains(ports, int(n)) {
			ports = append(ports, int(n))
		}
	}
	return ports, nil
}

// Response is what a hibd command prints with -json: one object, then the command exits. A
// failed command carries Error, may carry partial results, and exits 1.
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
	// Image is the image image save saved.
	Image *Image `json:"image,omitempty"`
	// Images are the images image ls lists.
	Images []Image `json:"images,omitempty"`
	// Template is the ID of the template an image was built into, at the default size.
	Template string `json:"template,omitempty"`
	// Sizes are the sizes image build built, in the order it built them. Older hibds don't send
	// it.
	Sizes []string `json:"sizes,omitempty"`
	// Version is hibd's release.
	Version string `json:"version,omitempty"`
	// LeakScan is the result of a leak scan.
	LeakScan *LeakScan `json:"leakScan,omitempty"`
	// GitHub is the account connect github stored.
	GitHub *GitHub `json:"github,omitempty"`
	// Codex is the login connect codex stored, or the one codex refresh left.
	Codex *Codex `json:"codex,omitempty"`
	// Status is the host's health, from status.
	Status *Status `json:"status,omitempty"`
	// Event is a build step starting or ending. hibd image build -json -events sends a Response
	// with only V and Event for each, one per line, before the final reply.
	Event *Event `json:"event,omitempty"`
	// Features are what this hibd can do beyond what every hibd does, from version. Older hibds
	// don't send it, and other commands leave it out.
	Features []string `json:"features,omitzero"`
	// Host is the host VM's power state, which hib status --json adds from Azure's answer. hibd
	// never sends it.
	Host *Host `json:"host,omitempty"`
	// Ports are the machines port ls listed, or the one machine port add or port rm changed.
	// It uses omitzero rather than omitempty: an allocated empty slice means "nothing is
	// published anywhere", and omitempty cannot tell that from a reply that carries no ports
	// at all. So port ls with nothing published is {"v":1,"ports":[]}.
	Ports []Ports `json:"ports,omitzero"`
	// Links are link log entries, from log --links.
	Links []LinkEntry `json:"links,omitzero"`
	// TaskReply is what task commands reply. Its fields are inline.
	TaskReply
}

// Ports is what one machine publishes.
type Ports struct {
	// Machine is the machine's name.
	Machine string `json:"machine"`
	// Addr is the virtual address its name resolves to in other machines, empty before its
	// first publish.
	Addr string `json:"addr,omitempty"`
	// Published is the ports, sorted. It is an allocated empty slice, not null, when the
	// machine publishes nothing.
	Published []PublishedPort `json:"published,omitzero"`
}

// PublishedPort is one published port.
type PublishedPort struct {
	Port int `json:"port"`
	// Open is how many links are using it right now, so a machine kept awake by a link can be
	// explained while it is happening rather than afterwards from a log. It is null when hibd
	// proxy didn't answer, and in the replies to port add and port rm, which don't ask it.
	Open *int `json:"open"`
}

// LinkEntry is one line of the link log, from log --links.
type LinkEntry struct {
	Time        string `json:"time"`
	Event       string `json:"event"`
	From        string `json:"from"`
	To          string `json:"to"`
	Port        int    `json:"port"`
	Reason      string `json:"reason,omitempty"`
	Woke        bool   `json:"woke,omitempty"`
	Count       int    `json:"count,omitempty"`
	BytesIn     int64  `json:"bytesIn,omitempty"`
	BytesOut    int64  `json:"bytesOut,omitempty"`
	SecondsOpen int64  `json:"secondsOpen,omitempty"`
}

const (
	// FeatureBuildEvents means image build takes -events, and then sends an Event per step.
	FeatureBuildEvents = "build-events"
	// FeatureDisconnect means hibd has the disconnect command.
	FeatureDisconnect = "disconnect"
	// FeaturePorts means hibd has the port command, so machines can reach each other.
	FeaturePorts = "ports"
)

// Event is a build step starting or ending.
type Event struct {
	// Image is the image being built.
	Image string `json:"image"`
	// Size is the size being built, if the image is built in sizes.
	Size string `json:"size,omitempty"`
	// Step is E2B's name for the step, such as base, 3/12 or finalize.
	Step string `json:"step"`
	// Text is what the step does, such as RUN apt-get install -y git. It comes from E2B, so it
	// must go through termsafe.Line before it's shown.
	Text  string     `json:"text"`
	State EventState `json:"state"`
	// Seconds is how long the step took, rounded, once it has ended.
	Seconds int `json:"seconds,omitempty"`
}

// EventState is where a build step is.
type EventState string

const (
	// EventStarted means the step has started.
	EventStarted EventState = "started"
	// EventReused means the step ended by reusing E2B's cached result.
	EventReused EventState = "reused"
	// EventBuilt means the step ran and ended well.
	EventBuilt EventState = "built"
	// EventFailed means the step ran and failed, which fails the build.
	EventFailed EventState = "failed"
)

// LeakScan is a leak scan result. It never carries secret values.
type LeakScan struct {
	Target      LeakScanTarget `json:"target"`
	BuildID     string         `json:"buildId,omitempty"`
	SecretCount int            `json:"secretCount"`
	// Secrets are the names the scan looked for. Replaced values count under the name they had.
	Secrets     []string      `json:"secrets,omitempty"`
	Hits        []LeakScanHit `json:"hits"`
	MemoryFound bool          `json:"memoryFound,omitempty"`
	DiskFound   bool          `json:"diskFound,omitempty"`
}

// LeakScanTarget is what was scanned.
type LeakScanTarget struct {
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
	ID   string `json:"id,omitempty"`
}

// LeakScanHit is one leak scan hit. It never carries the secret value.
type LeakScanHit struct {
	Secret   string `json:"secret"`
	Encoding string `json:"encoding"`
	Where    string `json:"where"`
	Offset   int64  `json:"offset"`
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
	// Sizes says how many more machines of each size fit, smallest first. Each count is on its
	// own, not a mix. Older hibds don't send it.
	Sizes []SizeRoom `json:"sizes,omitempty"`
}

// SizeRoom is how many more machines of one size fit next to the running machines.
type SizeRoom struct {
	Size string `json:"size"`
	Fits int    `json:"fits"`
}

const (
	// KindBuiltIn is an image in hibd's catalog, which hib image build builds.
	KindBuiltIn = "built-in"
	// KindSaved is an image saved from a machine.
	KindSaved = "saved"
)

// Image is an image machines can be made from, as hibd reports it.
type Image struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	MemoryMB int    `json:"memoryMB"`
	CPUs     int    `json:"cpus"`
	// Created is when the image was last built or saved. It's zero for a built-in image that
	// isn't built.
	Created time.Time `json:"created,omitzero"`
	// Built says whether a built-in image is built. Saved images always are.
	Built bool `json:"built"`
	// Shadowed says a saved image has a built-in image's name. hib new --image NAME then makes
	// the built-in one, and hib image rm NAME removes the saved one.
	Shadowed bool `json:"shadowed,omitempty"`
}

// ErrVersion is returned for a response or header of another protocol version.
var ErrVersion = errors.New("hib and hibd speak different versions")

// RemoteError is an error hibd reported.
type RemoteError struct{ Msg string }

func (e *RemoteError) Error() string { return e.Msg }

// DecodeResponse parses hibd's output. It returns the response with a *RemoteError when hibd
// reports an error, keeping partial results. Another version returns an error wrapping ErrVersion.
func DecodeResponse(data []byte) (Response, error) {
	var r Response
	if err := json.Unmarshal(data, &r); err != nil {
		return Response{}, fmt.Errorf("unreadable reply from hibd: %w (%q)", err, clip(data))
	}
	if r.V != Version {
		return Response{}, versionError(r.V)
	}
	if r.Error != "" {
		return r, &RemoteError{r.Error}
	}
	return r, nil
}

func versionError(v int) error {
	return fmt.Errorf("%w: hibd speaks %d, hib %d; install the same release of both", ErrVersion, v, Version)
}

func clip(data []byte) []byte {
	if len(data) > 200 {
		return data[:200]
	}
	return data
}

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
