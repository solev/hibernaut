// Package e2b implements engine.Engine on a self-hosted E2B (spec D4).
//
// Requests to a local or IP host are routed by the E2b-Sandbox-Id and E2b-Sandbox-Port headers.
// envd checks X-Access-Token. Other ports check e2b-traffic-access-token, because machines are
// created with public traffic off.
package e2b

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/coder/websocket"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/e2b/e2bapi"
	"github.com/solev/hibernaut/internal/engine/e2b/envdpb/process"
	"github.com/solev/hibernaut/internal/engine/e2b/envdpb/process/processconnect"
)

const (
	envdPort = 49983
	wsChunk  = 32 << 10
)

// BuildPollInterval is how often BuildTemplate asks E2B how a build is going.
var BuildPollInterval = time.Second

// Config says where E2B is.
type Config struct {
	// APIURL is E2B's REST API, e.g. http://127.0.0.1:3000.
	APIURL string
	// SandboxURL is E2B's client-proxy, e.g. http://127.0.0.1:3002. Its host must be an IP or
	// localhost, so the proxy routes by header.
	SandboxURL string
	APIKey     string
	// HTTPClient defaults to a client without a timeout; calls are bounded by their contexts.
	HTTPClient *http.Client
}

// Engine talks to one E2B installation.
type Engine struct {
	api        *e2bapi.ClientWithResponses
	envd       processconnect.ProcessClient
	hc         *http.Client
	guestHC    *http.Client
	sandboxURL string
}

var _ engine.Engine = (*Engine)(nil)

// New returns an Engine for cfg.
func New(cfg Config) (*Engine, error) {
	if cfg.APIURL == "" || cfg.SandboxURL == "" || cfg.APIKey == "" {
		return nil, errors.New("e2b: APIURL, SandboxURL and APIKey are required")
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	api, err := e2bapi.NewClientWithResponses(cfg.APIURL,
		e2bapi.WithHTTPClient(hc),
		e2bapi.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
			req.Header.Set("X-API-Key", cfg.APIKey)
			return nil
		}),
	)
	if err != nil {
		return nil, err
	}

	guestHC := *hc
	guestHC.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Engine{
		api:        api,
		envd:       processconnect.NewProcessClient(&guestHC, cfg.SandboxURL),
		hc:         hc,
		guestHC:    &guestHC,
		sandboxURL: cfg.SandboxURL,
	}, nil
}

func (e *Engine) Create(ctx context.Context, opts engine.CreateOptions) (engine.Machine, error) {
	body := e2bapi.NewSandboxV2{
		TemplateID:      opts.Template,
		AutoPause:       ptr(true),
		AutoPauseMemory: ptr(true),
		AutoResume:      &e2bapi.SandboxAutoResumeConfig{Enabled: false},
		Network:         &e2bapi.SandboxNetworkConfig{AllowPublicTraffic: ptr(false)},
	}
	if opts.Timeout > 0 {
		body.Timeout = ptr(seconds(opts.Timeout))
	}
	if len(opts.Metadata) > 0 {
		md := e2bapi.SandboxMetadata(opts.Metadata)
		body.Metadata = &md
	}
	if len(opts.Env) > 0 {
		env := e2bapi.EnvVars(opts.Env)
		body.EnvVars = &env
	}
	resp, err := e.api.PostV2SandboxesWithResponse(ctx, body)
	if err != nil {
		return engine.Machine{}, fmt.Errorf("create: %w", err)
	}
	if resp.StatusCode() == http.StatusNotFound {
		return engine.Machine{}, fmt.Errorf("create from %s: %w", opts.Template, engine.ErrNoTemplate)
	}
	if resp.JSON201 == nil {
		return engine.Machine{}, apiError("create", opts.Template, resp.StatusCode(), resp.Body)
	}
	return e.Get(ctx, resp.JSON201.SandboxID)
}

