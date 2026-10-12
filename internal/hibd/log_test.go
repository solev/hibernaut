package hibd

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/audit"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/hibapi"
)

// linkLogApp is a host with the machines web and api, and raw1, which hibd didn't make. Its
// link log holds lines, as the listener writes them, and then a corrupt line, or doesn't exist
// when there are no lines.
func linkLogApp(t *testing.T, lines ...audit.LinkEntry) (*App, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	e := fake.New()
	addApp(e, "web1", "web", engine.Running)
	addApp(e, "api1", "api", engine.Paused)
	e.Add(engine.Machine{ID: "raw1", State: engine.Running})
	a, out, errOut := newApp(t, e)
	path := filepath.Join(t.TempDir(), "links.jsonl")
	getenv := a.Getenv
	a.Getenv = func(k string) string {
		if k == "HIBD_LINK_LOG" {
			return path
		}
		return getenv(k)
	}
	if len(lines) == 0 {
		return a, out, errOut
	}
	var data []byte
	for _, l := range lines {
		line, err := json.Marshal(l)
		if err != nil {
			t.Fatal(err)
		}
		data = append(append(data, line...), '\n')
	}
	if err := os.WriteFile(path, append(data, "{not json\n"...), 0o600); err != nil {
		t.Fatal(err)
	}
	return a, out, errOut
}

// listenerLines are a link from web to api that woke api and was revoked an hour later, and
// two refusals: from raw1 to a machine that has gone, and from a source the listener couldn't
// tell.
func listenerLines() []audit.LinkEntry {
	at := time.Date(2026, 10, 3, 7, 0, 0, 0, time.UTC)
	return []audit.LinkEntry{
		{Time: at, Event: audit.LinkOpen, From: "web1", FromID: "web1", To: "api1", ToID: "api1", Port: 8080, Woke: true},
		{Time: at.Add(time.Hour), Event: audit.LinkClose, From: "web1", FromID: "web1", To: "api1", ToID: "api1", Port: 8080,
			Reason: "revoked", BytesIn: 10, BytesOut: 20, SecondsOpen: 3600},
		{Time: at.Add(2 * time.Hour), Event: audit.LinkRefused, From: "raw1", FromID: "raw1", To: "gone\x1b1", ToID: "gone\x1b1", Port: 80,
			Reason: "not published", Count: 7},
		{Time: at.Add(3 * time.Hour), Event: audit.LinkRefused, From: "?", To: "web1", ToID: "web1", Port: 22, Reason: "not published", Count: 1},
	}
}

