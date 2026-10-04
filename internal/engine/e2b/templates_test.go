package e2b_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/e2b"
)

type reply struct {
	status int
	body   any
	next   string
}

type replayE2B struct {
	t       *testing.T
	replies map[string]reply

	mu     sync.Mutex
	got    []string
	bodies map[string]string
}

func newReplayE2B(t *testing.T, replies map[string]reply) (*replayE2B, *e2b.Engine) {
	f := &replayE2B{t: t, replies: replies, bodies: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	eng, err := e2b.New(e2b.Config{APIURL: srv.URL, SandboxURL: srv.URL, APIKey: apiKey})
	if err != nil {
		t.Fatal(err)
	}
	return f, eng
}

func (f *replayE2B) serve(w http.ResponseWriter, r *http.Request) {
	key := r.Method + " " + r.URL.Path
	if r.URL.RawQuery != "" {
		key += "?" + r.URL.RawQuery
	}
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.got = append(f.got, key)
	f.bodies[key] = string(body)
	f.mu.Unlock()
	if r.Header.Get("X-API-Key") != apiKey {
		writeJSON(w, 401, map[string]any{"code": 401, "message": "bad key"})
		return
	}
	rep, ok := f.replies[key]
	if !ok {
		f.t.Errorf("unexpected request %s", key)
		writeJSON(w, 500, map[string]any{"code": 500, "message": "unexpected request"})
		return
	}
	if rep.next != "" {
		w.Header().Set("X-Next-Token", rep.next)
	}
	if rep.body == nil {
		w.WriteHeader(rep.status)
		return
	}
	writeJSON(w, rep.status, rep.body)
}

func (f *replayE2B) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.got)
}

func e2bError(status int, msg string) reply {
	return reply{status: status, body: map[string]any{"code": status, "message": msg}}
}

var (
	t0 = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	t1 = t0.Add(time.Hour)
	t2 = t0.Add(2 * time.Hour)
)

func build(id, status string, cpus, memoryMB int, created time.Time, finished *time.Time) map[string]any {
	b := map[string]any{
		"buildID": id, "status": status, "cpuCount": cpus, "memoryMB": memoryMB, "diskSizeMB": 11346,
		"envdVersion": "0.2.0", "createdAt": created, "updatedAt": created,
	}
	if finished != nil {
		b["finishedAt"] = *finished
	}
	return b
}

func templateDetail(id string, names []string, builds ...map[string]any) reply {
	return reply{status: 200, body: map[string]any{
		"templateID": id, "public": false, "aliases": []string{}, "names": names,
		"createdAt": t0, "updatedAt": t0, "lastSpawnedAt": nil, "spawnCount": 0, "builds": builds,
	}}
}

func aliasOf(id string) reply {
	return reply{status: 200, body: map[string]any{"templateID": id, "public": false}}
}

func sandboxDetail(id, state string) reply {
	return reply{status: 200, body: map[string]any{
		"sandboxID": id, "templateID": "base0000000000000000", "clientID": "c", "state": state,
		"startedAt": t0, "endAt": t1, "cpuCount": 2, "memoryMB": 2048, "diskSizeMB": 11346, "envdVersion": "0.2.0",
	}}
}

const (
	build1 = "11111111-1111-1111-1111-111111111111"
	build2 = "22222222-2222-2222-2222-222222222222"
	build3 = "33333333-3333-3333-3333-333333333333"
	origin = "origin00000000000000"
)

func TestSaveTemplate(t *testing.T) {
	f, e := newReplayE2B(t, map[string]reply{
		"POST /sandboxes/" + origin + "/snapshots": {status: 201, body: map[string]any{
			"names": []string{"local-dev-team/hibimg-web"}, "snapshotID": "local-dev-team/hibimg-web:default",
		}},
		"GET /templates/aliases/hibimg-web": aliasOf("snap1"),
		"GET /templates/snap1?limit=100": templateDetail("snap1", []string{"local-dev-team/hibimg-web"},
			build(build1, "ready", 2, 2048, t0, &t1)),
	})

	info, err := e.SaveTemplate(t.Context(), origin, "hibimg-web")
	if err != nil {
		t.Fatal(err)
	}
	want := engine.TemplateInfo{ID: "snap1", Alias: "hibimg-web", CPUs: 2, MemoryMB: 2048, Created: t1}
	if info != want {
		t.Errorf("SaveTemplate = %+v, want %+v", info, want)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(f.bodies["POST /sandboxes/"+origin+"/snapshots"]), &body); err != nil || body["name"] != "hibimg-web" {
		t.Errorf("snapshot request = %v (%v), want name hibimg-web", body, err)
	}
}