func (e *Engine) Get(ctx context.Context, id string) (engine.Machine, error) {
	d, err := e.detail(ctx, id)
	if err != nil {
		return engine.Machine{}, err
	}
	return engine.Machine{
		ID:        d.SandboxID,
		Template:  d.TemplateID,
		State:     engine.State(d.State),
		StartedAt: d.StartedAt,
		EndAt:     d.EndAt,
		MemoryMB:  int(d.MemoryMB),
		Metadata:  metadata(d.Metadata),
	}, nil
}

func (e *Engine) detail(ctx context.Context, id string) (*e2bapi.SandboxDetail, error) {
	resp, err := e.api.GetSandboxesSandboxIDWithResponse(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", id, err)
	}
	if resp.JSON200 == nil {
		return nil, apiError("get", id, resp.StatusCode(), resp.Body)
	}
	return resp.JSON200, nil
}

func (e *Engine) List(ctx context.Context, opts engine.ListOptions) ([]engine.Machine, error) {
	params := &e2bapi.GetV2SandboxesParams{Limit: ptr(e2bapi.PaginationLimit(100))}
	if len(opts.States) > 0 {
		states := make([]e2bapi.SandboxState, len(opts.States))
		for i, s := range opts.States {
			states[i] = e2bapi.SandboxState(s)
		}
		params.State = &states
	}
	if len(opts.Metadata) > 0 {
		// E2B wants the pairs as one URL-encoded query string ("a=1&b=2").
		q := url.Values{}
		for k, v := range opts.Metadata {
			q.Set(k, v)
		}
		params.Metadata = ptr(q.Encode())
	}

	var out []engine.Machine
	for {
		resp, err := e.api.GetV2SandboxesWithResponse(ctx, params)
		if err != nil {
			return nil, fmt.Errorf("list: %w", err)
		}
		if resp.JSON200 == nil {
			return nil, apiError("list", "", resp.StatusCode(), resp.Body)
		}
		for _, s := range *resp.JSON200 {
			m := engine.Machine{
				ID:        s.SandboxID,
				Template:  s.TemplateID,
				State:     engine.State(s.State),
				StartedAt: s.StartedAt,
				EndAt:     s.EndAt,
				MemoryMB:  int(s.MemoryMB),
				Metadata:  metadata(s.Metadata),
			}
			// A machine can move between the running and paused pages while we read them.
			if !slices.ContainsFunc(out, func(o engine.Machine) bool { return o.ID == m.ID }) {
				out = append(out, m)
			}
		}
		next := resp.HTTPResponse.Header.Get("X-Next-Token")
		if next == "" {
			return out, nil
		}
		params.NextToken = &next
	}
}

func (e *Engine) Pause(ctx context.Context, id string) error {
	resp, err := e.api.PostSandboxesSandboxIDPauseWithResponse(ctx, id, e2bapi.SandboxPauseRequest{})
	if err != nil {
		return fmt.Errorf("pause %s: %w", id, err)
	}
	if resp.StatusCode() == http.StatusNoContent {
		return nil
	}
	return apiError("pause", id, resp.StatusCode(), resp.Body)
}

func (e *Engine) Resume(ctx context.Context, id string, timeout time.Duration) (engine.Machine, error) {
	if _, err := e.connect(ctx, id, timeout); err != nil {
		return engine.Machine{}, err
	}
	return e.Get(ctx, id)
}

// E2B waits out a pause in progress, then resumes.
func (e *Engine) connect(ctx context.Context, id string, timeout time.Duration) (*e2bapi.Sandbox, error) {
	var body e2bapi.ConnectSandboxV2
	if timeout > 0 {
		body.Timeout = ptr(seconds(timeout))
	}
	resp, err := e.api.PostV2SandboxesSandboxIDConnectWithResponse(ctx, id, body)
	if err != nil {
		return nil, fmt.Errorf("resume %s: %w", id, err)
	}
	// 200: it was running and its timeout was extended. 201: it was resumed.
	switch {
	case resp.JSON200 != nil:
		return resp.JSON200, nil
	case resp.JSON201 != nil:
		return resp.JSON201, nil
	}
	return nil, apiError("resume", id, resp.StatusCode(), resp.Body)
}

