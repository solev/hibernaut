package e2b_test

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
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
	envd       http.Handler
	uploads    []uploadedFile
	fileRead   chan struct{}
	// refusals are what the next connects answer, one each, before connects resume machines
	// again.
	refusals []refusal

	maxMessage atomic.Int64
}

type refusal struct {
	status int
	body   map[string]any
}

type uploadedFile struct {
	path, user string
	body       []byte
}

// refuseConnect makes the next connect answer status with E2B's error body. An empty
// errorCode leaves error_code out.
func (f *fakeE2B) refuseConnect(status int, errorCode, message string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body := map[string]any{"code": status, "message": message}
	if errorCode != "" {
		body["error_code"] = errorCode
	}
	f.refusals = append(f.refusals, refusal{status, body})
}

func newFakeE2B(t *testing.T) (*fakeE2B, *e2b.Engine) {
	f := &fakeE2B{t: t, sandboxes: map[string]map[string]any{}, bodies: map[string]map[string]any{}}

	envdMux := http.NewServeMux()
	envdMux.Handle(processconnect.NewProcessHandler(&fakeEnvd{t: t}))
	envdMux.HandleFunc("POST /files", f.serveFiles)
	envdMux.HandleFunc("/", f.serveWebSocket)

	envdSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		redirect, envd := f.redirectTo, f.envd
		f.mu.Unlock()
		switch {
		case redirect != "":
			http.Redirect(w, r, redirect, http.StatusFound)
		case envd != nil:
			envd.ServeHTTP(w, r)
		default:
			envdMux.ServeHTTP(w, r)
		}
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
		if len(f.refusals) > 0 {
			r := f.refusals[0]
			f.refusals = f.refusals[1:]
			writeJSON(w, r.status, r.body)
			return
		}
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

func (f *fakeE2B) serveFiles(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("E2b-Sandbox-Id") != "aaaaaaaaaaaaaaaaaaaa" ||
		r.Header.Get("E2b-Sandbox-Port") != "49983" || r.Header.Get("X-Access-Token") != envdToken {
		f.t.Error("upload has incorrect routing or access token headers")
		http.Error(w, "bad headers", http.StatusForbidden)
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		f.t.Error(err)
		return
	}
	part, err := mr.NextPart()
	if err != nil {
		f.t.Error(err)
		return
	}
	if part.FormName() != "file" || part.FileName() == "" {
		f.t.Errorf("multipart part = %q, filename %q", part.FormName(), part.FileName())
	}
	first := make([]byte, 1)
	n, err := part.Read(first)
	if err != nil && err != io.EOF {
		f.t.Error(err)
		return
	}
	f.mu.Lock()
	if f.fileRead != nil {
		close(f.fileRead)
		f.fileRead = nil
	}
	f.mu.Unlock()
	body, err := io.ReadAll(part)
	if err != nil {
		f.t.Error(err)
		return
	}
	if _, err := mr.NextPart(); err != io.EOF {
		f.t.Errorf("next multipart part = %v, want EOF", err)
	}
	f.mu.Lock()
	f.uploads = append(f.uploads, uploadedFile{
		path: r.URL.Query().Get("path"), user: r.URL.Query().Get("username"),
		body: append(first[:n], body...),
	})
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, []any{})
}