func TestSaveTemplateErrors(t *testing.T) {
	const notFound = `Sandbox "origin00000000000000" doesn't exist or you don't have access to it`
	const failed = "Error creating snapshot template"
	save := "POST /sandboxes/" + origin + "/snapshots"
	get := "GET /sandboxes/" + origin

	cases := []struct {
		name    string
		replies map[string]reply
		want    error
		notWant []error
		msg     string
	}{
		{
			name:    "paused",
			replies: map[string]reply{save: e2bError(404, notFound), get: sandboxDetail(origin, "paused")},
			want:    engine.ErrNotRunning,
			notWant: []error{engine.ErrNotFound, engine.ErrCheckpoint},
		},
		{
			name:    "missing",
			replies: map[string]reply{save: e2bError(404, notFound), get: e2bError(404, "sandbox \"origin00000000000000\" not found")},
			want:    engine.ErrNotFound,
			notWant: []error{engine.ErrNotRunning, engine.ErrCheckpoint},
		},
		{
			name:    "pausing",
			replies: map[string]reply{save: e2bError(409, "Sandbox 'origin00000000000000' cannot be snapshotted while in 'pausing' state")},
			want:    engine.ErrNotRunning,
			notWant: []error{engine.ErrCheckpoint, engine.ErrNotFound},
			msg:     "cannot be snapshotted while in 'pausing' state",
		},
		{
			name:    "being killed",
			replies: map[string]reply{save: e2bError(409, "Sandbox 'origin00000000000000' cannot be snapshotted while in 'killing' state")},
			want:    engine.ErrNotRunning,
			notWant: []error{engine.ErrCheckpoint, engine.ErrNotFound},
			msg:     "cannot be snapshotted while in 'killing' state",
		},
		{
			name:    "already being saved",
			replies: map[string]reply{save: e2bError(409, "Sandbox 'origin00000000000000' cannot be snapshotted while in 'snapshotting' state")},
			want:    engine.ErrNotRunning,
			notWant: []error{engine.ErrCheckpoint, engine.ErrNotFound},
			msg:     "cannot be snapshotted while in 'snapshotting' state",
		},
		{
			name:    "checkpoint failed, paused at its last snapshot",
			replies: map[string]reply{save: e2bError(500, failed), get: sandboxDetail(origin, "paused")},
			want:    engine.ErrCheckpoint,
			notWant: []error{engine.ErrNotRunning, engine.ErrNotFound},
			msg:     failed,
		},
		{
			name:    "checkpoint failed, lost with no snapshot",
			replies: map[string]reply{save: e2bError(500, failed), get: e2bError(404, "sandbox \"origin00000000000000\" not found")},
			want:    engine.ErrCheckpoint,
			notWant: []error{engine.ErrNotRunning, engine.ErrNotFound},
			msg:     failed,
		},
		{
			name:    "failed before the checkpoint",
			replies: map[string]reply{save: e2bError(500, failed), get: sandboxDetail(origin, "running")},
			notWant: []error{engine.ErrCheckpoint, engine.ErrNotRunning, engine.ErrNotFound},
			msg:     failed,
		},
		{
			name:    "failed, then reading the machine failed",
			replies: map[string]reply{save: e2bError(500, failed), get: e2bError(500, "Error getting sandbox")},
			notWant: []error{engine.ErrCheckpoint, engine.ErrNotRunning, engine.ErrNotFound},
			msg:     "Error getting sandbox",
		},
		{
			name: "node busy",
			replies: map[string]reply{save: e2bError(503,
				"Sandbox 'origin00000000000000' cannot be snapshotted right now because its node is busy, please retry")},
			notWant: []error{engine.ErrCheckpoint, engine.ErrNotRunning, engine.ErrNotFound},
			msg:     "its node is busy, please retry",
		},
		{
			name: "saved, then reading the template failed",
			replies: map[string]reply{
				save:                                {status: 201, body: map[string]any{"names": []string{"local-dev-team/hibimg-web"}, "snapshotID": "local-dev-team/hibimg-web:default"}},
				"GET /templates/aliases/hibimg-web": e2bError(500, "Error when getting template"),
			},
			notWant: []error{engine.ErrCheckpoint, engine.ErrNotRunning, engine.ErrNotFound},
			msg:     "Error when getting template",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, e := newReplayE2B(t, c.replies)
			_, err := e.SaveTemplate(t.Context(), origin, "hibimg-web")
			if err == nil {
				t.Fatal("SaveTemplate succeeded")
			}
			if c.want != nil && !errors.Is(err, c.want) {
				t.Errorf("SaveTemplate = %v, want %v", err, c.want)
			}
			for _, w := range c.notWant {
				if errors.Is(err, w) {
					t.Errorf("SaveTemplate = %v, want it not to wrap %v", err, w)
				}
			}
			if !strings.Contains(err.Error(), c.msg) {
				t.Errorf("SaveTemplate = %v, want E2B's message %q", err, c.msg)
			}
		})
	}
}

