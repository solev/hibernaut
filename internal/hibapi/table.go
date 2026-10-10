package hibapi

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/solev/hibernaut/internal/termsafe"
)

// Style styles a state word, such as running or ok, for a terminal. It gets the word escaped,
// and returns it with styling such as colour around it. A nil Style leaves words plain.
type Style func(state string) string

// Table lays rows out in columns, as text/tabwriter does with two spaces between columns. It
// styles state words after padding them, so styling never moves a column.
type Table struct {
	w     io.Writer
	style Style
	buf   bytes.Buffer
	tw    *tabwriter.Writer
}

// stateMark brackets a state word in what Table writes to tabwriter, which counts only the word
// towards the width. Escaped text never holds the byte, since it isn't UTF-8.
const stateMark = "\xff"

// NewTable returns a Table that writes to w when flushed. style may be nil.
func NewTable(w io.Writer, style Style) *Table {
	t := &Table{w: w, style: style}
	t.tw = tabwriter.NewWriter(&t.buf, 0, 4, 2, ' ', 0)
	return t
}

// Write adds terminal-safe text to the table: tabs end cells and newlines end rows.
func (t *Table) Write(p []byte) (int, error) {
	return t.tw.Write(p)
}

// State returns word, which must be terminal-safe, marked as a cell's state for the style.
func (t *Table) State(word string) string {
	if t.style == nil {
		return word
	}
	return stateMark + word + stateMark
}

// Flush lays out the rows written so far, styles their states and writes them on.
func (t *Table) Flush() error {
	if err := t.tw.Flush(); err != nil {
		return err
	}
	out := t.buf.Bytes()
	if t.style != nil {
		out = t.styleStates(out)
	}
	_, err := t.w.Write(out)
	t.buf.Reset()
	return err
}

func (t *Table) styleStates(text []byte) []byte {
	var out []byte
	for {
		before, rest, found := bytes.Cut(text, []byte(stateMark))
		out = append(out, before...)
		if !found {
			return out
		}
		word, after, _ := bytes.Cut(rest, []byte(stateMark))
		out = append(out, t.style(string(word))...)
		text = after
	}
}

// PrintSavedImage writes a terminal-safe saved-image confirmation. It says how to use the image
// only when the name passes CheckName, so a command to copy never carries other text.
func PrintSavedImage(w io.Writer, img Image) {
	fmt.Fprintf(w, "saved image %s (%s).", termsafe.Line(img.Name), FormatMB(int64(img.MemoryMB)))
	if CheckName(img.Name) == nil {
		fmt.Fprintf(w, " use it with: hib new NAME --image %s", img.Name)
	}
	fmt.Fprintln(w)
}

// PrintImages writes the built-in and saved images as a terminal-safe table.
func PrintImages(w io.Writer, images []Image) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tKIND\tMEMORY\tCREATED")
	for _, img := range images {
		name := oneLine(img.Name)
		if img.Shadowed {
			name += " (shadowed)"
		}
		created := "-"
		if !img.Created.IsZero() {
			created = img.Created.UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", name, oneLine(img.Kind), FormatMB(int64(img.MemoryMB)), created)
	}
	tw.Flush()
}

// PrintFork writes the copies made and how to connect to the first one. It says how to connect
// only when that copy's name passes CheckName, so a command to copy never carries other text.
// No copies print nothing.
func PrintFork(w io.Writer, origin string, copies []Machine) {
	if len(copies) == 0 {
		return
	}
	names := make([]string, len(copies))
	for i, copy := range copies {
		names[i] = termsafe.Line(copy.Name)
	}
	fmt.Fprintf(w, "made %s from %s.", strings.Join(names, ", "), termsafe.Line(origin))
	if first := copies[0].Name; CheckName(first) == nil {
		fmt.Fprintf(w, " connect with: ssh %s.hib", first)
	}
	fmt.Fprintln(w)
}

// PrintSecrets writes a terminal-safe metadata table. Empty fields show as "-".
func PrintSecrets(w io.Writer, secrets []Secret) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tENV\tSITES\tPLACEHOLDER")
	for _, s := range secrets {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", field(s.Name), field(s.Env),
			field(strings.Join(s.Sites, ",")), field(s.Placeholder))
	}
	tw.Flush()
}

