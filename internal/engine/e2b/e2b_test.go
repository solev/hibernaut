package e2b_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/e2b"
	"github.com/solev/hibernaut/internal/engine/e2b/envdpb/process"
	"github.com/solev/hibernaut/internal/engine/e2b/envdpb/process/processconnect"
)

const (
	apiKey    = "e2b_test_key"
	envdToken = "envd-token"
)

// fakeE2B serves the few E2B API routes the engine uses, from a map of sandboxes.
type fakeE2B struct {
	t *testing.T

	mu        sync.Mutex
	sandboxes map[string]map[string]any // id → detail JSON
	requests  []*http.Request
	bodies    map[string]map[string]any // "METHOD path" → last JSON body
}

func newFakeE2B(t *testing.T) (*fakeE2B, *e2b.Engine) {
	f := &fakeE2B{t: t, sandboxes: map[string]map[string]any{}, bodies: map[string]map[string]any{}}

	envdMux := http.NewServeMux()
	envdMux.Handle(processconnect.NewProcessHandler(&fakeEnvd{t: t}))
	envdSrv := httptest.NewServer(envdMux)
	t.Cleanup(envdSrv.Close)

	apiSrv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(apiSrv.Close)

	eng, err := e2b.New(e2b.Config{APIURL: apiSrv.URL, SandboxURL: envdSrv.URL, APIKey: apiKey})
	if err != nil {
		t.Fatal(err)
	}
	return f, eng
}

func (f *fakeE2B) add(id, state string, md map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addLocked(id, state, md)
}

func (f *fakeE2B) addLocked(id, state string, md map[string]string) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	f.sandboxes[id] = map[string]any{
		"sandboxID": id, "templateID": "tmpl123", "clientID": "c", "state": state,
		"startedAt": now, "endAt": now.Add(10 * time.Minute), "metadata": md,
		"cpuCount": 2, "memoryMB": 512, "diskSizeMB": 1024, "envdVersion": "0.2.0",
		"envdAccessToken": envdToken,
	}
}

func (f *fakeE2B) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r)
	if r.Header.Get("X-API-Key") != apiKey {
		writeJSON(w, 401, map[string]any{"code": 401, "message": "bad key"})
		return
	}
	var body map[string]any
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	f.bodies[r.Method+" "+r.URL.Path] = body

	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	notFound := func(id string) {
		writeJSON(w, 404, map[string]any{"code": 404, "message": fmt.Sprintf("sandbox %q not found", id)})
	}
	switch {
	case r.Method == "POST" && r.URL.Path == "/v2/sandboxes":
		id := "new0000000000000000a"
		md := map[string]string{}
		if m, ok := body["metadata"].(map[string]any); ok {
			for k, v := range m {
				md[k] = v.(string)
			}
		}
		f.addLocked(id, "running", md)
		writeJSON(w, 201, map[string]any{"sandboxID": id, "templateID": "tmpl123", "clientID": "c", "envdVersion": "0.2.0"})

	case r.Method == "GET" && r.URL.Path == "/v2/sandboxes":
		// Two pages: the first names the second in X-Next-Token.
		if r.URL.Query().Get("nextToken") == "" {
			w.Header().Set("X-Next-Token", "page2")
			writeJSON(w, 200, []any{f.sandboxes["aaaaaaaaaaaaaaaaaaaa"]})
			return
		}
		writeJSON(w, 200, []any{f.sandboxes["aaaaaaaaaaaaaaaaaaaa"], f.sandboxes["bbbbbbbbbbbbbbbbbbbb"]})

	case len(parts) == 2 && parts[0] == "sandboxes" && r.Method == "GET":
		sb, ok := f.sandboxes[parts[1]]
		if !ok {
			notFound(parts[1])
			return
		}
		writeJSON(w, 200, sb)

	case len(parts) == 2 && parts[0] == "sandboxes" && r.Method == "DELETE":
		if _, ok := f.sandboxes[parts[1]]; !ok {
			notFound(parts[1])
			return
		}
		delete(f.sandboxes, parts[1])
		w.WriteHeader(204)

	case len(parts) == 3 && parts[0] == "sandboxes" && parts[2] == "pause":
		sb, ok := f.sandboxes[parts[1]]
		switch {
		case !ok:
			notFound(parts[1])
		case sb["state"] == "paused":
			writeJSON(w, 409, map[string]any{"code": 409, "message": "already paused"})
		default:
			sb["state"] = "paused"
			w.WriteHeader(204)
		}

	case len(parts) == 3 && parts[0] == "sandboxes" && parts[2] == "timeout":
		sb, ok := f.sandboxes[parts[1]]
		if !ok || sb["state"] != "running" {
			notFound(parts[1]) // E2B's answer for paused machines too
			return
		}
		w.WriteHeader(204)

	case len(parts) == 4 && parts[0] == "v2" && parts[3] == "connect":
		sb, ok := f.sandboxes[parts[2]]
		if !ok {
			notFound(parts[2])
			return
		}
		status := 200
		if sb["state"] == "paused" {
			sb["state"], status = "running", 201
		}
		writeJSON(w, status, map[string]any{"sandboxID": parts[2], "templateID": "tmpl123", "clientID": "c", "envdVersion": "0.2.0"})

	case len(parts) == 3 && parts[0] == "sandboxes" && parts[2] == "metrics":
		if parts[1] == "nometrics00000000000" {
			writeJSON(w, 200, []any{})
			return
		}
		t0 := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
		writeJSON(w, 200, []any{
			map[string]any{"timestamp": t0, "timestampUnix": t0.Unix(), "cpuCount": 2, "cpuUsedPct": 1.5, "memUsed": 1, "memTotal": 2, "memCache": 0, "diskUsed": 3, "diskTotal": 4},
			map[string]any{"timestamp": t0.Add(5 * time.Second), "timestampUnix": t0.Unix() + 5, "cpuCount": 2, "cpuUsedPct": 7.5, "memUsed": 10, "memTotal": 20, "memCache": 0, "diskUsed": 30, "diskTotal": 40},
		})

	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL)
		w.WriteHeader(500)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// fakeEnvd answers Process.Start: it echoes the command line to stdout, "err" to stderr, and
