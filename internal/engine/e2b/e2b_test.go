package e2b_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/coder/websocket"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/e2b"
	"github.com/solev/hibernaut/internal/engine/e2b/envdpb/process"
	"github.com/solev/hibernaut/internal/engine/e2b/envdpb/process/processconnect"
)

const (
	apiKey       = "e2b_test_key"
	envdToken    = "envd-token"
	trafficToken = "traffic-token"
)

type fakeE2B struct {
	t *testing.T

	mu         sync.Mutex
	sandboxes  map[string]map[string]any
	requests   []*http.Request
	bodies     map[string]map[string]any
	statuses   []map[string]any
	offsets    []string
	redirectTo string

	maxMessage atomic.Int64
}

func newFakeE2B(t *testing.T) (*fakeE2B, *e2b.Engine) {
	f := &fakeE2B{t: t, sandboxes: map[string]map[string]any{}, bodies: map[string]map[string]any{}}

	envdMux := http.NewServeMux()
	envdMux.Handle(processconnect.NewProcessHandler(&fakeEnvd{t: t}))
	envdMux.HandleFunc("/", f.serveWebSocket)

	envdSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		redirect := f.redirectTo
		f.mu.Unlock()
		if redirect != "" {
			http.Redirect(w, r, redirect, http.StatusFound)
			return
		}
		envdMux.ServeHTTP(w, r)
	}))
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
		"envdAccessToken": envdToken, "trafficAccessToken": trafficToken,
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
	case r.Method == "POST" && r.URL.Path == "/v2/sandboxes" && body["templateID"] == "missing":
		writeJSON(w, 404, map[string]any{"code": 404, "message": "template 'missing' not found"})

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
		writeJSON(w, status, map[string]any{
			"sandboxID": parts[2], "templateID": "tmpl123", "clientID": "c", "envdVersion": "0.2.0",
			"envdAccessToken": envdToken, "trafficAccessToken": sb["trafficAccessToken"],
		})

	case r.Method == "POST" && r.URL.Path == "/v3/templates":
		writeJSON(w, 202, map[string]any{
			"templateID": "tpl1", "buildID": "build1", "aliases": []string{}, "names": []string{body["name"].(string)},
			"public": false, "tags": []string{},
		})

	case r.Method == "POST" && r.URL.Path == "/v2/templates/tpl1/builds/build1":
		w.WriteHeader(202)

	case r.Method == "GET" && r.URL.Path == "/templates/tpl1/builds/build1/status":
		f.offsets = append(f.offsets, r.URL.Query().Get("logsOffset"))
		st := f.statuses[0]
		if len(f.statuses) > 1 {
			f.statuses = f.statuses[1:]
		}
		writeJSON(w, 200, st)

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

