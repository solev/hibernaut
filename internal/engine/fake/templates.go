package fake

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/solev/hibernaut/internal/engine"
)

type template struct {
	info         engine.TemplateInfo
	saved        bool
	noReadyBuild bool
	homeCopy     string
}

type resolvedTemplate struct {
	id       string
	cpus     int
	memoryMB int
	homeCopy string
}

// E2B's template names.
var aliasRE = regexp.MustCompile(`^[a-z0-9_-]+$`)

// FailNextSave makes the next SaveTemplate of a running machine fail with err. If err wraps
// engine.ErrNotRunning or engine.ErrNotFound, the save creates nothing, as E2B's 404 and 409 come
// before it creates the alias. Any other err leaves a new alias behind with no build, as E2B can. If err
// wraps engine.ErrCheckpoint, the machine stops as E2B stops it: it's left paused if it was paused
// or saved before, and removed with its home directory if not, since it has no snapshot to fall
// back on. The fake doesn't roll the machine's files back.
func (e *Engine) FailNextSave(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.failSave = err
}

// RejectNextSave makes the next SaveTemplate of a running machine fail with err before it creates
// anything, as E2B's save does when it can't reach the machine's node or record the save. The
// machine keeps running.
func (e *Engine) RejectNextSave(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rejectSave = err
}

// DelayNextSave makes the next SaveTemplate wait d after it looks its alias up, then go on to fail
// or succeed as armed. Other calls can use the fake meanwhile. The wait is in real time, which a
// synctest bubble controls, not WithClock's. If the save's context ends first, the save fails
// with the context's error and creates nothing.
func (e *Engine) DelayNextSave(d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.saveDelay = d
}

// FailCreateAfterStart makes the next Create whose metadata has every pair in metadata start its
// machine, then fail without returning it, as E2B's create does when reading the new machine
// fails. Empty metadata matches the next Create.
func (e *Engine) FailCreateAfterStart(metadata map[string]string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.failCreatesMatching = append(e.failCreatesMatching, maps.Clone(metadata))
}

// SaveTemplate saves machine id as template alias. With WithLocalExec, it copies the machine's
// home directory, and machines created from the template start with the copy. With
// WithAliasCache, saving into an alias the cache hides fails with a plain error, and creates
// nothing.
func (e *Engine) SaveTemplate(ctx context.Context, id, alias string) (engine.TemplateInfo, error) {
	if err := ctx.Err(); err != nil {
		return engine.TemplateInfo{}, err
	}
	if !aliasRE.MatchString(alias) {
		return engine.TemplateInfo{}, fmt.Errorf("fake: bad template name %q", alias)
	}
	e.mu.Lock()
	resolved := e.lookupAliasLocked(alias)
	delay := e.saveDelay
	e.saveDelay = 0
	e.mu.Unlock()
	if err := wait(ctx, delay); err != nil {
		return engine.TemplateInfo{}, fmt.Errorf("save %s as %s: %w", id, alias, err)
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
	if err := e.takeFailureBeforeCreating(); err != nil {
		return engine.TemplateInfo{}, fmt.Errorf("save %s as %s: %w", id, alias, err)
	}
	t := e.aliases[alias]
	if resolved == nil && t != nil {
		return engine.TemplateInfo{}, fmt.Errorf("fake: save %s as %s: creating the alias failed: it already exists", id, alias)
	}
	if t != nil && !t.saved {
		return engine.TemplateInfo{}, fmt.Errorf("fake: save %s as %s: a built template has that name", id, alias)
	}
	if t == nil {
		t = &template{info: engine.TemplateInfo{ID: NewID(), Alias: alias}, saved: true, noReadyBuild: true}
		e.aliases[alias] = t
	}
	if err := e.failSave; err != nil {
		e.failSave = nil
		err = fmt.Errorf("save %s as %s: %w", id, alias, err)
		switch {
		case !errors.Is(err, engine.ErrCheckpoint):
		case m.hasSnapshot:
			m.State = engine.Paused
		default:
			err = errors.Join(err, e.drop(m))
		}
		return engine.TemplateInfo{}, err
	}
	if m.home != "" {
		dir := e.newBuildDir(t.info.ID)
		if err := os.CopyFS(dir, os.DirFS(m.home)); err != nil {
			return engine.TemplateInfo{}, fmt.Errorf("fake: save %s as %s: %w", id, alias, err)
		}
		t.homeCopy = dir
	}
	t.info.CPUs, t.info.MemoryMB, t.info.Created = m.cpus, m.MemoryMB, e.now()
	t.noReadyBuild = false
	m.hasSnapshot = true
	delete(e.missedAt, alias)
	return t.info, nil
}

func (e *Engine) takeFailureBeforeCreating() error {
	if err := e.failSave; errors.Is(err, engine.ErrNotRunning) || errors.Is(err, engine.ErrNotFound) {
		e.failSave = nil
		return err
	}
	err := e.rejectSave
	e.rejectSave = nil
	return err
}

func wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Template finds the templates BuildTemplate built, WithTemplates named and SaveTemplate saved,
// unless WithAliasCache hides them.
func (e *Engine) Template(ctx context.Context, alias string) (engine.TemplateInfo, error) {
	if err := ctx.Err(); err != nil {
		return engine.TemplateInfo{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	t := e.lookupAliasLocked(alias)
	if t == nil {
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
		if t.saved && !t.noReadyBuild {
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
	t := e.byID(id)
	if t == nil {
		return fmt.Errorf("delete template %s: %w", id, engine.ErrNoTemplate)
	}
	for _, m := range e.machines {
		if m.Template == t.info.ID || m.Template == t.info.Alias {
			return fmt.Errorf("delete template %s: %w", id, engine.ErrTemplateInUse)
		}
	}
	delete(e.aliases, t.info.Alias)
	delete(e.missedAt, t.info.Alias)
	if e.homeRoot == "" {
		return nil
	}
	return os.RemoveAll(e.templateDir(id))
}

func (e *Engine) templateDir(id string) string {
	return filepath.Join(e.homeRoot, "templates", id)
}

func (e *Engine) newBuildDir(templateID string) string {
	e.buildDirs++
	return filepath.Join(e.templateDir(templateID), strconv.Itoa(e.buildDirs))
}

func (e *Engine) byID(id string) *template {
	for _, t := range e.aliases {
		if t.info.ID == id {
			return t
		}
	}
	return nil
}

func (e *Engine) resolveTemplateLocked(name string) (resolvedTemplate, error) {
	t := e.lookupAliasLocked(name)
	if t == nil {
		t = e.byID(name)
	}
	switch {
	case t != nil && t.noReadyBuild:
		return resolvedTemplate{}, fmt.Errorf("fake: create from %s: a failed save left it without a build: %w", name, engine.ErrNoTemplate)
	case t != nil:
		return resolvedTemplate{id: t.info.ID, cpus: t.info.CPUs, memoryMB: t.info.MemoryMB, homeCopy: t.homeCopy}, nil
	case e.refuseUnknownTemplates || e.aliases[name] != nil:
		return resolvedTemplate{}, fmt.Errorf("fake: create from %s: %w", name, engine.ErrNoTemplate)
	}
	return resolvedTemplate{id: name, cpus: DefaultCPUs, memoryMB: DefaultMemoryMB}, nil
}

func (e *Engine) takeCreateFailure(metadata map[string]string) bool {
	for i, want := range e.failCreatesMatching {
		if hasAll(metadata, want) {
			e.failCreatesMatching = slices.Delete(e.failCreatesMatching, i, i+1)
			return true
		}
	}
	return false
}