func (e *Engine) Kill(ctx context.Context, id string) error {
	resp, err := e.api.DeleteSandboxesSandboxIDWithResponse(ctx, id)
	if err != nil {
		return fmt.Errorf("kill %s: %w", id, err)
	}
	if resp.StatusCode() == http.StatusNoContent {
		return nil
	}
	return apiError("kill", id, resp.StatusCode(), resp.Body)
}

func (e *Engine) SetTimeout(ctx context.Context, id string, timeout time.Duration) error {
	resp, err := e.api.PostSandboxesSandboxIDTimeoutWithResponse(ctx, id, e2bapi.SandboxTimeoutRequest{Timeout: seconds(timeout)})
	if err != nil {
		return fmt.Errorf("set timeout %s: %w", id, err)
	}
	switch resp.StatusCode() {
	case http.StatusNoContent:
		return nil
	case http.StatusNotFound:
		// E2B answers 404 for paused machines too: its timeouts live only on running ones.
		if _, err := e.running(ctx, id); err != nil {
			return fmt.Errorf("set timeout: %w", err)
		}
	}
	return apiError("set timeout", id, resp.StatusCode(), resp.Body)
}

func (e *Engine) Metrics(ctx context.Context, id string) (engine.Metrics, error) {
	if _, err := e.running(ctx, id); err != nil {
		return engine.Metrics{}, fmt.Errorf("metrics: %w", err)
	}
	resp, err := e.api.GetSandboxesSandboxIDMetricsWithResponse(ctx, id, &e2bapi.GetSandboxesSandboxIDMetricsParams{})
	if err != nil {
		return engine.Metrics{}, fmt.Errorf("metrics %s: %w", id, err)
	}
	if resp.JSON200 == nil {
		return engine.Metrics{}, apiError("metrics", id, resp.StatusCode(), resp.Body)
	}
	samples := *resp.JSON200
	if len(samples) == 0 {
		return engine.Metrics{}, fmt.Errorf("metrics %s: %w", id, engine.ErrNoMetrics)
	}
	s := slices.MaxFunc(samples, func(a, b e2bapi.SandboxMetric) int { return a.Timestamp.Compare(b.Timestamp) })
	return engine.Metrics{
		At:         s.Timestamp,
		CPUCount:   int(s.CpuCount),
		CPUUsedPct: float64(s.CpuUsedPct),
		MemUsed:    s.MemUsed,
		MemTotal:   s.MemTotal,
		DiskUsed:   s.DiskUsed,
		DiskTotal:  s.DiskTotal,
	}, nil
}

func (e *Engine) Exec(ctx context.Context, id string, req engine.ExecRequest) (engine.ExecResult, error) {
	d, err := e.running(ctx, id)
	if err != nil {
		return engine.ExecResult{}, fmt.Errorf("exec: %w", err)
	}
	if d.EnvdAccessToken == nil {
		return engine.ExecResult{}, fmt.Errorf("exec %s: E2B returned no envd access token", id)
	}

	cfg := &process.ProcessConfig{Cmd: req.Cmd, Args: req.Args, Envs: req.Env}
	if req.Dir != "" {
		cfg.Cwd = &req.Dir
	}
	r := connect.NewRequest(&process.StartRequest{Process: cfg})
	r.Header().Set("E2b-Sandbox-Id", id)
	r.Header().Set("E2b-Sandbox-Port", strconv.Itoa(envdPort))
	r.Header().Set("X-Access-Token", *d.EnvdAccessToken)
	if req.User != "" {
		// envd takes the user to run as from the Basic auth username; the password is unused.
		basic := http.Request{Header: http.Header{}}
		basic.SetBasicAuth(req.User, "")
		r.Header().Set("Authorization", basic.Header.Get("Authorization"))
	}

	stream, err := e.envd.Start(ctx, r)
	if err != nil {
		return engine.ExecResult{}, fmt.Errorf("exec %s: %w", id, err)
	}
	defer stream.Close()

	var res engine.ExecResult
	ended := false
	for stream.Receive() {
		switch ev := stream.Msg().GetEvent().GetEvent().(type) {
		case *process.ProcessEvent_Data:
			res.Stdout = append(res.Stdout, ev.Data.GetStdout()...)
			res.Stderr = append(res.Stderr, ev.Data.GetStderr()...)
			if len(res.Stdout) > engine.MaxExecOutput || len(res.Stderr) > engine.MaxExecOutput {
				return engine.ExecResult{}, fmt.Errorf("exec %s: %s wrote more than %d bytes", id, req.Cmd, engine.MaxExecOutput)
			}
		case *process.ProcessEvent_End:
			res.ExitCode = int(ev.End.GetExitCode())
			ended = true
		}
	}
	if err := stream.Err(); err != nil {
		return engine.ExecResult{}, fmt.Errorf("exec %s: %w", id, err)
	}
	if !ended {
		return engine.ExecResult{}, fmt.Errorf("exec %s: stream closed before the command exited", id)
	}
	return res, nil
}