func TestUploadStreamsFilesAndReopensOnRetry(t *testing.T) {
	f, e := newFakeE2B(t)
	f.add("aaaaaaaaaaaaaaaaaaaa", "running", nil)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	received := make(chan struct{})
	f.fileRead = received
	payload := strings.Repeat("streamed", 16<<10)
	var opened, closed atomic.Int32
	files := []engine.File{
		{
			Path: "/root/new folder/a&b?prompt.txt", Size: int64(len(payload)),
			Open: func() (io.ReadCloser, error) {
				attempt := opened.Add(1)
				r, w := io.Pipe()
				go func() {
					_, err := io.WriteString(w, payload[:64<<10])
					if err == nil && attempt == 1 {
						select {
						case <-received:
						case <-ctx.Done():
							err = errors.New("Upload buffered the file before envd read its first byte")
						}
					}
					if err == nil {
						_, err = io.WriteString(w, payload[64<<10:])
					}
					_ = w.CloseWithError(err)
				}()
				return &trackedUploadReader{ReadCloser: r, closed: &closed}, nil
			},
		},
		{
			Path: "/root/small", Size: 5,
			Open: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("small")), nil },
		},
		{
			Path: "/root/empty", Size: 0,
			Open: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("")), nil },
		},
	}
	for range 2 {
		if err := e.Upload(ctx, "aaaaaaaaaaaaaaaaaaaa", "root", files); err != nil {
			t.Fatal(err)
		}
	}
	if opened.Load() != 2 || closed.Load() != 2 {
		t.Errorf("Open called %d times, Close called %d times, want 2 each", opened.Load(), closed.Load())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.uploads) != 6 {
		t.Fatalf("uploads = %d, want 6", len(f.uploads))
	}
	for i, got := range f.uploads {
		want := []string{payload, "small", ""}[i%3]
		if got.path != files[i%3].Path || got.user != "root" || string(got.body) != want {
			t.Errorf("upload %d path=%q user=%q bytes=%d, want path=%q root bytes=%d",
				i, got.path, got.user, len(got.body), files[i%3].Path, len(want))
		}
	}
}

type trackedUploadReader struct {
	io.ReadCloser
	closed *atomic.Int32
}

func (r *trackedUploadReader) Close() error {
	r.closed.Add(1)
	return r.ReadCloser.Close()
}

func TestUploadLifecycleErrors(t *testing.T) {
	f, e := newFakeE2B(t)
	f.add("aaaaaaaaaaaaaaaaaaaa", "paused", nil)
	files := []engine.File{{
		Path: "/root/file", Size: 1,
		Open: func() (io.ReadCloser, error) {
			t.Error("Upload opened a file for a machine that is not running")
			return io.NopCloser(strings.NewReader("x")), nil
		},
	}}
	for id, want := range map[string]error{
		"aaaaaaaaaaaaaaaaaaaa": engine.ErrNotRunning, "missing": engine.ErrNotFound,
	} {
		if err := e.Upload(t.Context(), id, "root", files); !errors.Is(err, want) {
			t.Errorf("Upload(%s) = %v, want %v", id, err, want)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.requests {
		if r.Method != http.MethodGet {
			t.Errorf("Upload changed machine state with %s %s", r.Method, r.URL)
		}
	}
}

func TestUploadConnectionCutAndRetry(t *testing.T) {
	f, e := newFakeE2B(t)
	f.add("aaaaaaaaaaaaaaaaaaaa", "running", nil)
	f.envd = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prefix := make([]byte, 1024)
		if _, err := io.ReadFull(r.Body, prefix); err != nil {
			t.Error(err)
		}
		conn, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	})
	payload := strings.Repeat("x", 8<<20)
	var opened, closed atomic.Int32
	files := []engine.File{{
		Path: "/root/file", Size: int64(len(payload)),
		Open: func() (io.ReadCloser, error) {
			opened.Add(1)
			return &trackedUploadReader{ReadCloser: io.NopCloser(strings.NewReader(payload)), closed: &closed}, nil
		},
	}}
	if err := e.Upload(t.Context(), "aaaaaaaaaaaaaaaaaaaa", "root", files); !errors.Is(err, engine.ErrConnectionLost) {
		t.Fatalf("Upload cut mid-body = %v, want ErrConnectionLost", err)
	}
	f.mu.Lock()
	f.envd = nil
	f.mu.Unlock()
	if err := e.Upload(t.Context(), "aaaaaaaaaaaaaaaaaaaa", "root", files); err != nil {
		t.Fatal(err)
	}
	if opened.Load() != 2 || closed.Load() != 2 {
		t.Errorf("Open called %d times, Close called %d times, want 2 each", opened.Load(), closed.Load())
	}
}

