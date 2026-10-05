package machines_test

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/machines"
	"github.com/solev/hibernaut/internal/room"
)

func buildSizes(t *testing.T, e *fake.Engine) {
	t.Helper()
	for _, s := range images.Sizes() {
		spec := engine.TemplateSpec{Name: images.SizeTemplate("dev", s), FromImage: "ubuntu:24.04", CPUs: s.CPUs(), MemoryMB: s.MemoryMB()}
		if _, err := e.BuildTemplate(t.Context(), spec, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func sizeNamed(t *testing.T, name string) images.Size {
	t.Helper()
	s, err := images.SizeNamed(name)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCreateSizes(t *testing.T) {
	v := newEnv(t)
	buildSizes(t, v.e)
	for _, tc := range []struct {
		name, size, want string
		cpus, memoryMB   int
		template         string
	}{
		{"tiny", "small", "small", 1, 1024, "hib-dev-small"},
		{"mid", "medium", "medium", 2, 2048, "hib-dev"},
		{"big", "large", "large", 4, 4096, "hib-dev-large"},
		{"plain", "", "medium", 2, 2048, "hib-dev"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var s images.Size
			if tc.size != "" {
				s = sizeNamed(t, tc.size)
			}
			m, err := v.m.CreateSize(t.Context(), tc.name, "dev", s, machines.DefaultIdle)
			if err != nil {
				t.Fatal(err)
			}
			em := v.get(t, m.ID)
			tmpl, err := v.e.Template(t.Context(), tc.template)
			if err != nil {
				t.Fatal(err)
			}
			if m.Size != tc.want || em.Metadata["hib.size"] != tc.want || em.Template != tmpl.ID || em.MemoryMB != tc.memoryMB {
				t.Errorf("machine %+v from %s with %d MiB and labels %v, want %s from %s with %d MiB",
					m, em.Template, em.MemoryMB, em.Metadata, tc.want, tmpl.ID, tc.memoryMB)
			}
			if mt, err := v.e.Metrics(t.Context(), m.ID); err != nil || mt.CPUCount != tc.cpus || mt.MemTotal != int64(tc.memoryMB)<<20 {
				t.Errorf("Metrics = %+v, %v, want %d CPUs and %d MiB", mt, err, tc.cpus, tc.memoryMB)
			}
		})
	}
	listed, err := v.m.List(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	sizes := map[string]string{}
	for _, m := range listed {
		sizes[m.Name] = m.Size
	}
	if want := map[string]string{"tiny": "small", "mid": "medium", "big": "large", "plain": "medium"}; !maps.Equal(sizes, want) {
		t.Errorf("List sizes = %v, want %v", sizes, want)
	}
}

func TestCreateSizeNotBuilt(t *testing.T) {
	v := newEnv(t)
	_, err := v.m.CreateSize(t.Context(), "app", "dev", sizeNamed(t, "small"), machines.DefaultIdle)
	if want := "image dev isn't built in size small yet (run: hib image build dev)"; err == nil || err.Error() != want {
		t.Errorf("CreateSize = %v, want %s", err, want)
	}
	if n := len(v.machines(t)); n != 0 {
		t.Errorf("%d machines, want none", n)
	}
}

func TestSavedImageKeepsItsSize(t *testing.T) {
	g := newCopyGuest()
	v := newEnv(t, fake.WithExec(g.exec))
	buildSizes(t, v.e)
	m := imageManager(v, nil)
	if _, err := m.CreateSize(t.Context(), "tiny", "dev", sizeNamed(t, "small"), machines.DefaultIdle); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SaveImage(t.Context(), "tiny", "notes"); err != nil {
		t.Fatal(err)
	}
	for _, size := range images.Sizes() {
		_, err := m.CreateSize(t.Context(), "copy", "notes", size, machines.DefaultIdle)
		want := "image notes is a saved image, and its machines are the size of the machine it was saved from (small). Leave out --size"
		if err == nil || err.Error() != want {
			t.Errorf("CreateSize(%s) = %v, want %s", size.Name(), err, want)
		}
	}
	copy, err := m.Create(t.Context(), "copy", "notes", machines.DefaultIdle)
	if err != nil {
		t.Fatal(err)
	}
	em := v.get(t, copy.ID)
	if copy.Size != "small" || em.Metadata["hib.size"] != "small" || em.MemoryMB != 1024 {
		t.Errorf("copy %+v with %d MiB and labels %v, want small", copy, em.MemoryMB, em.Metadata)
	}
	if mt, err := v.e.Metrics(t.Context(), copy.ID); err != nil || mt.CPUCount != 1 {
		t.Errorf("copy's Metrics = %+v, %v, want 1 CPU", mt, err)
	}

	web := v.create(t, "web")
	em = v.get(t, web.ID)
	em.MemoryMB = 1536
	v.e.Add(em)
	if _, err := m.SaveImage(t.Context(), "web", "odd"); err != nil {
		t.Fatal(err)
	}
	_, err = m.CreateSize(t.Context(), "odd1", "odd", sizeNamed(t, "medium"), machines.DefaultIdle)
	if err == nil || !strings.Contains(err.Error(), "the size of the machine it was saved from (1.5 GiB). Leave out --size") {
		t.Errorf("CreateSize from an image of no size = %v", err)
	}
	odd, err := m.Create(t.Context(), "odd1", "odd", machines.DefaultIdle)
	if err != nil {
		t.Fatal(err)
	}
	if em := v.get(t, odd.ID); odd.Size != "" || em.Metadata["hib.size"] != "" || em.MemoryMB != 1536 {
		t.Errorf("machine from an image of no size = %+v, labels %v, %d MiB", odd, em.Metadata, em.MemoryMB)
	}
}

func TestMachineFromBeforeSizes(t *testing.T) {
	g := newCopyGuest()
	v := newEnv(t, fake.WithExec(g.exec))
	v.e.Add(engine.Machine{ID: "old1", State: engine.Paused, Template: "hib-dev", MemoryMB: 2048,
		Metadata: map[string]string{"hib.name": "old", "hib.image": "dev", "hib.idle": "15m0s"}})
	v.e.Add(engine.Machine{ID: "bad1", State: engine.Paused, Template: "hib-dev", MemoryMB: 2048,
		Metadata: map[string]string{"hib.name": "bad", "hib.image": "dev", "hib.size": "huge\x1b[31m"}})
	v.e.Add(engine.Machine{ID: "lab1", State: engine.Paused, Template: "hib-dev", MemoryMB: 2048,
		Metadata: map[string]string{"hib.name": "lab", "hib.image": "dev", "hib.size": "small"}})
	v.e.Add(engine.Machine{ID: "odd1", State: engine.Paused, Template: "hib-dev", MemoryMB: 512,
		Metadata: map[string]string{"hib.name": "odd", "hib.image": "dev"}})
	m := imageManager(v, nil)
	listed, err := m.List(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	sizes := map[string]string{}
	for _, mach := range listed {
		sizes[mach.Name] = mach.Size
	}
	if want := map[string]string{"old": "medium", "bad": "medium", "lab": "small", "odd": ""}; !maps.Equal(sizes, want) {
		t.Errorf("List sizes = %v, want %v", sizes, want)
	}

	c, err := m.Dial(t.Context(), "old")
	if err != nil {
		t.Fatalf("Dial resuming a machine from before sizes: %v", err)
	}
	c.Close()
	if em := v.get(t, "old1"); em.State != engine.Running || em.MemoryMB != 2048 {
		t.Errorf("old = %s with %d MiB, want running with 2048", em.State, em.MemoryMB)
	}
	made, err := m.Fork(t.Context(), "old", 1)
	if err != nil {
		t.Fatal(err)
	}
	if em := v.get(t, made[0].ID); made[0].Size != "medium" || em.Metadata["hib.size"] != "medium" || em.MemoryMB != 2048 {
		t.Errorf("copy of a machine from before sizes = %+v, labels %v, %d MiB", made[0], em.Metadata, em.MemoryMB)
	}
}

func TestForkCopiesKeepTheOriginsSize(t *testing.T) {
	g := newCopyGuest()
	v := newEnv(t, fake.WithExec(g.exec))
	buildSizes(t, v.e)
	m := imageManager(v, nil)
	if _, err := m.CreateSize(t.Context(), "tiny", "dev", sizeNamed(t, "small"), machines.DefaultIdle); err != nil {
		t.Fatal(err)
	}
	made, err := m.Fork(t.Context(), "tiny", 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, copy := range made {
		if em := v.get(t, copy.ID); copy.Size != "small" || em.Metadata["hib.size"] != "small" || em.MemoryMB != 1024 {
			t.Errorf("copy %+v, labels %v, %d MiB, want small", copy, em.Metadata, em.MemoryMB)
		}
	}
}

func TestLargeMachineCanNeverBeSavedOrForked(t *testing.T) {
	g := newCopyGuest()
	v := newEnv(t, fake.WithExec(g.exec))
	buildSizes(t, v.e)
	m := imageManager(v, nil)
	big, err := m.CreateSize(t.Context(), "big", "dev", sizeNamed(t, "large"), machines.DefaultIdle)
	if err != nil {
		t.Fatal(err)
	}
	want := `no room on the host for "big": it needs 8 GiB of memory at once, and the host has 6.2 GiB ` +
		`for machines in all. It can't fit on this host, even with every machine paused`
	for _, paused := range []bool{false, true} {
		if paused {
			if err := m.Pause(t.Context(), "big"); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := m.SaveImage(t.Context(), "big", "bigimg"); err == nil || err.Error() != want || !errors.Is(err, room.ErrFull) {
			t.Errorf("SaveImage (paused %v) = %v\nwant %s", paused, err, want)
		}
		if _, err := m.Fork(t.Context(), "big", 1); err == nil || err.Error() != want {
			t.Errorf("Fork (paused %v) = %v\nwant %s", paused, err, want)
		}
	}
	if n := len(v.machines(t)); n != 1 {
		t.Errorf("%d machines, want only big", n)
	}
	if em := v.get(t, big.ID); em.State != engine.Paused {
		t.Errorf("big is %s, want it left paused", em.State)
	}
}

func TestNoRoomSaysWhichSizesStillFit(t *testing.T) {
	short := func() (room.Host, error) {
		return room.Host{PageSize: 2 << 20, Pages: 3200, FreePages: 600, DiskFree: 80 << 30}, nil
	}
	newManager := func(v *env, e engine.Engine, readRoom func() (room.Host, error)) *machines.Manager {
		return machines.New(machines.Config{Engine: e, StateDir: v.dir, AuthorizedKeys: v.keys, ReadRoom: readRoom})
	}
	v := newEnv(t)
	buildSizes(t, v.e)
	v.create(t, "web")
	for name, readRoom := range map[string]func() (room.Host, error){
		"first check":   short,
		"after refusal": sequence(roomy, short),
	} {
		t.Run(name, func(t *testing.T) {
			e := engine.Engine(v.e)
			if name == "after refusal" {
				e = placeRefusing{v.e}
			}
			_, err := newManager(v, e, readRoom).Create(t.Context(), "app", "dev", machines.DefaultIdle)
			if err == nil || !errors.Is(err, room.ErrFull) ||
				!strings.HasSuffix(err.Error(), "Running now: web. To free memory, pause one: hib pause NAME. Sizes that still fit: small") {
				t.Errorf("Create = %v, want the sizes that still fit", err)
			}
		})
	}
	_, err := newManager(v, v.e, func() (room.Host, error) {
		return room.Host{PageSize: 2 << 20, Pages: 3200, FreePages: 100, DiskFree: 80 << 30}, nil
	}).Create(t.Context(), "app", "dev", machines.DefaultIdle)
	if err == nil || strings.Contains(err.Error(), "Sizes that still fit") {
		t.Errorf("Create with no size fitting = %v, want no sizes", err)
	}
	if n := len(v.machines(t)); n != 1 {
		t.Errorf("%d machines, want only web", n)
	}
}

func TestSavedImageNoRoomSuggestsNoSizes(t *testing.T) {
	g := newCopyGuest()
	v := newEnv(t, fake.WithExec(g.exec))
	v.create(t, "web")
	if _, err := imageManager(v, nil).SaveImage(t.Context(), "web", "myimg"); err != nil {
		t.Fatal(err)
	}
	full := imageManager(v, func(cfg *machines.Config) {
		cfg.ReadRoom = func() (room.Host, error) {
			return room.Host{PageSize: 2 << 20, Pages: 3200, FreePages: 100, DiskFree: 80 << 30}, nil
		}
	})
	_, err := full.Create(t.Context(), "copy", "myimg", machines.DefaultIdle)
	if err == nil || !errors.Is(err, room.ErrFull) || strings.Contains(err.Error(), "Sizes") {
		t.Errorf("Create from a saved image = %v, want no room and no sizes", err)
	}
}

func TestRoomCountsEverySizeFromOneReading(t *testing.T) {
	v := newEnv(t)
	reads := 0
	m := machines.New(machines.Config{Engine: v.e, StateDir: v.dir, AuthorizedKeys: v.keys, ReadRoom: func() (room.Host, error) {
		reads++
		return roomy()
	}})
	r, err := m.Room(t.Context(), images.Default)
	if err != nil {
		t.Fatal(err)
	}
	want := []hibapi.SizeRoom{{Size: "small", Fits: 6}, {Size: "medium", Fits: 3}, {Size: "large", Fits: 1}}
	if r.Fits != 3 || r.Image != "dev" || !slices.Equal(r.Sizes, want) || reads != 1 {
		t.Errorf("Room = %+v after %d readings, want %v from one", r, reads, want)
	}
}

type placeRefusing struct{ *fake.Engine }

func (placeRefusing) Create(context.Context, engine.CreateOptions) (engine.Machine, error) {
	return engine.Machine{}, errors.New("E2B returned 500: Failed to place sandbox, please retry")
}

func sequence(hs ...func() (room.Host, error)) func() (room.Host, error) {
	return func() (room.Host, error) {
		h := hs[0]
		if len(hs) > 1 {
			hs = hs[1:]
		}
		return h()
	}
}