func (e *Engine) DialWebSocket(ctx context.Context, id string, port int, timeout time.Duration) (net.Conn, error) {
	sbx, err := e.connect(ctx, id, timeout)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	h := http.Header{}
	h.Set("E2b-Sandbox-Id", id)
	h.Set("E2b-Sandbox-Port", strconv.Itoa(port))
	// No token means the machine takes public traffic.
	if sbx.TrafficAccessToken != nil {
		h.Set("E2b-Traffic-Access-Token", *sbx.TrafficAccessToken)
	}
	c, resp, err := websocket.Dial(ctx, e.sandboxURL, &websocket.DialOptions{HTTPClient: e.guestHC, HTTPHeader: h})
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("dial %s port %d: E2B answered %s: %w", id, port, resp.Status, err)
		}
		return nil, fmt.Errorf("dial %s port %d: %w", id, port, err)
	}
	return chunkedConn{websocket.NetConn(context.Background(), c, websocket.MessageBinary)}, nil
}

type chunkedConn struct{ net.Conn }

func (c chunkedConn) Write(p []byte) (int, error) {
	n := 0
	for len(p) > 0 {
		k := min(len(p), wsChunk)
		w, err := c.Conn.Write(p[:k])
		n += w
		if err != nil {
			return n, err
		}
		p = p[k:]
	}
	return n, nil
}