func TestUploadRejectsHTTPFailuresAndRedirects(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusForbidden, http.StatusRequestEntityTooLarge, http.StatusBadGateway} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f, e := newFakeE2B(t)
			f.add("aaaaaaaaaaaaaaaaaaaa", "running", nil)
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				t.Error("Upload followed a redirect")
			}))
			defer target.Close()
			f.envd = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Location", target.URL)
				http.Error(w, "upload refused", status)
			})
			err := e.Upload(t.Context(), "aaaaaaaaaaaaaaaaaaaa", "root", []engine.File{{
				Path: "/root/file", Size: 1,
				Open: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("x")), nil },
			}})
			if err == nil || !strings.Contains(err.Error(), fmt.Sprint(status)) {
				t.Errorf("Upload = %v, want HTTP status %d", err, status)
			}
		})
	}
}

func TestUploadSourceErrors(t *testing.T) {
	for _, where := range []string{"open", "read", "close"} {
		t.Run(where, func(t *testing.T) {
			f, e := newFakeE2B(t)
			f.add("aaaaaaaaaaaaaaaaaaaa", "running", nil)
			f.envd = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.WriteHeader(http.StatusOK)
			})
			boom := errors.New("source " + where + " failed")
			var closed atomic.Int32
			err := e.Upload(t.Context(), "aaaaaaaaaaaaaaaaaaaa", "root", []engine.File{{
				Path: "/root/file", Size: 1,
				Open: func() (io.ReadCloser, error) {
					if where == "open" {
						return nil, boom
					}
					return &failingUploadReader{err: boom, where: where, closed: &closed}, nil
				},
			}})
			if !errors.Is(err, boom) {
				t.Errorf("Upload = %v, want %v", err, boom)
			}
			if where != "open" && closed.Load() != 1 {
				t.Errorf("Close called %d times, want 1", closed.Load())
			}
		})
	}
}

type failingUploadReader struct {
	err    error
	where  string
	closed *atomic.Int32
}

func (r *failingUploadReader) Read([]byte) (int, error) {
	if r.where == "read" {
		return 0, r.err
	}
	return 0, io.EOF
}

func (r *failingUploadReader) Close() error {
	r.closed.Add(1)
	if r.where == "close" {
		return r.err
	}
	return nil
}

func TestUploadMissingToken(t *testing.T) {
	f, e := newFakeE2B(t)
	f.add("aaaaaaaaaaaaaaaaaaaa", "running", nil)
	delete(f.sandboxes["aaaaaaaaaaaaaaaaaaaa"], "envdAccessToken")
	err := e.Upload(t.Context(), "aaaaaaaaaaaaaaaaaaaa", "root", []engine.File{{
		Path: "/root/file",
		Open: func() (io.ReadCloser, error) {
			t.Error("Upload opened a file without an access token")
			return io.NopCloser(strings.NewReader("")), nil
		},
	}})
	if err == nil || !strings.Contains(err.Error(), "no envd access token") {
		t.Errorf("Upload = %v, want a missing-token error", err)
	}
}

func TestUploadResponseCut(t *testing.T) {
	f, e := newFakeE2B(t)
	f.add("aaaaaaaaaaaaaaaaaaaa", "running", nil)
	f.envd = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("x"))
	})
	err := e.Upload(t.Context(), "aaaaaaaaaaaaaaaaaaaa", "root", []engine.File{{
		Path: "/root/file", Size: 1,
		Open: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("x")), nil },
	}})
	if !errors.Is(err, engine.ErrConnectionLost) {
		t.Errorf("Upload with a cut response = %v, want ErrConnectionLost", err)
	}
}