// exits 7.
type fakeEnvd struct {
	processconnect.UnimplementedProcessHandler
	t *testing.T
}

func (f *fakeEnvd) Start(_ context.Context, req *connect.Request[process.StartRequest], stream *connect.ServerStream[process.StartResponse]) error {
	h := req.Header()
	if h.Get("E2b-Sandbox-Id") != "aaaaaaaaaaaaaaaaaaaa" || h.Get("E2b-Sandbox-Port") != "49983" || h.Get("X-Access-Token") != envdToken {
		return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("bad routing headers: %v", h))
	}
	p := req.Msg.GetProcess()
	line := fmt.Sprintf("%s %s cwd=%s env=%s", p.GetCmd(), strings.Join(p.GetArgs(), " "), p.GetCwd(), p.GetEnvs()["K"])
	events := []*process.ProcessEvent{
		{Event: &process.ProcessEvent_Start{Start: &process.ProcessEvent_StartEvent{Pid: 42}}},
		{Event: &process.ProcessEvent_Data{Data: &process.ProcessEvent_DataEvent{Output: &process.ProcessEvent_DataEvent_Stdout{Stdout: []byte(line)}}}},
		{Event: &process.ProcessEvent_Keepalive{Keepalive: &process.ProcessEvent_KeepAlive{}}},
		{Event: &process.ProcessEvent_Data{Data: &process.ProcessEvent_DataEvent{Output: &process.ProcessEvent_DataEvent_Stderr{Stderr: []byte("err")}}}},
		{Event: &process.ProcessEvent_End{End: &process.ProcessEvent_EndEvent{ExitCode: 7, Exited: true, Status: "exit status 7"}}},
	}
	for _, ev := range events {
		if err := stream.Send(&process.StartResponse{Event: ev}); err != nil {
			return err
		}
	}
	return nil
}