func (f *fakeE2B) serveWebSocket(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("E2b-Sandbox-Id") != "aaaaaaaaaaaaaaaaaaaa" || r.Header.Get("E2b-Sandbox-Port") != "8081" {
		http.Error(w, "bad routing headers", http.StatusBadRequest)
		return
	}
	if r.Header.Get("E2b-Traffic-Access-Token") != trafficToken {
		http.Error(w, "bad traffic access token", http.StatusForbidden)
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(-1)
	for {
		typ, msg, err := c.Read(context.Background())
		if err != nil {
			return
		}
		if typ != websocket.MessageBinary {
			msg = []byte("not binary")
		}
		for {
			old := f.maxMessage.Load()
			if int64(len(msg)) <= old || f.maxMessage.CompareAndSwap(old, int64(len(msg))) {
				break
			}
		}
		if err := c.Write(context.Background(), websocket.MessageBinary, msg); err != nil {
			return
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

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
	user := "-"
	if h.Get("Authorization") != "" {
		u, pass, ok := (&http.Request{Header: h}).BasicAuth()
		if !ok || pass != "" {
			return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("bad Authorization %q", h.Get("Authorization")))
		}
		user = u
	}
	if p.GetCmd() == "flood" {
		chunk := bytes.Repeat([]byte("x"), 1<<20)
		for range 10 {
			ev := &process.ProcessEvent{Event: &process.ProcessEvent_Data{Data: &process.ProcessEvent_DataEvent{Output: &process.ProcessEvent_DataEvent_Stdout{Stdout: chunk}}}}
			if err := stream.Send(&process.StartResponse{Event: ev}); err != nil {
				return err
			}
		}
		return stream.Send(&process.StartResponse{Event: &process.ProcessEvent{Event: &process.ProcessEvent_End{End: &process.ProcessEvent_EndEvent{Exited: true}}}})
	}
	line := fmt.Sprintf("%s %s cwd=%s env=%s user=%s", p.GetCmd(), strings.Join(p.GetArgs(), " "), p.GetCwd(), p.GetEnvs()["K"], user)
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
	if m.ID != "new0000000000000000a" || m.State != engine.Running || m.Metadata["hib"] != "1" || m.Template != "tmpl123" ||
		m.MemoryMB != 512 {
		t.Errorf("Create = %+v", m)
	}
	body := f.bodies["POST /v2/sandboxes"]
	if body["templateID"] != "base" || body["autoPause"] != true || body["timeout"] != float64(91) {
		t.Errorf("create body = %v, want templateID base, autoPause true and the timeout rounded up to 91 s", body)
	}
	if body["autoPauseMemory"] != true {
		t.Errorf("create body autoPauseMemory = %v, want true: a paused machine keeps its processes", body["autoPauseMemory"])
	}
	if resume, _ := body["autoResume"].(map[string]any); resume["enabled"] != false {
		t.Errorf("create body autoResume = %v, want enabled false: only hibd wakes machines", body["autoResume"])
	}
	if network, _ := body["network"].(map[string]any); network["allowPublicTraffic"] != false {
		t.Errorf("create body network = %v, want allowPublicTraffic false", body["network"])
	}
	if env, _ := body["envVars"].(map[string]any); env["A"] != "b" {
		t.Errorf("create body envVars = %v", body["envVars"])
	}

	if _, err := e.Create(t.Context(), engine.CreateOptions{Template: "missing"}); !errors.Is(err, engine.ErrNoTemplate) ||
		errors.Is(err, engine.ErrNotFound) {
		t.Errorf("Create(missing template) = %v, want ErrNoTemplate only", err)
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
	if len(ms) != 2 || ms[0].ID != "aaaaaaaaaaaaaaaaaaaa" || ms[1].ID != "bbbbbbbbbbbbbbbbbbbb" || ms[1].State != engine.Paused ||
		ms[0].MemoryMB != 512 {
		t.Errorf("List = %+v, want a and b once each, with their memory", ms)
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
	if res.ExitCode != 7 || string(res.Stdout) != "sh -c x cwd=/tmp env=v user=-" || string(res.Stderr) != "err" {
		t.Errorf("Exec = {exit %d, stdout %q, stderr %q}", res.ExitCode, res.Stdout, res.Stderr)
	}

	res, err = e.Exec(t.Context(), "aaaaaaaaaaaaaaaaaaaa", engine.ExecRequest{Cmd: "id", User: "root"})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(res.Stdout); got != "id  cwd= env= user=root" {
		t.Errorf("Exec as root reached envd as %q, want Basic auth user root", got)
	}
}

func TestExecOutputCap(t *testing.T) {
	f, e := newFakeE2B(t)
	f.add("aaaaaaaaaaaaaaaaaaaa", "running", nil)
	_, err := e.Exec(t.Context(), "aaaaaaaaaaaaaaaaaaaa", engine.ExecRequest{Cmd: "flood"})
	if err == nil || !strings.Contains(err.Error(), "more than") {
		t.Errorf("Exec(flood) = %v, want an error about too much output", err)
	}
}

func TestDialWebSocket(t *testing.T) {
	f, e := newFakeE2B(t)
	f.add("aaaaaaaaaaaaaaaaaaaa", "paused", nil)

	ctx, cancel := context.WithCancel(t.Context())
	c, err := e.DialWebSocket(ctx, "aaaaaaaaaaaaaaaaaaaa", 8081, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	cancel()

	if body := f.bodies["POST /v2/sandboxes/aaaaaaaaaaaaaaaaaaaa/connect"]; body["timeout"] != float64(3600) {
		t.Errorf("connect body = %v, want the 1h timeout", body)
	}
	if st := f.sandboxes["aaaaaaaaaaaaaaaaaaaa"]["state"]; st != "running" {
		t.Errorf("state after dial = %v, want the paused machine resumed", st)
	}

	want := make([]byte, 100<<10)
	rand.Read(want)
	go func() {
		if _, err := c.Write(want); err != nil {
			t.Errorf("Write: %v", err)
		}
	}()
	got := make([]byte, len(want))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatalf("ReadFull: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Error("echoed bytes differ from the bytes written")
	}
	if largest := f.maxMessage.Load(); largest == 0 || largest > 32<<10 {
		t.Errorf("largest message = %d bytes, want at most 32 KiB", largest)
	}
}

func TestDialWebSocketErrors(t *testing.T) {
	f, e := newFakeE2B(t)
	f.add("aaaaaaaaaaaaaaaaaaaa", "running", nil)
	f.mu.Lock()
	f.sandboxes["aaaaaaaaaaaaaaaaaaaa"]["trafficAccessToken"] = "stale"
	f.mu.Unlock()

	if _, err := e.DialWebSocket(t.Context(), "zzzzzzzzzzzzzzzzzzzz", 8081, time.Hour); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("DialWebSocket(unknown) = %v, want ErrNotFound", err)
	}
	if _, err := e.DialWebSocket(t.Context(), "aaaaaaaaaaaaaaaaaaaa", 8081, time.Hour); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("DialWebSocket with a wrong traffic token = %v, want a 403 error", err)
	}
}

// TestDialWebSocketRejectsRedirect checks that an attacker-controlled machine can't turn a dial
// into a host-side request of its choosing (SSRF): a redirect from the machine's port must not
// be followed, and the dial must fail instead.
func TestDialWebSocketRejectsRedirect(t *testing.T) {
	f, e := newFakeE2B(t)
	f.add("aaaaaaaaaaaaaaaaaaaa", "running", nil)

	var hit atomic.Bool
	attacker := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hit.Store(true)
	}))
	t.Cleanup(attacker.Close)

	f.mu.Lock()
	f.redirectTo = attacker.URL
	f.mu.Unlock()

	if _, err := e.DialWebSocket(t.Context(), "aaaaaaaaaaaaaaaaaaaa", 8081, time.Hour); err == nil {
		t.Error("DialWebSocket followed a redirect and succeeded, want it refused")
	}
	if hit.Load() {
		t.Error("the dial followed the machine's redirect to another server (SSRF)")
	}
}