func (e *Engine) BuildTemplate(ctx context.Context, spec engine.TemplateSpec, logs io.Writer) (string, error) {
	if spec.Name == "" || spec.FromImage == "" {
		return "", errors.New("build template: Name and FromImage are required")
	}
	if logs == nil {
		logs = io.Discard
	}
	req := e2bapi.TemplateBuildRequestV3{Name: &spec.Name}
	if spec.CPUs > 0 {
		req.CpuCount = ptr(int32(spec.CPUs))
	}
	if spec.MemoryMB > 0 {
		req.MemoryMB = ptr(int32(spec.MemoryMB))
	}
	if spec.MinFreeDiskMB > 0 {
		req.MinFreeDiskMb = ptr(int32(spec.MinFreeDiskMB))
	}
	resp, err := e.api.PostV3TemplatesWithResponse(ctx, req)
	if err != nil {
		return "", fmt.Errorf("build template %s: %w", spec.Name, err)
	}
	if resp.JSON202 == nil {
		return "", apiError("build template", spec.Name, resp.StatusCode(), resp.Body)
	}
	templateID, buildID := resp.JSON202.TemplateID, resp.JSON202.BuildID

	steps := make([]e2bapi.TemplateStep, len(spec.Steps))
	for i, s := range spec.Steps {
		// E2B runs RUN steps as [command, user], the user defaulting to the template's.
		args := []string{s.Cmd}
		if s.User != "" {
			args = append(args, s.User)
		}
		steps[i] = e2bapi.TemplateStep{Type: "RUN", Args: &args}
	}
	start := e2bapi.TemplateBuildStartV2{FromImage: &spec.FromImage, Steps: &steps, Force: ptr(spec.Force)}
	if spec.StartCmd != "" {
		start.StartCmd = &spec.StartCmd
	}
	if spec.ReadyCmd != "" {
		start.ReadyCmd = &spec.ReadyCmd
	}
	sresp, err := e.api.PostV2TemplatesTemplateIDBuildsBuildIDWithResponse(ctx, templateID, buildID, start)
	if err != nil {
		return "", fmt.Errorf("build template %s: %w", spec.Name, err)
	}
	if sresp.StatusCode() != http.StatusAccepted {
		return "", apiError("build template", spec.Name, sresp.StatusCode(), sresp.Body)
	}

	var offset int32
	for {
		st, err := e.api.GetTemplatesTemplateIDBuildsBuildIDStatusWithResponse(ctx, templateID, buildID,
			&e2bapi.GetTemplatesTemplateIDBuildsBuildIDStatusParams{LogsOffset: &offset})
		if err != nil {
			return "", fmt.Errorf("build template %s: %w", spec.Name, err)
		}
		if st.JSON200 == nil {
			return "", apiError("build template", spec.Name, st.StatusCode(), st.Body)
		}
		info := st.JSON200
		for _, l := range info.LogEntries {
			fmt.Fprintln(logs, elide(l.Message))
		}
		offset += int32(len(info.LogEntries))
		done := info.Status == e2bapi.TemplateBuildStatusReady || info.Status == e2bapi.TemplateBuildStatusError
		// A finished build can still have logs past this page.
		if done && len(info.LogEntries) == 0 {
			if info.Status == e2bapi.TemplateBuildStatusReady {
				return templateID, nil
			}
			return "", buildError(spec.Name, info.Reason)
		}
		if done {
			continue
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("build template %s: %w", spec.Name, ctx.Err())
		case <-time.After(BuildPollInterval):
		}
	}
}

func buildError(name string, r *e2bapi.BuildStatusReason) error {
	if r == nil {
		return fmt.Errorf("build template %s failed", name)
	}
	if r.Step != nil {
		return fmt.Errorf("build template %s failed at step %s: %s", name, *r.Step, elide(r.Message))
	}
	return fmt.Errorf("build template %s failed: %s", name, elide(r.Message))
}

// E2B quotes a step's whole command in logs and errors.
func elide(s string) string {
	const keep = 300
	if len(s) <= 3*keep {
		return s
	}
	return fmt.Sprintf("%s … (%d bytes) … %s", strings.ToValidUTF8(s[:keep], ""), len(s)-2*keep,
		strings.ToValidUTF8(s[len(s)-keep:], ""))
}

func (e *Engine) running(ctx context.Context, id string) (*e2bapi.SandboxDetail, error) {
	d, err := e.detail(ctx, id)
	if err != nil {
		return nil, err
	}
	if d.State != e2bapi.Running {
		return nil, fmt.Errorf("%s is %s: %w", id, d.State, engine.ErrNotRunning)
	}
	return d, nil
}

func apiError(op, id string, status int, body []byte) error {
	msg := apiMessage(body)
	subject := op
	if id != "" {
		subject += " " + id
	}
	switch status {
	case http.StatusNotFound:
		return fmt.Errorf("%s: %s: %w", subject, msg, engine.ErrNotFound)
	case http.StatusConflict:
		return fmt.Errorf("%s: %s: %w", subject, msg, engine.ErrNotRunning)
	}
	return fmt.Errorf("%s: E2B returned %d: %s", subject, status, msg)
}

func apiMessage(body []byte) string {
	var apiErr e2bapi.Error
	if json.Unmarshal(body, &apiErr) == nil && apiErr.Message != "" {
		return apiErr.Message
	}
	return string(body)
}

func seconds(d time.Duration) int32 {
	s := math.Ceil(d.Seconds())
	return int32(min(max(s, 1), math.MaxInt32))
}

func metadata(md *e2bapi.SandboxMetadata) map[string]string {
	if md == nil {
		return nil
	}
	return map[string]string(*md)
}

func ptr[T any](v T) *T { return &v }