func TestTemplateTakesTheNewestReadyBuild(t *testing.T) {
	f, e := newReplayE2B(t, map[string]reply{
		"GET /templates/aliases/hibimg-web": aliasOf("snap1"),
		"GET /templates/snap1?limit=100": func() reply {
			r := templateDetail("snap1", []string{"local-dev-team/hibimg-web"}, build(build3, "error", 4, 4096, t2, &t2))
			r.next = "page2"
			return r
		}(),
		"GET /templates/snap1?nextToken=page2&limit=100": templateDetail("snap1", []string{"local-dev-team/hibimg-web"},
			build(build2, "ready", 4, 4096, t1, &t2),
			build(build1, "ready", 2, 2048, t0, &t1)),
	})

	info, err := e.Template(t.Context(), "hibimg-web")
	if err != nil {
		t.Fatal(err)
	}
	want := engine.TemplateInfo{ID: "snap1", Alias: "hibimg-web", CPUs: 4, MemoryMB: 4096, Created: t2}
	if info != want {
		t.Errorf("Template = %+v, want %+v from the newest ready build", info, want)
	}
	if got := f.requests(); len(got) != 3 {
		t.Errorf("requests = %v, want the alias and two pages of builds", got)
	}
}

func TestTemplateFindsAFailedSave(t *testing.T) {
	_, e := newReplayE2B(t, map[string]reply{
		"GET /templates/aliases/hibimg-web": aliasOf("snap1"),
		"GET /templates/snap1?limit=100": templateDetail("snap1", []string{"local-dev-team/hibimg-web"},
			build(build1, "error", 2, 2048, t0, &t1)),
	})

	info, err := e.Template(t.Context(), "hibimg-web")
	if err != nil {
		t.Fatal(err)
	}
	if want := (engine.TemplateInfo{ID: "snap1", Alias: "hibimg-web"}); info != want {
		t.Errorf("Template = %+v, want %+v: a failed save's alias, with no build to create from", info, want)
	}
}

func TestTemplateMissing(t *testing.T) {
	cases := map[string]map[string]reply{
		"no alias": {
			"GET /templates/aliases/hibimg-web": e2bError(404, "template 'hibimg-web' not found"),
		},
		"deleted after the alias lookup": {
			"GET /templates/aliases/hibimg-web": aliasOf("snap1"),
			"GET /templates/snap1?limit=100":    e2bError(404, "Template snap1 not found"),
		},
	}
	for name, replies := range cases {
		t.Run(name, func(t *testing.T) {
			_, e := newReplayE2B(t, replies)
			_, err := e.Template(t.Context(), "hibimg-web")
			if !errors.Is(err, engine.ErrNoTemplate) || errors.Is(err, engine.ErrNotFound) {
				t.Errorf("Template = %v, want ErrNoTemplate only", err)
			}
			if err == nil || !strings.Contains(err.Error(), "not found") {
				t.Errorf("Template = %v, want E2B's message", err)
			}
		})
	}
}