func TestCreate(t *testing.T) {
	f, e := newFakeE2B(t)
	m, err := e.Create(t.Context(), engine.CreateOptions{
		Template: "base",
		Timeout:  90*time.Second + time.Millisecond,
		Metadata: map[string]string{"hib": "1"},
		Env:      map[string]string{"A": "b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "new0000000000000000a" || m.State != engine.Running || m.Metadata["hib"] != "1" || m.Template != "tmpl123" {
		t.Errorf("Create = %+v", m)
	}
	body := f.bodies["POST /v2/sandboxes"]
	if body["templateID"] != "base" || body["autoPause"] != true || body["timeout"] != float64(91) {
		t.Errorf("create body = %v, want templateID base, autoPause true and the timeout rounded up to 91 s", body)
	}
	if env, _ := body["envVars"].(map[string]any); env["A"] != "b" {
		t.Errorf("create body envVars = %v", body["envVars"])
	}
}

func TestListFollowsPagesAndEncodesFilters(t *testing.T) {
	f, e := newFakeE2B(t)
	f.add("aaaaaaaaaaaaaaaaaaaa", "running", map[string]string{"k": "v"})
	f.add("bbbbbbbbbbbbbbbbbbbb", "paused", nil)

	ms, err := e.List(t.Context(), engine.ListOptions{
		States:   []engine.State{engine.Running, engine.Paused},
		Metadata: map[string]string{"k": "v w", "a&b": "c"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 || ms[0].ID != "aaaaaaaaaaaaaaaaaaaa" || ms[1].ID != "bbbbbbbbbbbbbbbbbbbb" || ms[1].State != engine.Paused {
		t.Errorf("List = %+v, want a and b once each", ms)
	}

	first := f.requests[0].URL.Query()
	if got := first.Get("state"); got != "running,paused" {
		t.Errorf("state = %q, want running,paused", got)
	}
	md, err := url.ParseQuery(first.Get("metadata"))
	if err != nil || md.Get("k") != "v w" || md.Get("a&b") != "c" {
		t.Errorf("metadata = %q (%v), want an encoded query with both pairs", first.Get("metadata"), err)
	}
	if got := f.requests[1].URL.Query().Get("nextToken"); got != "page2" {
		t.Errorf("second page nextToken = %q, want page2", got)
	}
}

func TestErrorMapping(t *testing.T) {
	f, e := newFakeE2B(t)
	f.add("aaaaaaaaaaaaaaaaaaaa", "paused", nil)
	ctx := t.Context()
	const unknown = "zzzzzzzzzzzzzzzzzzzz"

	cases := []struct {
		name string
		err  error
		want error
	}{
		{"Get unknown", errOf(e.Get(ctx, unknown)), engine.ErrNotFound},
		{"Pause unknown", e.Pause(ctx, unknown), engine.ErrNotFound},
		{"Pause paused", e.Pause(ctx, "aaaaaaaaaaaaaaaaaaaa"), engine.ErrNotRunning},
		{"Kill unknown", e.Kill(ctx, unknown), engine.ErrNotFound},
		{"SetTimeout unknown", e.SetTimeout(ctx, unknown, time.Minute), engine.ErrNotFound},
		{"SetTimeout paused", e.SetTimeout(ctx, "aaaaaaaaaaaaaaaaaaaa", time.Minute), engine.ErrNotRunning},
		{"Exec paused", errOf(e.Exec(ctx, "aaaaaaaaaaaaaaaaaaaa", engine.ExecRequest{Cmd: "true"})), engine.ErrNotRunning},
		{"Metrics paused", errOf(e.Metrics(ctx, "aaaaaaaaaaaaaaaaaaaa")), engine.ErrNotRunning},
		{"Resume unknown", errOf(e.Resume(ctx, unknown, time.Minute)), engine.ErrNotFound},
	}
	for _, c := range cases {
		if !errors.Is(c.err, c.want) {
			t.Errorf("%s = %v, want %v", c.name, c.err, c.want)
		}
	}
	// Exec on a paused machine must not reach envd, or the proxy could wake it.
	for _, r := range f.requests {
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/connect") && strings.Contains(r.URL.Path, "aaaa") {
			t.Errorf("a call on the paused machine resumed it: %s %s", r.Method, r.URL.Path)
		}
	}
}

func TestResumeAndKill(t *testing.T) {
	f, e := newFakeE2B(t)
	f.add("aaaaaaaaaaaaaaaaaaaa", "paused", nil)
	ctx := t.Context()
	m, err := e.Resume(ctx, "aaaaaaaaaaaaaaaaaaaa", 0)
	if err != nil || m.State != engine.Running {
		t.Fatalf("Resume = %+v, %v; want running", m, err)
	}
	if body := f.bodies["POST /v2/sandboxes/aaaaaaaaaaaaaaaaaaaa/connect"]; len(body) != 0 {
		t.Errorf("Resume with no timeout sent %v, want E2B's default (no timeout field)", body)
	}
	if err := e.Kill(ctx, "aaaaaaaaaaaaaaaaaaaa"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Get(ctx, "aaaaaaaaaaaaaaaaaaaa"); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("Get after Kill = %v, want ErrNotFound", err)
	}
}

func TestExec(t *testing.T) {
	f, e := newFakeE2B(t)
	f.add("aaaaaaaaaaaaaaaaaaaa", "running", nil)
	res, err := e.Exec(t.Context(), "aaaaaaaaaaaaaaaaaaaa", engine.ExecRequest{
		Cmd: "sh", Args: []string{"-c", "x"}, Dir: "/tmp", Env: map[string]string{"K": "v"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 7 || string(res.Stdout) != "sh -c x cwd=/tmp env=v" || string(res.Stderr) != "err" {
		t.Errorf("Exec = {exit %d, stdout %q, stderr %q}", res.ExitCode, res.Stdout, res.Stderr)
	}
}

func TestMetricsPicksLatestSample(t *testing.T) {
	f, e := newFakeE2B(t)
	f.add("aaaaaaaaaaaaaaaaaaaa", "running", nil)
	f.add("nometrics00000000000", "running", nil)
	m, err := e.Metrics(t.Context(), "aaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	if m.CPUUsedPct != 7.5 || m.MemTotal != 20 || m.CPUCount != 2 {
		t.Errorf("Metrics = %+v, want the later sample", m)
	}
	if _, err := e.Metrics(t.Context(), "nometrics00000000000"); !errors.Is(err, engine.ErrNoMetrics) {
		t.Errorf("Metrics with no samples = %v, want ErrNoMetrics", err)
	}
}

func TestNewRequiresConfig(t *testing.T) {
	if _, err := e2b.New(e2b.Config{APIURL: "http://127.0.0.1:3000"}); err == nil {
		t.Error("New with no SandboxURL or APIKey succeeded")
	}
}

func errOf[T any](_ T, err error) error { return err }
