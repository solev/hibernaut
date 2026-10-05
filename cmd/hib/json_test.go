package main

import (
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/hibapi"
)

const hostile = "evil\x1b]0;owned\a\x1b[2J\u202e\u2066\r\n\t\x00\x7f\u00e9\u2028\U0001F600\"\\<>&'"

func everything() hibapi.Response {
	idle := int64(900)
	count := 2
	at := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	m := hibapi.Machine{Name: hostile, ID: hostile, Image: hostile, Size: hostile, ForkOf: hostile, State: hostile,
		StartedAt: at, EndAt: at.Add(time.Hour), IdleSeconds: &idle}
	return hibapi.Response{
		Machine:  &m,
		Machines: []hibapi.Machine{m},
		Secret:   &hibapi.Secret{Name: hostile, Env: hostile, Sites: []string{hostile}, Placeholder: hostile},
		Secrets:  []hibapi.Secret{{Name: hostile, Env: hostile, Sites: []string{hostile}, Placeholder: hostile}},
		Logs:     []hibapi.LogEntry{{Time: at, Machine: hostile, Site: hostile, Method: hostile, Path: hostile, Secrets: []string{hostile}}},
		Corrupt:  3,
		Room: &hibapi.Room{Fits: 2, Image: hostile, MemoryFreeMB: 4096, MemoryMB: 8192, DiskFreeMB: 1000,
			Sizes: []hibapi.SizeRoom{{Size: hostile, Fits: 1}}},
		Image:    &hibapi.Image{Name: hostile, Kind: hostile, MemoryMB: 2048, CPUs: 2, Created: at, Built: true},
		Images:   []hibapi.Image{{Name: hostile, Kind: hostile, MemoryMB: 2048, CPUs: 2, Created: at, Built: true, Shadowed: true}},
		Template: hostile,
		Sizes:    []string{hostile},
		Version:  hostile,
		GitHub:   &hibapi.GitHub{Login: hostile, Email: hostile},
		Status: &hibapi.Status{
			Version: hostile, E2B: hibapi.Health{OK: true, Detail: hostile},
			GitHub:   &hibapi.GitHub{Login: hostile, ID: 7, Name: hostile, Email: hostile, Source: hostile, Scopes: []string{hostile}},
			Codex:    &hibapi.Codex{Email: hostile, Plan: hostile, AccountID: hostile, Health: hibapi.CodexOK, Expires: at, LastRefresh: at},
			Secrets:  []hibapi.SecretHealth{{Name: hostile, Sites: []string{hostile}, State: hibapi.SecretOK, Detail: hostile}},
			Machines: &count,
		},
		Host: &hibapi.Host{State: hostile, Detail: hostile},
	}
}

func decodeOne(t *testing.T, out string) hibapi.Response {
	t.Helper()
	if strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
		t.Fatalf("stdout isn't one line: %q", out)
	}
	for _, b := range []byte(strings.TrimSuffix(out, "\n")) {
		if b < 0x20 || b > 0x7e {
			t.Fatalf("stdout has byte %#x: %q", b, out)
		}
	}
	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	var r hibapi.Response
	if err := dec.Decode(&r); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	return r
}

func sameJSON(t *testing.T, got, want hibapi.Response) {
	t.Helper()
	want.V = hibapi.Version
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var carried hibapi.Response
	if err := json.Unmarshal(data, &carried); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, carried) {
		gotData, _ := json.Marshal(got)
		t.Errorf("got  %s\nwant %s", gotData, data)
	}
}