func TestSavedTemplates(t *testing.T) {
	named := func(alias string) map[string]any {
		return map[string]any{"names": []string{"local-dev-team/" + alias}, "snapshotID": "local-dev-team/" + alias + ":default"}
	}
	f, e := newReplayE2B(t, map[string]reply{
		"GET /snapshots?limit=100": {status: 200, next: "page2", body: []any{
			named("hibimg-b"),
			map[string]any{"names": []string{}, "snapshotID": "unnamed0000000000000:default"},
		}},
		"GET /snapshots?limit=100&nextToken=page2": {status: 200, body: []any{named("hibimg-a"), named("hibimg-gone")}},

		"GET /templates/aliases/hibimg-a":    aliasOf("snapA"),
		"GET /templates/aliases/hibimg-b":    aliasOf("snapB"),
		"GET /templates/aliases/hibimg-gone": e2bError(404, "template 'hibimg-gone' not found"),
		"GET /templates/snapA?limit=100":     templateDetail("snapA", []string{"local-dev-team/hibimg-a"}, build(build1, "ready", 2, 512, t0, &t1)),
		"GET /templates/snapB?limit=100":     templateDetail("snapB", []string{"local-dev-team/hibimg-b"}, build(build2, "ready", 2, 2048, t1, &t2)),
	})

	got, err := e.SavedTemplates(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := []engine.TemplateInfo{
		{ID: "snapA", Alias: "hibimg-a", CPUs: 2, MemoryMB: 512, Created: t1},
		{ID: "snapB", Alias: "hibimg-b", CPUs: 2, MemoryMB: 2048, Created: t2},
	}
	if !slices.Equal(got, want) {
		t.Errorf("SavedTemplates = %+v, want %+v: named saves only, sorted, without one deleted since listed", got, want)
	}
	for _, r := range f.requests() {
		if strings.Contains(r, "unnamed") {
			t.Errorf("SavedTemplates looked up the unnamed snapshot: %s", r)
		}
	}
}

func TestDeleteTemplate(t *testing.T) {
	const del = "DELETE /templates/snap1"
	cases := []struct {
		name  string
		reply reply
		want  error
		msg   string
	}{
		{name: "deleted", reply: reply{status: 204}},
		{
			name:  "running machines use it",
			reply: e2bError(400, "cannot delete template 'snap1' because there are running sandboxes using it"),
			want:  engine.ErrTemplateInUse,
			msg:   "running sandboxes using it",
		},
		{
			name:  "paused machines use it",
			reply: e2bError(400, "cannot delete template 'snap1' because there are paused sandboxes using it"),
			want:  engine.ErrTemplateInUse,
			msg:   "paused sandboxes using it",
		},
		{name: "missing", reply: e2bError(404, "template 'snap1' not found"), want: engine.ErrNoTemplate, msg: "not found"},
		{name: "bad ID", reply: e2bError(400, "Invalid template ID: bad"), msg: "Invalid template ID"},
		{name: "E2B failed", reply: e2bError(500, "Error when deleting template"), msg: "Error when deleting template"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, e := newReplayE2B(t, map[string]reply{del: c.reply})
			err := e.DeleteTemplate(t.Context(), "snap1")
			if c.reply.status == 204 {
				if err != nil {
					t.Errorf("DeleteTemplate = %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("DeleteTemplate succeeded")
			}
			if c.want != nil && !errors.Is(err, c.want) {
				t.Errorf("DeleteTemplate = %v, want %v", err, c.want)
			}
			for _, other := range []error{engine.ErrTemplateInUse, engine.ErrNoTemplate, engine.ErrNotFound} {
				if other != c.want && errors.Is(err, other) {
					t.Errorf("DeleteTemplate = %v, want it not to wrap %v", err, other)
				}
			}
			if !strings.Contains(err.Error(), c.msg) {
				t.Errorf("DeleteTemplate = %v, want E2B's message %q", err, c.msg)
			}
		})
	}
}
