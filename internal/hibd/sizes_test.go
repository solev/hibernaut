package hibd

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/room"
)

func sizeTemplates() fake.Option {
	var names []string
	for _, s := range images.Sizes() {
		names = append(names, images.SizeTemplate("dev", s))
	}
	return fake.WithTemplates(names...)
}

func TestNewSize(t *testing.T) {
	e := newEngine(newKey(t), sizeTemplates())
	a, out, errOut := newApp(t, e)
	for _, tc := range []struct {
		name     string
		flags    []string
		size     string
		template string
	}{
		{"tiny", []string{"-size", "small"}, "small", "hib-dev-small"},
		{"mid", []string{"-size", "medium"}, "medium", "hib-dev"},
		{"big", []string{"-size", "large"}, "large", "hib-dev-large"},
		{"plain", nil, "medium", "hib-dev"},
	} {
		code, r, err := runJSON(t, a, out, append([]string{"new", tc.name, "-json"}, tc.flags...)...)
		if code != 0 || err != nil || r.Machine == nil || r.Machine.Size != tc.size {
			t.Errorf("new %s %q = %d, %+v, %v; want a %s machine; stderr %s", tc.name, tc.flags, code, r.Machine, err, tc.size, errOut)
			continue
		}
		em, err := e.Get(t.Context(), r.Machine.ID)
		if err != nil || em.Template != tc.template || em.Metadata["hib.size"] != tc.size {
			t.Errorf("new %s %q made %+v, %v; want template %s and hib.size %s", tc.name, tc.flags, em, err, tc.template, tc.size)
		}
	}
	for _, size := range []string{"", "huge", "Small", "small\x1b[31m"} {
		code, _, err := runJSON(t, a, out, "new", "bad", "-size", size, "-json")
		if code != 1 || err == nil || !strings.HasSuffix(err.Error(), "(there is: small, medium, large)") || strings.Contains(err.Error(), "\x1b") {
			t.Errorf("new -size %q = %d, %v; want an error that lists the sizes", size, code, err)
		}
	}
	errOut.Reset()
	if code := a.Run(t.Context(), []string{"new", "-size", "huge", "bad"}); code != 1 ||
		errOut.String() != "hibd new: no size \"huge\" (there is: small, medium, large)\n" {
		t.Errorf("new -size huge = %d, stderr %q", code, errOut)
	}
	if ms, _ := e.List(t.Context(), engine.ListOptions{}); len(ms) != 4 {
		t.Errorf("%d machines, want the 4 good ones", len(ms))
	}
}

func TestNewSizeNotBuilt(t *testing.T) {
	a, out, _ := newApp(t, newEngine(newKey(t)))
	code, _, err := runJSON(t, a, out, "new", "app", "-size", "small", "-json")
	if code != 1 || errText(err) != "image dev isn't built in size small yet (run: hib image build dev)" {
		t.Errorf("new -size small = %d, %v; want it to say small isn't built", code, err)
	}
}

func TestNewSizeSaysWhichSizesStillFit(t *testing.T) {
	a, out, _ := newApp(t, newEngine(newKey(t), sizeTemplates()))
	a.ReadRoom = func() (room.Host, error) {
		return room.Host{PageSize: 2 << 20, Pages: 3200, FreePages: 1100, DiskFree: 80 << 30}, nil
	}
	code, _, err := runJSON(t, a, out, "new", "app", "-size", "large", "-json")
	if code != 1 || !strings.Contains(errText(err), `no room on the host for "app"`) ||
		!strings.HasSuffix(errText(err), ". Sizes that still fit: small, medium") {
		t.Errorf("new -size large = %d, %v; want no room, and the sizes that still fit", code, err)
	}
}

