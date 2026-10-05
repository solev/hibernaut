package machines_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/machines"
	"github.com/solev/hibernaut/internal/room"

	"golang.org/x/crypto/ssh"
)

func imageManager(v *env, configure func(*machines.Config)) *machines.Manager {
	cfg := machines.Config{
		Engine: v.e, StateDir: v.dir, AuthorizedKeys: v.keys, ReadRoom: roomy,
		Durable: func(ctx context.Context, id string) error {
			em, err := v.e.Get(ctx, id)
			if err != nil {
				return err
			}
			if em.State != engine.Paused {
				return errors.New("snapshot wasn't paused")
			}
			return nil
		},
	}
	if configure != nil {
		configure(&cfg)
	}
	return machines.New(cfg)
}

func TestSaveImageGuardsFirstCheckpoint(t *testing.T) {
	v := newEnv(t)
	web := v.create(t, "web")
	m := imageManager(v, nil)
	v.e.FailNextSave(engine.ErrCheckpoint)
	_, err := m.SaveImage(t.Context(), "web", "myimg")
	want := "E2B couldn't save web (its checkpoint failed). web is running again as it was when the save began. Nothing was saved. Try again."
	if err == nil || err.Error() != want {
		t.Fatalf("SaveImage = %v, want %s", err, want)
	}
	if !errors.Is(err, engine.ErrCheckpoint) {
		t.Errorf("SaveImage = %v, want ErrCheckpoint", err)
	}
	if em := v.get(t, web.ID); em.State != engine.Running {
		t.Errorf("origin = %s, want running", em.State)
	}
	if _, err := v.e.Template(t.Context(), images.SavedTemplate("myimg")); !errors.Is(err, engine.ErrNoTemplate) {
		t.Errorf("failed save alias = %v, want it removed", err)
	}
}

func TestSaveImage(t *testing.T) {
	for _, paused := range []bool{false, true} {
		t.Run(map[bool]string{false: "running", true: "paused"}[paused], func(t *testing.T) {
			v := newEnv(t)
			web := v.createIdle(t, "web", time.Second)
			if paused {
				if err := v.m.Pause(t.Context(), "web"); err != nil {
					t.Fatal(err)
				}
			}
			m := imageManager(v, nil)
			img, err := m.SaveImage(t.Context(), "web", "myimg")
			if err != nil {
				t.Fatal(err)
			}
			if img.Name != "myimg" || img.Kind != "saved" || img.MemoryMB != 512 || img.CPUs != 2 || !img.Built || img.Created.IsZero() {
				t.Errorf("image = %+v", img)
			}
			want := engine.Running
			if paused {
				want = engine.Paused
			}
			if em := v.get(t, web.ID); em.State != want {
				t.Errorf("origin = %s, want %s", em.State, want)
			}
		})
	}
}

func TestSaveImageRefusesNames(t *testing.T) {
	v := newEnv(t)
	v.create(t, "web")
	m := imageManager(v, nil)
	for _, name := range []string{"dev", "Bad", strings.Repeat("a", 64)} {
		if _, err := m.SaveImage(t.Context(), "web", name); err == nil {
			t.Errorf("SaveImage(%q) accepted", name)
		}

	}
	if _, err := m.SaveImage(t.Context(), "web", "myimg"); err != nil {
		t.Fatal(err)
	}
	_, err := m.SaveImage(t.Context(), "web", "myimg")
	if err == nil || err.Error() != "image myimg exists: remove it first with: hib image rm myimg" {
		t.Errorf("duplicate SaveImage = %v", err)
	}
}

type copyGuest struct {
	mu       sync.Mutex
	key      ssh.PublicKey
	keys     map[string]ssh.PublicKey
	env      map[string][]byte
	requests map[string][]engine.ExecRequest
	fail     func(engine.ExecRequest) error
	reset    map[string]string
}

func newCopyGuest() *copyGuest {
	return &copyGuest{key: newKey(), keys: map[string]ssh.PublicKey{}, env: map[string][]byte{}, requests: map[string][]engine.ExecRequest{}, reset: map[string]string{}}
}

func (g *copyGuest) exec(_ context.Context, em engine.Machine, _ string, req engine.ExecRequest) (engine.ExecResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.requests[em.ID] = append(g.requests[em.ID], req)
	if g.fail != nil {
		if err := g.fail(req); err != nil {
			return engine.ExecResult{}, err
		}
	}
	if req.User != "root" {
		return engine.ExecResult{}, errors.New("command wasn't root")
	}
	switch req.Cmd {
	case images.MachineInit:
		key := g.keys[em.ID]
		if key == nil {
			key = g.key
			g.keys[em.ID] = key
		}
		return engine.ExecResult{Stdout: []byte(hibapi.FormatHostKey(key) + "\n")}, nil
	case "cat":
		file, ok := g.env[em.ID]
		if !ok {
			file = []byte("KEEP=yes\n# BEGIN hibernaut secrets: hibd manages this block and replaces edits\nGH_TOKEN=gho_hibplaceholder\n# END hibernaut secrets\n")
		}
		return engine.ExecResult{Stdout: file}, nil
	case "sh":
		if len(req.Args) == 5 && req.Args[2] == "hibd-reset" {
			if req.Args[3] != "/etc/hibernaut/ssh" || req.Args[4] != em.ID {
				return engine.ExecResult{}, errors.New("wrong re-key arguments")
			}
			if g.reset[em.ID] != em.ID {
				g.keys[em.ID] = newKey()
				g.reset[em.ID] = em.ID
			}
			return engine.ExecResult{}, nil
		}
		if len(req.Args) == 5 && req.Args[2] == "hibd-env" {
			next, err := base64.StdEncoding.DecodeString(req.Args[4])
			g.env[em.ID] = next
			return engine.ExecResult{}, err
		}
	}
	return engine.ExecResult{}, fmt.Errorf("unexpected command %q", req.Cmd)
}

