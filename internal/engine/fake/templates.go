package fake

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/solev/hibernaut/internal/engine"
)

// template is a template the fake knows: built, named by WithTemplates, or saved.
type template struct {
	info  engine.TemplateInfo
	saved bool
	// broken marks an alias a failed save left behind. Template finds it, but machines can't be
	// created from it.
	broken bool
	// files is a copy of the saved machine's home directory, with WithLocalExec.
	files string
}

// source is what Create starts a machine from.
type source struct {
	id       string
	memoryMB int
	files    string
}

// E2B's template names.
var aliasRE = regexp.MustCompile(`^[a-z0-9_-]+$`)

// FailNextSave makes the next SaveTemplate fail with err, and leave a new alias behind, as E2B
// can. If err wraps engine.ErrCheckpoint, the machine is left paused too, as E2B leaves it. The
// fake doesn't roll the machine's files back.
func (e *Engine) FailNextSave(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.failSave = err
}

// FailCreateAfterStart makes the next Create whose metadata has every pair in metadata start its
// machine, then fail without returning it, as E2B's create does when reading the new machine
// fails. Empty metadata matches the next Create.
func (e *Engine) FailCreateAfterStart(metadata map[string]string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.failCreates = append(e.failCreates, metadata)
}

// SaveTemplate saves machine id as template alias. With WithLocalExec, it copies the machine's
// home directory, and machines created from the template start with the copy.
func (e *Engine) SaveTemplate(ctx context.Context, id, alias string) (engine.TemplateInfo, error) {
	if err := ctx.Err(); err != nil {
		return engine.TemplateInfo{}, err
	}
	if !aliasRE.MatchString(alias) {
		return engine.TemplateInfo{}, fmt.Errorf("fake: bad template name %q", alias)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	m, err := e.lookup(id)
	if err != nil {
		return engine.TemplateInfo{}, err
	}
	if m.State != engine.Running {
		return engine.TemplateInfo{}, fmt.Errorf("save %s: %w", id, engine.ErrNotRunning)
	}
	t := e.aliases[alias]
	if t != nil && !t.saved {
		return engine.TemplateInfo{}, fmt.Errorf("fake: save %s as %s: a built template has that name", id, alias)
	}
	if t == nil {
		t = &template{info: engine.TemplateInfo{ID: NewID(), Alias: alias}, saved: true, broken: true}
		e.aliases[alias] = t
	}
	if err := e.failSave; err != nil {
		e.failSave = nil
		if errors.Is(err, engine.ErrCheckpoint) {
			m.State = engine.Paused
		}
		return engine.TemplateInfo{}, fmt.Errorf("save %s as %s: %w", id, alias, err)
	}
	if home := e.homes[id]; home != "" {
		dir := filepath.Join(e.homeRoot, "templates", t.info.ID)
		if err := os.RemoveAll(dir); err != nil {
			return engine.TemplateInfo{}, err
		}
		if err := os.CopyFS(dir, os.DirFS(home)); err != nil {
			return engine.TemplateInfo{}, fmt.Errorf("fake: save %s as %s: %w", id, alias, err)
		}
		t.files = dir
	}
	t.info.CPUs, t.info.MemoryMB, t.info.Created = DefaultCPUs, m.MemoryMB, e.now()
	t.broken = false
	return t.info, nil
}

// Template finds the templates BuildTemplate built, WithTemplates named and SaveTemplate saved.
func (e *Engine) Template(ctx context.Context, alias string) (engine.TemplateInfo, error) {
	if err := ctx.Err(); err != nil {
		return engine.TemplateInfo{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	t, ok := e.aliases[alias]
	if !ok {
		return engine.TemplateInfo{}, fmt.Errorf("template %s: %w", alias, engine.ErrNoTemplate)
	}
	return t.info, nil
}

// SavedTemplates leaves out every alias a failed save left behind.
func (e *Engine) SavedTemplates(ctx context.Context) ([]engine.TemplateInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []engine.TemplateInfo
	for _, t := range e.aliases {
		if t.saved && !t.broken {
			out = append(out, t.info)
		}
	}
	slices.SortFunc(out, func(a, b engine.TemplateInfo) int { return strings.Compare(a.Alias, b.Alias) })
	return out, nil
}

// DeleteTemplate deletes any template, built or saved, that no machine was created from.
func (e *Engine) DeleteTemplate(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	var t *template
	for _, c := range e.aliases {
		if c.info.ID == id {
			t = c
		}
	}
	if t == nil {
		return fmt.Errorf("delete template %s: %w", id, engine.ErrNoTemplate)
	}
	for _, m := range e.machines {
		if m.Template == t.info.ID || m.Template == t.info.Alias {
			return fmt.Errorf("delete template %s: %w", id, engine.ErrTemplateInUse)
		}
	}
	delete(e.aliases, t.info.Alias)
	if e.templates != nil {
		delete(e.templates, t.info.ID)
		delete(e.templates, t.info.Alias)
	}
	if t.files != "" {
		return os.RemoveAll(t.files)
	}
	return nil
}

// source resolves name for Create. Saved templates are found by alias or ID. Any other name
// is taken as is, and must be known if WithTemplates was used.
func (e *Engine) source(name string) (source, error) {
	for _, t := range e.aliases {
		if !t.saved || t.info.Alias != name && t.info.ID != name {
			continue
		}
		if t.broken {
			return source{}, fmt.Errorf("fake: create from %s: a failed save left it without a build: %w", name, engine.ErrNoTemplate)
		}
		return source{id: t.info.ID, memoryMB: t.info.MemoryMB, files: t.files}, nil
	}
	if e.templates != nil && !e.templates[name] {
		return source{}, fmt.Errorf("fake: create from %s: %w", name, engine.ErrNoTemplate)
	}
	return source{id: name, memoryMB: DefaultMemoryMB}, nil
}

func (e *Engine) takeCreateFailure(metadata map[string]string) bool {
	for i, want := range e.failCreates {
		if hasAll(metadata, want) {
			e.failCreates = slices.Delete(e.failCreates, i, i+1)
			return true
		}
	}
	return false
}
