// Package e2b implements engine.Engine on a self-hosted E2B (spec D4).
//
// Machine lifecycle calls go to E2B's REST API. Exec goes to envd, the agent inside each
// machine, through E2B's client-proxy: requests to a local or IP host are routed by the
// E2b-Sandbox-Id and E2b-Sandbox-Port headers, and envd checks X-Access-Token.
package e2b

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"connectrpc.com/connect"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/e2b/e2bapi"
	"github.com/solev/hibernaut/internal/engine/e2b/envdpb/process"
	"github.com/solev/hibernaut/internal/engine/e2b/envdpb/process/processconnect"
)

// envdPort is where envd listens inside every machine.
const envdPort = 49983

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
	api  *e2bapi.ClientWithResponses
	envd processconnect.ProcessClient
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
	return &Engine{api: api, envd: processconnect.NewProcessClient(hc, cfg.SandboxURL)}, nil
}

func (e *Engine) Create(ctx context.Context, opts engine.CreateOptions) (engine.Machine, error) {
	body := e2bapi.NewSandboxV2{
		TemplateID: opts.Template,
		// A machine whose timeout runs out pauses, memory included, instead of being deleted.
		AutoPause: ptr(true),
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
	var body e2bapi.ConnectSandboxV2
	if timeout > 0 {
		body.Timeout = ptr(seconds(timeout))
	}
	resp, err := e.api.PostV2SandboxesSandboxIDConnectWithResponse(ctx, id, body)
	if err != nil {
		return engine.Machine{}, fmt.Errorf("resume %s: %w", id, err)
	}
	// 200: it was running and its timeout was extended. 201: it was resumed.
	if resp.StatusCode() != http.StatusOK && resp.StatusCode() != http.StatusCreated {
		return engine.Machine{}, apiError("resume", id, resp.StatusCode(), resp.Body)
	}
	return e.Get(ctx, id)
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
	// Checking first keeps Exec from waking a paused machine through the proxy.
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

// running returns the machine's details if it's running, and ErrNotRunning if it's paused.
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

// apiError turns an unexpected E2B response into an error, mapping 404 to ErrNotFound and 409
// (paused, or pausing or being deleted) to ErrNotRunning.
func apiError(op, id string, status int, body []byte) error {
	msg := string(body)
	var apiErr e2bapi.Error
	if json.Unmarshal(body, &apiErr) == nil && apiErr.Message != "" {
		msg = apiErr.Message
	}
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

// seconds rounds d up to whole seconds, at least one.
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