func TestJSONPrintsOnlyTheCommandsFields(t *testing.T) {
	all := everything()
	for _, tc := range []struct {
		args []string
		want hibapi.Response
	}{
		{[]string{"ls", "--json"}, hibapi.Response{Machines: all.Machines, Room: all.Room}},
		{[]string{"new", "app", "--json"}, hibapi.Response{Machine: all.Machine}},
		{[]string{"fork", "app", "--json"}, hibapi.Response{Machines: all.Machines}},
		{[]string{"image", "ls", "--json"}, hibapi.Response{Images: all.Images}},
		{[]string{"secret", "ls", "--json"}, hibapi.Response{Secrets: all.Secrets}},
		{[]string{"log", "--json"}, hibapi.Response{Logs: all.Logs, Corrupt: all.Corrupt}},
		{[]string{"status", "--json"}, hibapi.Response{Status: all.Status, Host: &hibapi.Host{State: hibapi.HostRunning}}},
		{[]string{"ls", "-json"}, hibapi.Response{Machines: all.Machines, Room: all.Room}},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			h := newHost(t)
			c := initClient(t, h)
			h.override = func(_ []string, stdout, _ io.Writer) (int, bool) {
				return writeTestResponse(stdout, everything()), true
			}
			if code := c.hib(tc.args...); code != 0 || c.errOut.Len() != 0 {
				t.Fatalf("exit %d, stderr %q", code, c.errOut)
			}
			sameJSON(t, decodeOne(t, c.out.String()), tc.want)
		})
	}
}

func TestJSONStatusWithoutAzureHasNoHost(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h, true)
	all := everything()
	h.override = func(_ []string, stdout, _ io.Writer) (int, bool) {
		return writeTestResponse(stdout, everything()), true
	}
	if code := c.hib("status", "--json"); code != 0 {
		t.Fatalf("exit %d: %s", code, c.errOut)
	}
	sameJSON(t, decodeOne(t, c.out.String()), hibapi.Response{Status: all.Status})
}

func TestJSONStatusNeverWakesTheHost(t *testing.T) {
	for _, state := range []string{"VM deallocated", "VM stopped", "VM starting", "VM deallocated\x1b[2J\u202e"} {
		h := newHost(t)
		c := initClient(t, h)
		h.sleep(state)
		if code := c.hib("status", "--json"); code != 0 || c.errOut.Len() != 0 {
			t.Errorf("%q: exit %d, stderr %q", state, code, c.errOut)
		}
		sameJSON(t, decodeOne(t, c.out.String()), hibapi.Response{Host: &hibapi.Host{State: hibapi.HostAsleep, Detail: state}})
		for _, call := range h.takeCalls() {
			if call[0] != "az" || call[1] != "vm" || call[2] != "get-instance-view" {
				t.Errorf("%q: status --json ran %q", state, call)
			}
		}
	}

	h := newHost(t)
	c := initClient(t, h)
	h.sleep("VM deallocated")
	if c.hib("status", "--json"); c.out.String() != `{"v":1,"host":{"state":"asleep","detail":"VM deallocated"}}`+"\n" {
		t.Errorf("asleep status --json = %q", c.out)
	}
}

func TestJSONStatusFailingExitsOne(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	status := hibapi.Status{Version: "v-test", E2B: hibapi.Health{Detail: "E2B didn't answer"}}
	h.override = func(_ []string, stdout, _ io.Writer) (int, bool) {
		return writeTestResponse(stdout, hibapi.Response{Status: &status}), true
	}
	if code := c.hib("status", "--json"); code != 1 || c.errOut.Len() != 0 {
		t.Errorf("exit %d, stderr %q; want 1 and nothing", code, c.errOut)
	}
	sameJSON(t, decodeOne(t, c.out.String()), hibapi.Response{Status: &status, Host: &hibapi.Host{State: hibapi.HostRunning}})
}

