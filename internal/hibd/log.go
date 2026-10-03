package hibd

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/solev/hibernaut/internal/audit"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/termsafe"
)

func (a *App) log(args []string) int {
	fl := a.flags("log")
	n := fl.Int("n", 50, "number of newest entries to show")
	asJSON := fl.Bool("json", false, "print JSON")
	if _, ok := a.parse(fl, args, 0, "log [-n N] [-json]"); !ok {
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
	result, err := audit.Read(cfg.auditLogPath, *n)
	if err != nil {
		return a.fail(*asJSON, "log", err)
	}
	if result.Corrupt > 0 {
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
