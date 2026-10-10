package hibd

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/solev/hibernaut/internal/audit"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/machines"
	"github.com/solev/hibernaut/internal/termsafe"
)

func (a *App) log(args []string) int {
	fl := a.flags("log")
	n := fl.Int("n", 50, "number of newest entries to show")
	linkLog := fl.Bool("links", false, "show the link log: links between machines, and the ones refused")
	asJSON := fl.Bool("json", false, "print JSON")
	if _, ok := a.parse(fl, args, 0, "log [-n N] [-links] [-json]"); !ok {
		return 2
	}
	if *n < 0 {
		fmt.Fprintln(a.Stderr, "hibd log: -n must be non-negative")
		return 2
	}
	cfg, err := loadConfig(a.Getenv)
	if err != nil {
		return a.fail(*asJSON, "log", err)
	}
	if *linkLog {
		return a.logLinks(cfg, *n, *asJSON)
	}
	result, err := audit.Read(cfg.auditLogPath, *n)
	if err != nil {
		return a.fail(*asJSON, "log", err)
	}
	if result.Corrupt > 0 && !*asJSON {
		fmt.Fprintf(a.Stderr, "warning: skipped %d corrupt audit log line(s)\n", result.Corrupt)
	}
	entries := apiLogEntries(result.Entries)
	if *asJSON {
		return a.reply(hibapi.Response{Logs: entries, Corrupt: result.Corrupt})
	}
	printLogEntries(a.Stdout, entries)
	return 0
}

func apiLogEntries(entries []audit.Entry) []hibapi.LogEntry {
	out := make([]hibapi.LogEntry, len(entries))
	for i, e := range entries {
		out[i] = hibapi.LogEntry{
			Time:    e.Time.UTC(),
			Machine: e.Machine,
			Site:    e.Site,
			Method:  e.Method,
			Path:    e.Path,
			Secrets: append([]string(nil), e.Secrets...),
		}
	}
	return out
}

func printLogEntries(w io.Writer, entries []hibapi.LogEntry) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TIME\tMACHINE\tSITE\tMETHOD\tPATH\tSECRETS")
	for _, e := range entries {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			e.Time.UTC().Format(time.RFC3339),
			termsafe.String(e.Machine),
			termsafe.String(e.Site),
			termsafe.String(e.Method),
			termsafe.String(e.Path),
			termsafe.String(strings.Join(e.Secrets, ",")))
	}
	tw.Flush()
}

// logLinks prints the newest n entries of the link log, with machine names where the listener
// wrote sandbox IDs.
func (a *App) logLinks(cfg config, n int, asJSON bool) int {
	result, err := audit.ReadLinks(cfg.linkLog, n)
	if err != nil {
		return a.fail(asJSON, "log", err)
	}
	if result.Corrupt > 0 && !asJSON {
		fmt.Fprintf(a.Stderr, "warning: skipped %d corrupt link log line(s)\n", result.Corrupt)
	}
	entries := apiLinkEntries(result.Entries, a.machineNames(result.Entries))
	if asJSON {
		return a.reply(hibapi.Response{Links: entries, Corrupt: result.Corrupt})
	}
	hibapi.PrintLinkEntries(a.Stdout, entries, nil)
	return 0
}

// machineNames maps the sandbox IDs of every machine E2B has, paused ones included, to their
// names. The listener knows machines only by ID, so this is how the link log gets names. When
// the machines can't be listed the log still prints, with IDs.
func (a *App) machineNames(entries []audit.LinkEntry) map[string]string {
	if len(entries) == 0 {
		return nil
	}
	e, err := a.engine()
	if err != nil {
		a.warnf("the link log shows sandbox IDs, not names: %v", err)
		return nil
	}
	// Run's dispatch gives log no context.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ms, err := e.List(ctx, engine.ListOptions{})
	if err != nil {
		a.warnf("the link log shows sandbox IDs, not names: %v", err)
		return nil
	}
	names := make(map[string]string, len(ms))
	for _, m := range ms {
		if name := machines.Name(m); name != "" {
			names[m.ID] = name
		}
	}
	return names
}

// apiLinkEntries puts the name of each machine in names in From and To, and keeps what the
// listener wrote, the sandbox ID or "?", for a machine that has none. The slice is never nil, so
// an empty log is "links":[] in JSON.
func apiLinkEntries(entries []audit.LinkEntry, names map[string]string) []hibapi.LinkEntry {
	named := func(written, id string) string {
		if name := names[id]; name != "" {
			return name
		}
		return written
	}
	out := make([]hibapi.LinkEntry, len(entries))
	for i, e := range entries {
		out[i] = hibapi.LinkEntry{
			Time:        e.Time.UTC().Format(time.RFC3339),
			Event:       e.Event,
			From:        named(e.From, e.FromID),
			To:          named(e.To, e.ToID),
			Port:        e.Port,
			Reason:      e.Reason,
			Woke:        e.Woke,
			Count:       e.Count,
			BytesIn:     e.BytesIn,
			BytesOut:    e.BytesOut,
			SecondsOpen: e.SecondsOpen,
		}
	}
	return out
}
