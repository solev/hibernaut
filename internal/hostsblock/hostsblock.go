// Package hostsblock renders and merges hibd's managed block in /etc/hosts.
//
// The block maps each machine with a published port to its virtual address:
//
//	# BEGIN hibernaut machines: hibd manages this block and replaces edits
//	198.18.0.1 api.hib
//	# END hibernaut machines
//
// It shares only the idea with internal/envblock: /etc/hosts syntax and its markers differ.
package hostsblock

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/links"
)

const (
	beginLine = "# BEGIN hibernaut machines: hibd manages this block and replaces edits"
	endLine   = "# END hibernaut machines"
)

// Host is one line in the block.
type Host struct {
	IP   string // a dotted quad from 198.18.0.0/15
	Name string // "api.hib"
}

// The errors Render and Merge return, wrapped with the offending value.
var (
	// ErrReservedName is a name /etc/hosts already gives the machine itself, such as localhost
	// or E2B's hostname, e2b.local.
	ErrReservedName = errors.New("reserved host name")
	// ErrBadName is a name that isn't a machine name followed by .hib.
	ErrBadName = errors.New("host name is not NAME.hib")
	// ErrBadIP is an address that isn't a dotted quad hibd hands out from links.CIDR.
	ErrBadIP = errors.New("address outside the machine range")
	// ErrDuplicate is a name or an address that appears twice.
	ErrDuplicate = errors.New("duplicate host")
	// ErrUnterminated is a file whose markers don't pair up.
	ErrUnterminated = errors.New("unterminated hibernaut machines block")
)

var (
	addrRange = netip.MustParsePrefix(links.CIDR)
	reserved  = map[string]bool{
		"localhost":             true,
		"localhost.localdomain": true,
		"ip6-localhost":         true,
		"ip6-loopback":          true,
		"ip6-localnet":          true,
		"ip6-mcastprefix":       true,
		"ip6-allnodes":          true,
		"ip6-allrouters":        true,
		"broadcasthost":         true,
		"e2b.local":             true,
		"127.0.0.1":             true,
		"::1":                   true,
	}
)

// Render returns the block, sorted by name, or nil when there are no hosts.
func Render(hosts []Host) ([]byte, error) {
	if len(hosts) == 0 {
		return nil, nil
	}
	if err := check(hosts); err != nil {
		return nil, err
	}
	sorted := slices.Clone(hosts)
	slices.SortFunc(sorted, func(a, b Host) int { return strings.Compare(a.Name, b.Name) })
	var out bytes.Buffer
	out.WriteString(beginLine + "\n")
	for _, h := range sorted {
		out.WriteString(h.IP + " " + h.Name + "\n")
	}
	out.WriteString(endLine + "\n")
	return out.Bytes(), nil
}

// Merge returns file with block put in place of any existing block, appended when there is
// none, and removed when block is nil or empty. Lines outside the markers are untouched. hibd
// replaces edits, so a file with more than one block keeps the first one's place and loses the
// rest.
//
// block must be one Render returned: Merge parses it back and checks it with the same rules, so
// a hand-made block can't carry a reserved name either.
func Merge(file, block []byte) ([]byte, error) {
	if len(block) != 0 {
		if err := checkBlock(block); err != nil {
			return nil, err
		}
	}
	spans, err := find(file)
	if err != nil {
		return nil, err
	}
	if len(spans) == 0 {
		out := slices.Clone(file)
		if len(block) == 0 {
			return out, nil
		}
		if len(out) > 0 && out[len(out)-1] != '\n' {
			out = append(out, '\n')
		}
		return append(out, block...), nil
	}
	out := make([]byte, 0, len(file)+len(block))
	from := 0
	for i, s := range spans {
		out = append(out, file[from:s.start]...)
		if i == 0 {
			out = append(out, block...)
		}
		from = s.end
	}
	return append(out, file[from:]...), nil
}

func check(hosts []Host) error {
	names, ips := map[string]bool{}, map[string]bool{}
	for _, h := range hosts {
		if reserved[strings.ToLower(h.Name)] {
			return fmt.Errorf("%w: %q", ErrReservedName, h.Name)
		}
		if base, ok := strings.CutSuffix(h.Name, ".hib"); !ok || hibapi.CheckName(base) != nil {
			return fmt.Errorf("%w: %q", ErrBadName, h.Name)
		}
		ip, err := netip.ParseAddr(h.IP)
		if err != nil || !ip.Is4() || ip.String() != h.IP || !addrRange.Contains(ip) || ip.Less(links.First) {
			return fmt.Errorf("%w: %q for %s", ErrBadIP, h.IP, h.Name)
		}
		if names[h.Name] {
			return fmt.Errorf("%w: %s", ErrDuplicate, h.Name)
		}
		if ips[h.IP] {
			return fmt.Errorf("%w: %s", ErrDuplicate, h.IP)
		}
		names[h.Name], ips[h.IP] = true, true
	}
	return nil
}

var errNotRendered = errors.New("the hosts block isn't one hostsblock.Render returned")

func checkBlock(block []byte) error {
	body, ok := bytes.CutPrefix(block, []byte(beginLine+"\n"))
	if ok {
		body, ok = bytes.CutSuffix(body, []byte(endLine+"\n"))
	}
	if !ok {
		return errNotRendered
	}
	var hosts []Host
	for l := range eachLine(body) {
		fields := strings.Fields(string(l.text))
		if len(fields) != 2 {
			return fmt.Errorf("bad line in the hosts block: %q", l.text)
		}
		hosts = append(hosts, Host{IP: fields[0], Name: fields[1]})
	}
	rendered, err := Render(hosts)
	if err != nil {
		return err
	}
	if !bytes.Equal(rendered, block) {
		return errNotRendered
	}
	return nil
}

// span is one block in a file, from the start of its begin marker to the end of its end
// marker's line.
type span struct {
	start, end int
}

func find(file []byte) ([]span, error) {
	var spans []span
	open := -1
	for l := range eachLine(file) {
		switch strings.TrimRight(string(l.text), " \t\r") {
		case beginLine:
			if open >= 0 {
				return nil, fmt.Errorf("%w: a second begin marker before the end marker", ErrUnterminated)
			}
			open = l.start
		case endLine:
			if open < 0 {
				return nil, fmt.Errorf("%w: an end marker without a begin marker", ErrUnterminated)
			}
			spans = append(spans, span{start: open, end: l.end})
			open = -1
		}
	}
	if open >= 0 {
		return nil, fmt.Errorf("%w: a begin marker without an end marker", ErrUnterminated)
	}
	return spans, nil
}

type line struct {
	text       []byte
	start, end int
}

func eachLine(b []byte) func(func(line) bool) {
	return func(yield func(line) bool) {
		for start := 0; start < len(b); {
			end := bytes.IndexByte(b[start:], '\n')
			if end < 0 {
				yield(line{text: b[start:], start: start, end: len(b)})
				return
			}
			end += start
			if !yield(line{text: b[start:end], start: start, end: end + 1}) {
				return
			}
			start = end + 1
		}
	}
}
