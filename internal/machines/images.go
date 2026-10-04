package machines

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/room"
	"github.com/solev/hibernaut/internal/termsafe"
)

// SaveImage saves machine as image name, which machines can then be made from, and returns the
// image. name follows hibapi.CheckName, and can't be a built-in image's or an existing saved
// image's. So that a failed save costs machine at most what it did since the save began,
// machine is paused, its snapshot confirmed with Config.Durable, and resumed before the save.
// It's left running or paused as it was.
func (m *Manager) SaveImage(ctx context.Context, machine, name string) (hibapi.Image, error) {
	if err := hibapi.CheckName(name); err != nil {
		return hibapi.Image{}, err
	}
	if images.IsBuiltIn(name) {
		return hibapi.Image{}, fmt.Errorf("image %s is built-in: choose another name", name)
	}
	if err := m.canSave(); err != nil {
		return hibapi.Image{}, err
	}
	unlock, err := m.lock(ctx)
	if err != nil {
		return hibapi.Image{}, err
	}
	defer unlock()
	alias := images.SavedTemplate(name)
	switch _, err := m.e.Template(ctx, alias); {
	case err == nil:
		return hibapi.Image{}, fmt.Errorf("image %s exists: remove it first with: hib image rm %s", name, name)
	case !errors.Is(err, engine.ErrNoTemplate):
		return hibapi.Image{}, safeError(err)
	}
	em, err := m.lookup(ctx, machine)
	if err != nil {
		return hibapi.Image{}, err
	}
	running := 1
	if em.State == engine.Paused {
		running = 2
	}
	if err := m.checkPlan(ctx, machine, room.Plan{MemoryMB: em.MemoryMB, Running: running, Snapshots: 2}); err != nil {
		return hibapi.Image{}, err
	}
	tmpl, err := m.saveOrigin(ctx, em, alias)
	if tmpl.ID == "" {
		return hibapi.Image{}, err
	}
	return savedImage(tmpl, name), err
}

// Images returns the built-in images, built or not, then the saved ones, each sorted by name.
func (m *Manager) Images(ctx context.Context) ([]hibapi.Image, error) {
	var out []hibapi.Image
	for _, name := range images.Names() {
		memoryMB, _ := images.MemoryMB(name)
		img := hibapi.Image{Name: name, Kind: hibapi.KindBuiltIn, MemoryMB: memoryMB}
		tmpl, err := m.e.Template(ctx, images.Template(name))
		switch {
		case err == nil:
			img.Built, img.Created, img.CPUs = true, tmpl.Created, tmpl.CPUs
			img.MemoryMB = tmpl.MemoryMB
		case !errors.Is(err, engine.ErrNoTemplate):
			return nil, safeError(err)
		}
		out = append(out, img)
	}
	tmpls, err := m.e.SavedTemplates(ctx)
	if err != nil {
		return nil, safeError(err)
	}
	var saved []hibapi.Image
	for _, tmpl := range tmpls {
		if name, ok := images.SavedName(tmpl.Alias); ok {
			saved = append(saved, savedImage(tmpl, name))
		}
	}
	slices.SortFunc(saved, func(a, b hibapi.Image) int { return strings.Compare(a.Name, b.Name) })
	return append(out, saved...), nil
}

// RemoveImage deletes saved image name, never a built-in one. While machines use the image, it
// fails and names them.
func (m *Manager) RemoveImage(ctx context.Context, name string) error {
	if err := hibapi.CheckName(name); err != nil {
		return err
	}
	if err := m.checkState(); err != nil {
		return err
	}
	unlock, err := m.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	tmpl, err := m.e.Template(ctx, images.SavedTemplate(name))
	if errors.Is(err, engine.ErrNoTemplate) {
		return describe(err, fmt.Sprintf("no saved image %s: %v. List images with: hib image ls", name, err))
	}
	if err != nil {
		return safeError(err)
	}
	err = m.e.DeleteTemplate(ctx, tmpl.ID)
	if !errors.Is(err, engine.ErrTemplateInUse) {
		return safeError(err)
	}
	ems, lerr := m.e.List(ctx, engine.ListOptions{})
	if lerr != nil {
		return safeError(errors.Join(err, lerr))
	}
	var names []string
	for _, em := range ems {
		if em.Template == tmpl.ID {
			names = append(names, cmp.Or(Name(em), termsafe.String(em.ID)))
		}
	}
	if len(names) == 0 {
		return safeError(err)
	}
	slices.Sort(names)
	return describe(err, fmt.Sprintf("image %s is used by machines %s: remove them first", name, strings.Join(names, ", ")))
}

func savedImage(tmpl engine.TemplateInfo, name string) hibapi.Image {
	return hibapi.Image{
		Name: name, Kind: hibapi.KindSaved, MemoryMB: tmpl.MemoryMB, CPUs: tmpl.CPUs,
		Created: tmpl.Created, Built: true, Shadowed: images.IsBuiltIn(name),
	}
}

func (m *Manager) imageMemory(ctx context.Context, name string) (int, error) {
	if images.IsBuiltIn(name) {
		return images.MemoryMB(name)
	}
	if err := hibapi.CheckName(name); err != nil {
		return 0, fmt.Errorf("no image %q: %w", name, err)
	}
	tmpl, err := m.e.Template(ctx, images.Template(name))
	if err != nil {
		return 0, describe(err, fmt.Sprintf("no image %q: %v. List images with: hib image ls", name, err))
	}
	if tmpl.MemoryMB <= 0 {
		return 0, fmt.Errorf("image %s has no memory size: check it with: hib image ls", name)
	}
	return tmpl.MemoryMB, nil
}