// PrintPorts writes hib port ls's table, terminal-safe. Empty fields show as "-". OPEN is how
// many links use a machine's ports now, or "-" when hibd proxy didn't count one of them.
func PrintPorts(w io.Writer, all []Ports) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tHOST\tIP\tPORTS\tOPEN")
	for _, p := range all {
		nums := make([]string, len(p.Published))
		total, counted := 0, true
		for i, pp := range p.Published {
			nums[i] = strconv.Itoa(pp.Port)
			if pp.Open == nil {
				counted = false
			} else {
				total += *pp.Open
			}
		}
		open := "-"
		if counted {
			open = strconv.Itoa(total)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", field(p.Machine), field(p.Machine+".hib"), field(p.Addr),
			strings.Join(nums, ","), open)
	}
	tw.Flush()
}

// PrintStatus writes the host's health for people, terminal-safe, one line per field. host is
// what the first line calls the host. "" leaves that line out. style styles the states: E2B's,
// the Codex login's when it isn't ok, and each secret's. It may be nil.
func PrintStatus(w io.Writer, host string, s Status, style Style) {
	tw := NewTable(w, style)
	if host != "" {
		fmt.Fprintf(tw, "host\t%s\n", oneLine(host))
	}
	fmt.Fprintf(tw, "hibd\t%s\n", oneLine(s.Version))
	switch {
	case s.E2B.OK:
		fmt.Fprintf(tw, "E2B\t%s\n", tw.State("ok"))
	case s.E2B.Detail == "":
		fmt.Fprintf(tw, "E2B\t%s\n", tw.State("down"))
	default:
		fmt.Fprintf(tw, "E2B\t%s: %s\n", tw.State("down"), oneLine(s.E2B.Detail))
	}
	if g := s.GitHub; g != nil {
		fmt.Fprintf(tw, "GitHub\t%s, commits as %s\n", oneLine(g.Who()), oneLine(g.Email))
		if warning := g.ScopeWarning(); warning != "" {
			fmt.Fprintf(tw, "\twarning: %s\n", warning)
		}
	} else {
		fmt.Fprintln(tw, "GitHub\tnot connected: run hib connect github --from-gh")
	}
	printCodex(tw, s.Codex)
	if len(s.Secrets) == 0 {
		fmt.Fprintln(tw, "secrets\tnone")
		tw.Flush()
		return
	}
	tw.Flush()
	fmt.Fprintln(w)
	fmt.Fprintln(tw, "SECRET\tSITES\tSTATE\tDETAIL")
	for _, sec := range s.Secrets {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", oneLine(sec.Name), oneLine(strings.Join(sec.Sites, ",")),
			tw.State(oneLine(string(sec.State))), oneLine(sec.Detail))
	}
	tw.Flush()
}

// PrintCodex writes the Codex login as PrintStatus does, terminal-safe: the account, the plan
// and the token's expiry, then what's wrong unless the login is ok. style may be nil.
func PrintCodex(w io.Writer, c Codex, style Style) {
	tw := NewTable(w, style)
	printCodex(tw, &c)
	tw.Flush()
}

func printCodex(w *Table, c *Codex) {
	if c == nil {
		fmt.Fprintln(w, "Codex\tnot connected: run hib connect codex")
		return
	}
	line := fmt.Sprintf("%s, %s plan", oneLine(c.Email), oneLine(c.Plan))
	if !c.Expires.IsZero() {
		line += ", token expires " + c.Expires.UTC().Format("2006-01-02 15:04 UTC")
	}
	fmt.Fprintf(w, "Codex\t%s\n", line)
	if c.Health == CodexOK {
		return
	}
	fix := "run hib connect codex"
	if c.Problem != "" && !strings.Contains(c.Problem, "hib connect codex") {
		fix = oneLine(c.Problem) + ". Run hib connect codex"
	} else if c.Problem != "" {
		fix = oneLine(c.Problem)
	}
	fmt.Fprintf(w, "\t%s: %s\n", w.State(oneLine(string(c.Health))), fix)
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(termsafe.String(s)), " ")
	if s == "" {
		return "-"
	}
	return s
}

// field is s escaped to stay in its cell, or "-" when it is empty.
func field(s string) string {
	if s == "" {
		return "-"
	}
	return termsafe.Line(s)
}
