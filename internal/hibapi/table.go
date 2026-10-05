package hibapi

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/solev/hibernaut/internal/termsafe"
)

// PrintSavedImage writes a terminal-safe saved-image confirmation and how to use it.
func PrintSavedImage(w io.Writer, img Image) {
	name := termsafe.String(img.Name)
	fmt.Fprintf(w, "saved image %s (%s). use it with: hib new NAME -image %s\n",
		name, FormatMB(int64(img.MemoryMB)), name)
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

// PrintFork writes the copies made and how to connect to the first one. No copies print nothing.
func PrintFork(w io.Writer, origin string, copies []Machine) {
	if len(copies) == 0 {
		return
	}
	names := make([]string, len(copies))
	for i, copy := range copies {
		names[i] = oneLine(copy.Name)
	}
	fmt.Fprintf(w, "made %s from %s. connect with: ssh %s.hib\n",
		strings.Join(names, ", "), oneLine(origin), names[0])
}

// PrintSecrets writes a terminal-safe metadata table. Empty fields show as "-".
func PrintSecrets(w io.Writer, secrets []Secret) {
	field := func(s string) string {
		if s == "" {
			return "-"
		}
		return termsafe.String(s)
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tENV\tSITES\tPLACEHOLDER")
	for _, s := range secrets {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", field(s.Name), field(s.Env),
			field(strings.Join(s.Sites, ",")), field(s.Placeholder))
	}
	tw.Flush()
}

// PrintStatus writes the host's health for people, terminal-safe, one line per field. host is
// what the first line calls the host. "" leaves that line out.
func PrintStatus(w io.Writer, host string, s Status) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	if host != "" {
		fmt.Fprintf(tw, "host\t%s\n", oneLine(host))
	}
	fmt.Fprintf(tw, "hibd\t%s\n", oneLine(s.Version))
	switch {
	case s.E2B.OK:
		fmt.Fprintln(tw, "E2B\tok")
	case s.E2B.Detail == "":
		fmt.Fprintln(tw, "E2B\tdown")
	default:
		fmt.Fprintf(tw, "E2B\tdown: %s\n", oneLine(s.E2B.Detail))
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
	tw = tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "SECRET\tSITES\tSTATE\tDETAIL")
	for _, sec := range s.Secrets {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", oneLine(sec.Name), oneLine(strings.Join(sec.Sites, ",")),
			oneLine(string(sec.State)), oneLine(sec.Detail))
	}
	tw.Flush()
}

// PrintCodex writes the Codex login as PrintStatus does, terminal-safe: the account, the plan
// and the token's expiry, then what's wrong unless the login is ok.
func PrintCodex(w io.Writer, c Codex) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	printCodex(tw, &c)
	tw.Flush()
}

func printCodex(w io.Writer, c *Codex) {
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
	fmt.Fprintf(w, "\t%s: %s\n", oneLine(string(c.Health)), fix)
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(termsafe.String(s)), " ")
	if s == "" {
		return "-"
	}
	return s
}