// TestExecRejectsRedirect is TestDialWebSocketRejectsRedirect for the envd RPC path.
func TestExecRejectsRedirect(t *testing.T) {
	f, e := newFakeE2B(t)
	f.add("aaaaaaaaaaaaaaaaaaaa", "running", nil)

	var hit atomic.Bool
	attacker := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hit.Store(true)
	}))
	t.Cleanup(attacker.Close)

	f.mu.Lock()
	f.redirectTo = attacker.URL
	f.mu.Unlock()

	if _, err := e.Exec(t.Context(), "aaaaaaaaaaaaaaaaaaaa", engine.ExecRequest{Cmd: "id"}); err == nil {
		t.Error("Exec followed a redirect and succeeded, want it refused")
	}
	if hit.Load() {
		t.Error("the envd RPC followed the machine's redirect to another server (SSRF)")
	}
}

func TestBuildTemplate(t *testing.T) {
	fastPolls(t)
	f, e := newFakeE2B(t)
	entry := func(msg string) map[string]any {
		return map[string]any{"level": "info", "message": msg, "timestamp": time.Now()}
	}
	f.statuses = []map[string]any{
		{"templateID": "tpl1", "buildID": "build1", "status": "waiting", "logs": []string{}, "logEntries": []any{}},
		{"templateID": "tpl1", "buildID": "build1", "status": "building", "logs": []string{}, "logEntries": []any{entry("one"), entry("two")}},
		{"templateID": "tpl1", "buildID": "build1", "status": "ready", "logs": []string{}, "logEntries": []any{entry("three")}},
		{"templateID": "tpl1", "buildID": "build1", "status": "ready", "logs": []string{}, "logEntries": []any{}},
	}
	var logs bytes.Buffer
	id, err := e.BuildTemplate(t.Context(), engine.TemplateSpec{
		Name:          "hib-dev",
		FromImage:     "ubuntu:24.04",
		Steps:         []engine.BuildStep{{Cmd: "apt-get update", User: "root"}, {Cmd: "echo hi"}},
		StartCmd:      "start",
		ReadyCmd:      "ready",
		CPUs:          2,
		MemoryMB:      2048,
		MinFreeDiskMB: 10240,
		Force:         true,
	}, &logs)
	if err != nil {
		t.Fatal(err)
	}
	if id != "tpl1" {
		t.Errorf("BuildTemplate = %q, want tpl1", id)
	}
	if got := logs.String(); got != "one\ntwo\nthree\n" {
		t.Errorf("logs = %q, want every entry once, in order", got)
	}
	if got := strings.Join(f.offsets, ","); got != "0,0,2,3" {
		t.Errorf("logsOffset of each poll = %s, want 0,0,2,3", got)
	}

	req := f.bodies["POST /v3/templates"]
	if req["name"] != "hib-dev" || req["cpuCount"] != float64(2) || req["memoryMB"] != float64(2048) || req["minFreeDiskMb"] != float64(10240) {
		t.Errorf("v3 request = %v", req)
	}
	start := f.bodies["POST /v2/templates/tpl1/builds/build1"]
	steps, _ := json.Marshal(start["steps"])
	if string(steps) != `[{"args":["apt-get update","root"],"type":"RUN"},{"args":["echo hi"],"type":"RUN"}]` {
		t.Errorf("steps = %s", steps)
	}
	if start["fromImage"] != "ubuntu:24.04" || start["startCmd"] != "start" || start["readyCmd"] != "ready" || start["force"] != true {
		t.Errorf("build start = %v", start)
	}
}