func TestUploadCancellationClosesSource(t *testing.T) {
	f, e := newFakeE2B(t)
	f.add("aaaaaaaaaaaaaaaaaaaa", "running", nil)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f.envd = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.CopyN(io.Discard, r.Body, 1024); err != nil {
			t.Error(err)
		}
		cancel()
		_, _ = io.Copy(io.Discard, r.Body)
	})
	var closed atomic.Int32
	r, w := io.Pipe()
	defer w.Close()
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		_, _ = io.WriteString(w, strings.Repeat("x", 4096))
	}()
	done := make(chan error, 1)
	go func() {
		done <- e.Upload(ctx, "aaaaaaaaaaaaaaaaaaaa", "root", []engine.File{{
			Path: "/root/file", Size: 8192,
			Open: func() (io.ReadCloser, error) {
				return &trackedUploadReader{ReadCloser: r, closed: &closed}, nil
			},
		}})
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Upload = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled Upload did not close its blocked source")
	}
	_ = r.Close()
	<-sent
	if closed.Load() != 1 {
		t.Errorf("Close called %d times, want 1", closed.Load())
	}
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
	switch p.GetCmd() {
	case "flood":
		chunk := bytes.Repeat([]byte("x"), 1<<20)
		for range 10 {
			if err := stream.Send(stdout(chunk)); err != nil {
				return err
			}
		}
		return stream.Send(exited(0))
	case "true":
		return stream.Send(exited(0))
	case "noend":
		return stream.Send(stdout([]byte("partial")))
	case "fail":
		var code connect.Code
		if err := code.UnmarshalText([]byte(p.GetArgs()[0])); err != nil {
			return err
		}
		if err := stream.Send(stdout([]byte("partial"))); err != nil {
			return err
		}
		return connect.NewError(code, errors.New("envd says no"))
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

func stdout(b []byte) *process.StartResponse {
	return &process.StartResponse{Event: &process.ProcessEvent{Event: &process.ProcessEvent_Data{Data: &process.ProcessEvent_DataEvent{Output: &process.ProcessEvent_DataEvent_Stdout{Stdout: b}}}}}
}

func exited(code int32) *process.StartResponse {
	return &process.StartResponse{Event: &process.ProcessEvent{Event: &process.ProcessEvent_End{End: &process.ProcessEvent_EndEvent{ExitCode: code, Exited: true}}}}
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

func TestMachineCPUs(t *testing.T) {
	f, e := newFakeE2B(t)
	f.add("aaaaaaaaaaaaaaaaaaaa", "running", nil)
	f.add("bbbbbbbbbbbbbbbbbbbb", "paused", nil)
	f.sandboxes["bbbbbbbbbbbbbbbbbbbb"]["cpuCount"] = 4
	for id, want := range map[string]int{"aaaaaaaaaaaaaaaaaaaa": 2, "bbbbbbbbbbbbbbbbbbbb": 4} {
		m, err := e.Get(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if m.CPUs != want {
			t.Errorf("Get(%s).CPUs = %d, want %d", id, m.CPUs, want)
		}
	}
	ms, err := e.List(t.Context(), engine.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 || ms[0].CPUs != 2 || ms[1].CPUs != 4 {
		t.Errorf("List CPUs = %+v, want 2 and 4", ms)
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

// TestPlacementRefusals checks that a resume E2B turns down for now says so with
// engine.ErrNotPlaced, and one E2B tried and failed with engine.ErrStartFailed, for Resume and
// for the resume DialWebSocket does. The error keeps E2B's status and message. Other refusals
// wrap neither.
func TestPlacementRefusals(t *testing.T) {
	f, e := newFakeE2B(t)
	f.add("aaaaaaaaaaaaaaaaaaaa", "paused", nil)
	cases := []struct {
		status    int
		errorCode string
		message   string
		want      error
	}{
		{503, "sandbox_capacity_unavailable", "Failed to place sandbox: not enough capacity for the requested resources right now, please retry shortly", engine.ErrNotPlaced},
		{503, "sandbox_no_compatible_node", "Failed to place sandbox: no compatible node for this template's requirements", engine.ErrNotPlaced},
		{504, "sandbox_placement_timeout", "Failed to place sandbox: placement timed out after 3 attempt(s), please retry", engine.ErrNotPlaced},
		{500, "sandbox_create_failed", "Failed to place sandbox: sandbox creation failed on 1 node(s), please retry; if the problem persists, contact us", engine.ErrStartFailed},
		{503, "sandbox_feature_unsupported", "Failed to place sandbox: no available orchestrator at version 0.2.0 or above for a feature the request asked for", nil},
		{500, "internal_server_error", "Failed to place sandbox", nil},
		{503, "", "Failed to place sandbox: not enough capacity for the requested resources right now, please retry shortly", nil},
	}
	for _, c := range cases {
		t.Run(cmp.Or(c.errorCode, "no error code"), func(t *testing.T) {
			f.refuseConnect(c.status, c.errorCode, c.message)
			f.refuseConnect(c.status, c.errorCode, c.message)
			for op, err := range map[string]error{
				"Resume":        errOf(e.Resume(t.Context(), "aaaaaaaaaaaaaaaaaaaa", time.Minute)),
				"DialWebSocket": errOf(e.DialWebSocket(t.Context(), "aaaaaaaaaaaaaaaaaaaa", 8081, time.Minute)),
			} {
				for _, sentinel := range []error{engine.ErrNotPlaced, engine.ErrStartFailed} {
					if got := errors.Is(err, sentinel); got != (sentinel == c.want) {
						t.Errorf("%s = %v, which wraps %v: %t, want %t", op, err, sentinel, got, !got)
					}
				}
				if want := fmt.Sprintf("E2B returned %d: %s", c.status, c.message); err == nil || !strings.Contains(err.Error(), want) {
					t.Errorf("%s = %v, want it to say %q", op, err, want)
				}
			}
		})
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
	if err == nil || !strings.Contains(err.Error(), "more than") || errors.Is(err, engine.ErrConnectionLost) {
		t.Errorf("Exec(flood) = %v, want only an error about too much output", err)
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
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	entry := func(msg string) map[string]any {
		return map[string]any{"level": "info", "step": "3", "message": msg, "timestamp": at}
	}
	f.statuses = []map[string]any{
		{"templateID": "tpl1", "buildID": "build1", "status": "waiting", "logs": []string{}, "logEntries": []any{}},
		{"templateID": "tpl1", "buildID": "build1", "status": "building", "logs": []string{}, "logEntries": []any{entry("one"), entry("two")}},
		{"templateID": "tpl1", "buildID": "build1", "status": "ready", "logs": []string{}, "logEntries": []any{entry("three")}},
		{"templateID": "tpl1", "buildID": "build1", "status": "ready", "logs": []string{}, "logEntries": []any{}},
	}
	var logs []engine.LogEntry
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
	}, func(entry engine.LogEntry) { logs = append(logs, entry) })
	if err != nil {
		t.Fatal(err)
	}
	if id != "tpl1" {
		t.Errorf("BuildTemplate = %q, want tpl1", id)
	}
	want := []engine.LogEntry{
		{Time: at, Level: "info", Step: "3", Message: "one"},
		{Time: at, Level: "info", Step: "3", Message: "two"},
		{Time: at, Level: "info", Step: "3", Message: "three"},
	}
	if !slices.Equal(logs, want) {
		t.Errorf("logs = %+v, want %+v", logs, want)
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
func TestBuildTemplateKeepsFullMessagesAndElidesFailureReason(t *testing.T) {
	fastPolls(t)
	f, e := newFakeE2B(t)
	cmd := "RUN put /x 755 '" + strings.Repeat("QUFB", 5000) + "'; /x"
	f.statuses = []map[string]any{
		{"templateID": "tpl1", "buildID": "build1", "status": "error", "logs": []string{},
			"logEntries": []any{map[string]any{"timestamp": "2026-01-01T00:00:00Z", "level": "info", "message": cmd}}},
		{"templateID": "tpl1", "buildID": "build1", "status": "error", "logs": []string{}, "logEntries": []any{},
			"reason": map[string]any{"message": "failed to run command '" + cmd + "': exit status 1", "step": "1"}},
	}
	var logs []engine.LogEntry
	_, err := e.BuildTemplate(t.Context(), engine.TemplateSpec{Name: "hib-dev", FromImage: "ubuntu:24.04"},
		func(entry engine.LogEntry) { logs = append(logs, entry) })
	if err == nil || len(err.Error()) > 1000 || !strings.Contains(err.Error(), "failed at step 1: failed to run command 'RUN put /x") ||
		!strings.HasSuffix(err.Error(), "'; /x': exit status 1") || !strings.Contains(err.Error(), "bytes) …") {
		t.Errorf("BuildTemplate = %v", err)
	}
	if len(logs) != 1 || logs[0].Message != cmd {
		t.Errorf("logs did not preserve the full command")
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