func TestCreateFromSavedImage(t *testing.T) {
	g := newCopyGuest()
	v := newEnv(t, fake.WithExec(g.exec))
	web := v.create(t, "web")
	em := v.get(t, web.ID)
	em.MemoryMB = 4096
	v.e.Add(em)
	bigPool := imageManager(v, func(cfg *machines.Config) {
		cfg.ReadRoom = func() (room.Host, error) {
			return room.Host{PageSize: 2 << 20, Pages: 8192, FreePages: 8000, DiskFree: 80 << 30}, nil
		}
	})
	if _, err := bigPool.SaveImage(t.Context(), "web", "myimg"); err != nil {
		t.Fatal(err)
	}
	m := imageManager(v, nil)
	r, err := m.Room(t.Context(), "myimg")
	if err != nil || r.Fits != 1 || r.Image != "myimg" {
		t.Fatalf("saved image room = %+v, %v, want one 4 GiB machine", r, err)
	}
	full := imageManager(v, func(cfg *machines.Config) {
		cfg.ReadRoom = func() (room.Host, error) {
			return room.Host{PageSize: 2 << 20, Pages: 3200, FreePages: 1024, DiskFree: 80 << 30}, nil
		}
	})
	if _, err := full.Create(t.Context(), "too-big", "myimg", machines.DefaultIdle); !errors.Is(err, room.ErrFull) {
		t.Fatalf("create with only 2 GiB free = %v, want ErrFull", err)
	}
	copy, err := m.Create(t.Context(), "web2", "myimg", 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	got := v.get(t, copy.ID)
	tmpl, _ := v.e.Template(t.Context(), images.SavedTemplate("myimg"))
	if got.Template != tmpl.ID || got.MemoryMB != 4096 || got.Metadata["hib.image"] != "myimg" {
		t.Errorf("copy = %+v", got)
	}
	if reqs := g.requests[copy.ID]; len(reqs) != 4 || reqs[0].Cmd != "sh" || reqs[0].Args[2] != "hibd-reset" || reqs[1].Cmd != images.MachineInit || reqs[2].Cmd != "cat" {
		t.Errorf("copy setup requests = %+v", reqs)
	}
	if got := string(g.env[copy.ID]); got != "KEEP=yes\n" {
		t.Errorf("empty current setup left %q, want only the unmanaged env", got)
	}
	c, err := m.Dial(t.Context(), "web2")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if hibapi.FormatHostKey(c.HostKey) == hibapi.FormatHostKey(g.key) {
		t.Error("copy kept the origin's host key")
	}
}

func TestImagesAndRemoval(t *testing.T) {
	g := newCopyGuest()
	v := newEnv(t, fake.WithExec(g.exec))
	web := v.create(t, "web")
	m := imageManager(v, nil)
	if _, err := m.SaveImage(t.Context(), "web", "zulu"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.e.SaveTemplate(t.Context(), web.ID, "hibimg-dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.e.SaveTemplate(t.Context(), web.ID, "hibimg-alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.e.SaveTemplate(t.Context(), web.ID, "hibfork-hidden"); err != nil {
		t.Fatal(err)
	}
	all, err := m.Images(t.Context())
	if err != nil || len(all) != 4 {
		t.Fatalf("Images = %+v, %v", all, err)
	}
	if all[0].Name != "dev" || all[0].Kind != "built-in" || !all[0].Built || all[1].Name != "alpha" || all[2].Name != "dev" || !all[2].Shadowed || all[3].Name != "zulu" {
		t.Errorf("Images = %+v", all)
	}
	for _, name := range []string{"b", "a"} {
		if _, err := m.Create(t.Context(), name, "zulu", machines.DefaultIdle); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Pause(t.Context(), "a"); err != nil {
		t.Fatal(err)
	}
	err = m.RemoveImage(t.Context(), "zulu")
	if err == nil || !errors.Is(err, engine.ErrTemplateInUse) || !strings.Contains(err.Error(), "image zulu is used by machines a, b: remove them first") {
		t.Fatalf("RemoveImage in use = %v", err)
	}
	for _, name := range []string{"a", "b"} {
		if err := m.Remove(t.Context(), name); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.RemoveImage(t.Context(), "zulu"); err != nil {
		t.Fatal(err)
	}
	if err := m.RemoveImage(t.Context(), "dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.e.Template(t.Context(), images.Template("dev")); err != nil {
		t.Errorf("built-in template was removed: %v", err)
	}
	v = newEnv(t, fake.WithTemplates())
	all, err = imageManager(v, nil).Images(t.Context())
	if err != nil || len(all) != 1 || all[0].Built || all[0].MemoryMB != 2048 {
		t.Errorf("unbuilt catalog = %+v, %v", all, err)
	}
}

func TestCreateCleansUpUnknownOutcome(t *testing.T) {
	v := newEnv(t)
	v.e.FailCreateAfterStart(map[string]string{"hib.name": "web"})
	if _, err := v.m.Create(t.Context(), "web", "dev", machines.DefaultIdle); err == nil {
		t.Fatal("Create succeeded after the engine lost its response")
	}
	if ms := v.machines(t); len(ms) != 0 {
		t.Fatalf("failed Create left machines: %+v", ms)
	}
	v.create(t, "web")
}