func TestLogLinksPutsInNamesAndKeepsTheIDsOfMachinesWithNone(t *testing.T) {
	a, out, errOut := linkLogApp(t, listenerLines()...)
	code, r, err := runJSON(t, a, out, "log", "-links", "-json")
	if code != 0 || err != nil {
		t.Fatalf("log -links -json = %d, %v; stderr %s", code, err, errOut)
	}
	want := []hibapi.LinkEntry{
		{Time: "2026-10-03T07:00:00Z", Event: "open", From: "web", To: "api", Port: 8080, Woke: true},
		{Time: "2026-10-03T08:00:00Z", Event: "close", From: "web", To: "api", Port: 8080, Reason: "revoked",
			BytesIn: 10, BytesOut: 20, SecondsOpen: 3600},
		{Time: "2026-10-03T09:00:00Z", Event: "refused", From: "raw1", To: "gone\x1b1", Port: 80, Reason: "not published", Count: 7},
		{Time: "2026-10-03T10:00:00Z", Event: "refused", From: "?", To: "web", Port: 22, Reason: "not published", Count: 1},
	}
	if !slices.Equal(r.Links, want) || r.Corrupt != 1 {
		t.Errorf("links = %+v, corrupt %d; want %+v, corrupt 1", r.Links, r.Corrupt, want)
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestLogLinksPrintsATable(t *testing.T) {
	a, out, errOut := linkLogApp(t, listenerLines()...)
	if code := a.Run(t.Context(), []string{"log", "-links"}); code != 0 {
		t.Fatalf("log -links = %d; stderr %s", code, errOut)
	}
	if want := "warning: skipped 1 corrupt link log line(s)\n"; errOut.String() != want {
		t.Errorf("stderr = %q, want %q", errOut, want)
	}
	want := [][]string{
		{"TIME", "MACHINE", "TO", "PORT", "RESULT", "WOKE", "BYTES"},
		{"2026-10-03T07:00:00Z", "web", "api", "8080", "open", "yes", "-"},
		{"2026-10-03T08:00:00Z", "web", "api", "8080", "closed after 1h: revoked", "-", "10 sent, 20 received"},
		{"2026-10-03T09:00:00Z", "raw1", `gone\x1b1`, "80", "refused: not published (7 times)", "-", "-"},
		{"2026-10-03T10:00:00Z", "?", "web", "22", "refused: not published", "-", "-"},
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != len(want) {
		t.Fatalf("log -links printed %q", out)
	}
	for i, line := range lines {
		if got := regexp.MustCompile(`\s{2,}`).Split(line, -1); !slices.Equal(got, want[i]) {
			t.Errorf("line %d = %q, want %q", i, got, want[i])
		}
	}
}

// Some lines have no port: a refusal while the host quiesces, and the line a machine's refusals
// beyond MaxFoldsPerSource fold into. The table shows - for their port, and JSON keeps 0.
func TestLogLinksShowsNoPortAsADash(t *testing.T) {
	at := time.Date(2026, 10, 3, 7, 0, 0, 0, time.UTC)
	a, out, errOut := linkLogApp(t, audit.LinkEntry{Time: at, Event: audit.LinkRefused, From: "web1", FromID: "web1", To: "?",
		Reason: "rate limited", Count: 936})
	if code := a.Run(t.Context(), []string{"log", "-links"}); code != 0 {
		t.Fatalf("log -links = %d; stderr %s", code, errOut)
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	want := []string{"2026-10-03T07:00:00Z", "web", "?", "-", "refused: rate limited (936 times)", "-", "-"}
	if got := regexp.MustCompile(`\s{2,}`).Split(lines[len(lines)-1], -1); len(lines) != 2 || !slices.Equal(got, want) {
		t.Errorf("log -links printed %q, want the line %q", out, want)
	}
	code, _, err := runJSON(t, a, out, "log", "-links", "-json")
	if code != 0 || err != nil || !strings.Contains(out.String(), `"port":0,`) {
		t.Errorf("log -links -json = %d, %v, printed %s; want port 0", code, err, out)
	}
}

func TestLogLinksShowsIDsWhenTheMachinesCantBeListed(t *testing.T) {
	a, out, errOut := linkLogApp(t, listenerLines()...)
	a.NewEngine = func() (engine.Engine, error) { return nil, errors.New("e2b is down") }
	code, r, err := runJSON(t, a, out, "log", "-links", "-json")
	if code != 0 || err != nil || len(r.Links) != 4 {
		t.Fatalf("log -links -json = %d, %+v, %v; stderr %s", code, r, err, errOut)
	}
	if r.Links[0].From != "web1" || r.Links[0].To != "api1" {
		t.Errorf("first line = %+v, want web1 to api1", r.Links[0])
	}
	if want := "warning: the link log shows sandbox IDs, not names: e2b is down\n"; errOut.String() != want {
		t.Errorf("stderr = %q, want %q", errOut, want)
	}
}

func TestLogLinksWithNoLogIsAnEmptyList(t *testing.T) {
	a, out, errOut := linkLogApp(t)
	a.NewEngine = func() (engine.Engine, error) { return nil, errors.New("unexpected engine") }
	if code := a.Run(t.Context(), []string{"log", "-links", "-json"}); code != 0 || !strings.Contains(out.String(), `"links":[]`) {
		t.Errorf("log -links -json = %d, %q; stderr %s", code, out, errOut)
	}
	out.Reset()
	if code := a.Run(t.Context(), []string{"log", "-links"}); code != 0 || strings.Fields(out.String())[0] != "TIME" || strings.Count(out.String(), "\n") != 1 {
		t.Errorf("log -links = %d, %q, want the header alone; stderr %s", code, out, errOut)
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q", errOut)
	}
}