func TestBuildTemplateFailure(t *testing.T) {
	fastPolls(t)
	f, e := newFakeE2B(t)
	f.statuses = []map[string]any{
		{"templateID": "tpl1", "buildID": "build1", "status": "error", "logs": []string{}, "logEntries": []any{},
			"reason": map[string]any{"message": "exit status 1", "step": "2"}},
	}
	_, err := e.BuildTemplate(t.Context(), engine.TemplateSpec{Name: "hib-dev", FromImage: "ubuntu:24.04"}, nil)
	if err == nil || !strings.Contains(err.Error(), "failed at step 2: exit status 1") {
		t.Errorf("BuildTemplate = %v, want the failing step and reason", err)
	}
	if _, err := e.BuildTemplate(t.Context(), engine.TemplateSpec{Name: "hib-dev"}, nil); err == nil {
		t.Error("BuildTemplate with no FromImage succeeded")
	}
}

// E2B quotes a failed step's whole command, which can be most of a megabyte of file contents.
func TestBuildTemplateElidesLongMessages(t *testing.T) {
	fastPolls(t)
	f, e := newFakeE2B(t)
	cmd := "RUN put /x 755 '" + strings.Repeat("QUFB", 5000) + "'; /x"
	f.statuses = []map[string]any{
		{"templateID": "tpl1", "buildID": "build1", "status": "error", "logs": []string{},
			"logEntries": []any{map[string]any{"timestamp": "2026-01-01T00:00:00Z", "level": "info", "message": cmd}}},
		{"templateID": "tpl1", "buildID": "build1", "status": "error", "logs": []string{}, "logEntries": []any{},
			"reason": map[string]any{"message": "failed to run command '" + cmd + "': exit status 1", "step": "1"}},
	}
	var logs strings.Builder
	_, err := e.BuildTemplate(t.Context(), engine.TemplateSpec{Name: "hib-dev", FromImage: "ubuntu:24.04"}, &logs)
	if err == nil || len(err.Error()) > 1000 || !strings.Contains(err.Error(), "failed at step 1: failed to run command 'RUN put /x") ||
		!strings.HasSuffix(err.Error(), "'; /x': exit status 1") || !strings.Contains(err.Error(), "bytes) …") {
		t.Errorf("BuildTemplate = %v", err)
	}
	if line := logs.String(); len(line) > 1000 || !strings.HasPrefix(line, "RUN put /x 755 'QUFB") || !strings.HasSuffix(line, "QUFB'; /x\n") {
		t.Errorf("logs = %q", line)
	}
}

func fastPolls(t *testing.T) {
	old := e2b.BuildPollInterval
	e2b.BuildPollInterval = time.Millisecond
	t.Cleanup(func() { e2b.BuildPollInterval = old })
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