func TestLsShowsSizes(t *testing.T) {
	e := fake.New()
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	add := func(id string, state engine.State, memoryMB int, labels map[string]string) {
		e.Add(engine.Machine{ID: id, State: state, MemoryMB: memoryMB, StartedAt: now, EndAt: now.Add(time.Hour), Metadata: labels})
	}
	add("tiny1", engine.Running, 1024, map[string]string{"hib.name": "tiny", "hib.image": "dev", "hib.size": "small"})
	add("old1", engine.Paused, 2048, map[string]string{"hib.name": "old", "hib.image": "dev"})
	add("odd1", engine.Running, 512, map[string]string{"hib.name": "odd", "hib.image": "mine"})
	add("bad1", engine.Paused, 4096, map[string]string{"hib.name": "bad", "hib.image": "dev", "hib.size": "huge\x1b[31m"})
	a, out, errOut := newApp(t, e)
	if code := a.Run(t.Context(), []string{"ls"}); code != 0 {
		t.Fatalf("ls = %d: %s", code, errOut)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if got := strings.Fields(lines[0]); !slices.Equal(got, []string{"NAME", "ID", "STATE", "IMAGE", "SIZE", "IDLE", "STARTED", "ENDS"}) {
		t.Errorf("ls header = %q", got)
	}
	sizes := map[string]string{}
	for _, l := range lines[1:] {
		f := strings.Fields(l)
		sizes[f[1]] = f[4]
	}
	want := map[string]string{"tiny1": "small", "old1": "medium", "odd1": "-", "bad1": "large"}
	if !maps.Equal(sizes, want) {
		t.Errorf("SIZE column = %v, want %v; ls =\n%s", sizes, want, out)
	}

	code, r, err := runJSON(t, a, out, "ls", "-json")
	if code != 0 || err != nil || r.Room == nil {
		t.Fatalf("ls -json = %d, %+v, %v", code, r, err)
	}
	for _, m := range r.Machines {
		if cmp.Or(m.Size, "-") != want[m.ID] {
			t.Errorf("ls -json size of %s = %q, want %q", m.ID, m.Size, want[m.ID])
		}
	}
	running := []room.Machine{{Name: "tiny", MemoryMB: 1024}, {Name: "odd", MemoryMB: 512}}
	wantRoom := []hibapi.SizeRoom{
		{Size: "small", Fits: roomy.Fits(1024, running)},
		{Size: "medium", Fits: roomy.Fits(2048, running)},
		{Size: "large", Fits: roomy.Fits(4096, running)},
	}
	if !slices.Equal(r.Room.Sizes, wantRoom) || r.Room.Fits != wantRoom[1].Fits || r.Room.Image != "dev" {
		t.Errorf("ls -json room = %+v, want sizes %+v and medium's count as Fits", r.Room, wantRoom)
	}
}

func builtNames(e *fake.Engine) []string {
	var names []string
	for _, spec := range e.Built() {
		names = append(names, spec.Name)
	}
	return names
}

func TestImageBuildBuildsEverySize(t *testing.T) {
	e := fake.New()
	a, out, errOut := newApp(t, e)
	out.Reset()
	if code := a.Run(t.Context(), []string{"image", "build", "dev", "-json"}); code != 0 {
		t.Fatalf("image build dev -json = %d: %s", code, errOut)
	}
	dec := json.NewDecoder(bytes.NewReader(out.Bytes()))
	var r hibapi.Response
	if err := dec.Decode(&r); err != nil || dec.More() {
		t.Fatalf("stdout = %q, %v; want exactly one JSON object", out, err)
	}
	if r.Template != "tmpl-hib-dev" || !slices.Equal(r.Sizes, []string{"medium", "small", "large"}) {
		t.Errorf("reply = %+v, want medium's template and the three sizes", r)
	}
	built := e.Built()
	for i, want := range []struct {
		name           string
		cpus, memoryMB int
	}{{"hib-dev", 2, 2048}, {"hib-dev-small", 1, 1024}, {"hib-dev-large", 4, 4096}} {
		if i >= len(built) {
			t.Fatalf("built %v, want hib-dev, hib-dev-small and hib-dev-large", builtNames(e))
		}
		b := built[i]
		if b.Name != want.name || b.CPUs != want.cpus || b.MemoryMB != want.memoryMB || b.Force {
			t.Errorf("build %d = %s, %d CPUs, %d MiB, force %v; want %+v", i, b.Name, b.CPUs, b.MemoryMB, b.Force, want)
		}
		if !slices.Equal(b.Steps, built[0].Steps) || b.FromImage != built[0].FromImage || b.MinFreeDiskMB != built[0].MinFreeDiskMB {
			t.Errorf("build %s doesn't share medium's steps, base image and disk", b.Name)
		}
	}
	logged := []int{
		strings.Index(errOut.String(), "built dev (medium) in "),
		strings.Index(errOut.String(), "built dev (small) in "),
		strings.Index(errOut.String(), "built dev (large) in "),
	}
	if logged[0] < 0 || logged[1] < logged[0] || logged[2] < logged[1] {
		t.Errorf("stderr = %q, want each size's time, in order", errOut)
	}

	if code := a.Run(t.Context(), []string{"image", "build", "-force", "dev", "-json"}); code != 0 {
		t.Fatalf("image build -force = %d: %s", code, errOut)
	}
	forced := e.Built()[3:]
	if len(forced) != 3 || !forced[0].Force || !forced[1].Force || !forced[2].Force {
		t.Errorf("forced builds = %+v, want every size forced", forced)
	}

	out.Reset()
	if code := a.Run(t.Context(), []string{"image", "build", "dev"}); code != 0 || out.String() != "tmpl-hib-dev\n" {
		t.Errorf("image build dev = %d, %q; want medium's template", code, out)
	}
}

func TestImageBuildStopsAtTheFirstFailure(t *testing.T) {
	for _, tc := range []struct {
		fails string
		built []string
		want  string
	}{
		{"hib-dev", nil, "building dev (medium): E2B is down. Built: none. Failed: medium. Not tried: small, large"},
		{"hib-dev-small", []string{"hib-dev"}, "building dev (small): E2B is down. Built: medium. Failed: small. Not tried: large"},
		{"hib-dev-large", []string{"hib-dev", "hib-dev-small"}, "building dev (large): E2B is down. Built: medium, small. Failed: large"},
	} {
		t.Run(tc.fails, func(t *testing.T) {
			var tried []string
			e := fake.New(fake.WithBuildHook(func(_ context.Context, spec engine.TemplateSpec) error {
				tried = append(tried, spec.Name)
				if spec.Name == tc.fails {
					return errors.New("E2B is down")
				}
				return nil
			}))
			a, out, errOut := newApp(t, e)
			code, _, err := runJSON(t, a, out, "image", "build", "dev", "-json")
			if code != 1 || errText(err) != tc.want {
				t.Errorf("image build = %d, %v; want %q", code, err, tc.want)
			}
			if got := builtNames(e); !slices.Equal(got, tc.built) || !slices.Equal(tried, append(slices.Clone(tc.built), tc.fails)) {
				t.Errorf("built %v after trying %v, want %v and then the failure", got, tried, tc.built)
			}
			if logged := strings.Contains(errOut.String(), "built dev (medium) in "); logged != (len(tc.built) > 0) {
				t.Errorf("stderr = %q, want medium's time only if it was built", errOut)
			}
		})
	}
}

func TestImageBuildChecksRoomBeforeEachSize(t *testing.T) {
	var events []string
	e := newEngine(newKey(t), fake.WithBuildHook(func(_ context.Context, spec engine.TemplateSpec) error {
		events = append(events, spec.Name)
		return nil
	}))
	a, out, errOut := newApp(t, e)
	a.ReadRoom = func() (room.Host, error) {
		events = append(events, "room")
		return roomy, nil
	}
	if code, _, err := runJSON(t, a, out, "image", "build", "dev", "-json"); code != 0 || err != nil {
		t.Fatalf("image build = %d, %v; stderr %s", code, err, errOut)
	}
	if want := []string{"room", "hib-dev", "room", "hib-dev-small", "room", "hib-dev-large"}; !slices.Equal(events, want) {
		t.Errorf("events = %v, want a room check before each size", events)
	}

	if code := a.Run(t.Context(), []string{"new", "web"}); code != 0 {
		t.Fatalf("new web: %s", errOut)
	}
	a.ReadRoom = readings(roomy, full)
	before := len(e.Built())
	code, _, err := runJSON(t, a, out, "image", "build", "dev", "-json")
	wantNoRoom(t, "image build with the host full after medium", code, errText(err), "dev (small)")
	if !strings.HasSuffix(errText(err), "hib pause NAME. Built: medium. Failed: small. Not tried: large") {
		t.Errorf("image build = %v, want it to say which sizes are built", err)
	}
	if got := builtNames(e)[before:]; !slices.Equal(got, []string{"hib-dev"}) {
		t.Errorf("built %v, want only medium", got)
	}
}

func TestImageBuildHasOneTimeLimit(t *testing.T) {
	var deadlines []time.Time
	e := fake.New(fake.WithBuildHook(func(ctx context.Context, _ engine.TemplateSpec) error {
		d, ok := ctx.Deadline()
		if !ok {
			return errors.New("no deadline")
		}
		deadlines = append(deadlines, d)
		return nil
	}))
	a, out, _ := newApp(t, e)
	start := time.Now()
	if code, _, err := runJSON(t, a, out, "image", "build", "dev", "-json"); code != 0 || err != nil {
		t.Fatalf("image build = %d, %v", code, err)
	}
	if len(deadlines) != 3 || deadlines[1] != deadlines[0] || deadlines[2] != deadlines[0] ||
		deadlines[0].Before(start.Add(buildTimeout)) || deadlines[0].After(time.Now().Add(buildTimeout)) {
		t.Errorf("deadlines = %v, want one hour from the start for every size", deadlines)
	}
}

func TestImageBuildStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var tried []string
	e := fake.New(fake.WithBuildHook(func(ctx context.Context, spec engine.TemplateSpec) error {
		tried = append(tried, spec.Name)
		if spec.Name == "hib-dev-small" {
			cancel()
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}))
	a, out, _ := newApp(t, e)
	code := a.Run(ctx, []string{"image", "build", "dev", "-json"})
	_, err := hibapi.DecodeResponse(out.Bytes())
	if code != 1 || errText(err) != "building dev (small): context canceled. Built: medium. Failed: small. Not tried: large" {
		t.Errorf("cancelled image build = %d, %v", code, err)
	}
	if !slices.Equal(tried, []string{"hib-dev", "hib-dev-small"}) {
		t.Errorf("tried %v, want it to stop at small", tried)
	}
}