func TestJSONFailures(t *testing.T) {
	all := everything()
	for _, tc := range []struct {
		name   string
		args   []string
		reply  hibapi.Response
		want   hibapi.Response
		prefix bool
	}{
		{"hibd's error", []string{"ls", "--json"}, hibapi.Response{Error: hostile}, hibapi.Response{Error: hostile}, false},
		{"fork partway", []string{"fork", "app", "--count", "3", "--json"}, hibapi.Response{Error: hostile, Machines: all.Machines},
			hibapi.Response{Error: hostile, Machines: all.Machines}, false},
		{"new without a machine", []string{"new", "app", "--json"}, hibapi.Response{},
			hibapi.Response{Error: "hibd didn't say which machine it made"}, true},
		{"fork without copies", []string{"fork", "app", "--json"}, hibapi.Response{},
			hibapi.Response{Error: "hibd didn't say which copies it made"}, true},
		{"status without a status", []string{"status", "--json"}, hibapi.Response{},
			hibapi.Response{Error: "hibd didn't report a status"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHost(t)
			c := initClient(t, h)
			h.override = func(_ []string, stdout, _ io.Writer) (int, bool) {
				return writeTestResponse(stdout, tc.reply), true
			}
			if code := c.hib(tc.args...); code != 1 || c.errOut.Len() != 0 {
				t.Errorf("exit %d, stderr %q; want 1 and nothing", code, c.errOut)
			}
			got := decodeOne(t, c.out.String())
			if tc.prefix && strings.HasPrefix(got.Error, tc.want.Error) {
				got.Error = tc.want.Error
			}
			sameJSON(t, got, tc.want)
		})
	}
}

func TestJSONFailuresBeforeTheHostAnswers(t *testing.T) {
	h := newHost(t)
	c := newClient(t, h)
	if code := c.hib("ls", "--json"); code != 1 || c.errOut.Len() != 0 {
		t.Errorf("without settings: exit %d, stderr %q", code, c.errOut)
	}
	if r := decodeOne(t, c.out.String()); !strings.Contains(r.Error, "run hib init") {
		t.Errorf("without settings: %+v", r)
	}

	c = initClient(t, h, true)
	h.override = func([]string, io.Writer, io.Writer) (int, bool) { return 255, true }
	if code := c.hib("secret", "ls", "--json"); code != 1 || c.errOut.Len() != 0 {
		t.Errorf("unreachable: exit %d, stderr %q", code, c.errOut)
	}
	if r := decodeOne(t, c.out.String()); !strings.Contains(r.Error, "couldn't reach "+testHost) {
		t.Errorf("unreachable: %+v", r)
	}

	ctx, cancel := context.WithCancelCause(t.Context())
	interruptOn(h, cancel, "new")
	if code := c.hibContext(ctx, "new", "app", "--json"); code != 130 || c.errOut.Len() != 0 {
		t.Errorf("Ctrl-C: exit %d, stderr %q", code, c.errOut)
	}
	if r := decodeOne(t, c.out.String()); r.Error != "stopped waiting. The host may still be making app: check hib ls" {
		t.Errorf("Ctrl-C: %+v", r)
	}
}

func TestJSONMistakesStayMistakes(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	for _, args := range [][]string{
		{"new", "App", "--json"},
		{"new", "app", "--size", "huge", "--json"},
		{"ls", "--json", "extra"},
	} {
		if code := c.hib(args...); code != 2 || c.out.Len() != 0 || !strings.HasPrefix(c.errOut.String(), "hib ") {
			t.Errorf("hib %q: exit %d, stdout %q, stderr %q", args, code, c.out, c.errOut)
		}
	}
	if calls := h.takeCalls(); len(calls) != 0 {
		t.Errorf("mistakes reached the host: %q", calls)
	}
}

func TestJSONIsOnlyForThatRun(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h, true)
	if code := c.hib("ls", "--json"); code != 0 {
		t.Fatalf("ls --json exit %d: %s", code, c.errOut)
	}
	h.override = func([]string, io.Writer, io.Writer) (int, bool) { return 255, true }
	if code := c.hib("ls"); code != 1 || c.out.Len() != 0 || !strings.HasPrefix(c.errOut.String(), "hib: couldn't reach") {
		t.Errorf("ls after ls --json: exit %d, stdout %q, stderr %q", code, c.out, c.errOut)
	}
}
