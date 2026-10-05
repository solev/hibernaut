package e2b

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/e2b/e2bapi"
)

// SaveTemplate saves with E2B's named snapshot, then reads the template back, because E2B's
// answer names the template without giving its ID.
func (e *Engine) SaveTemplate(ctx context.Context, id, alias string) (engine.TemplateInfo, error) {
	answer, err := e.api.PostSandboxesSandboxIDSnapshots(ctx, id, e2bapi.SandboxSnapshotRequest{Name: &alias})
	if err != nil {
		if answer == nil {
			err = errors.Join(engine.ErrNoAnswer, err)
		}
		return engine.TemplateInfo{}, fmt.Errorf("save %s: %w", id, err)
	}
	resp, err := e2bapi.ParsePostSandboxesSandboxIDSnapshotsResponse(answer)
	if err != nil {
		return engine.TemplateInfo{}, fmt.Errorf("save %s: %w", id, err)
	}
	if resp.JSON201 == nil {
		return engine.TemplateInfo{}, e.saveError(ctx, id, resp.StatusCode(), resp.Body)
	}
	info, err := e.Template(ctx, alias)
	if err != nil {
		return engine.TemplateInfo{}, fmt.Errorf("save %s: saved as %s, then reading it back failed: %w", id, alias, err)
	}
	return info, nil
}

// saveError maps a failed save. E2B answers 404 and 409 only before it creates anything, so only
// they become ErrNotFound or ErrNotRunning. Its 409 means the machine is pausing, being killed or
// already being saved.
func (e *Engine) saveError(ctx context.Context, id string, status int, body []byte) error {
	saveErr := apiError("save", id, status, body)
	switch status {
	case http.StatusNotFound:
		// E2B answers 404 for a paused machine too: it saves only running ones.
		if _, err := e.running(ctx, id); err != nil {
			return fmt.Errorf("save: %w", err)
		}
	case http.StatusInternalServerError:
		// E2B answers 500 both when it stopped the machine because its checkpoint failed and when
		// the save failed before the checkpoint, leaving the machine running.
		_, err := e.running(ctx, id)
		switch {
		case errors.Is(err, engine.ErrNotRunning), errors.Is(err, engine.ErrNotFound):
			return fmt.Errorf("%w: %w", saveErr, engine.ErrCheckpoint)
		case err != nil:
			return errors.Join(saveErr, err)
		}
	}
	return saveErr
}

// Template looks alias up, and takes its CPUs, memory and time from its newest ready build. An
// alias a failed save left behind has no ready build, so it comes back with only its ID.
func (e *Engine) Template(ctx context.Context, alias string) (engine.TemplateInfo, error) {
	resp, err := e.api.GetTemplatesAliasesAliasWithResponse(ctx, alias)
	if err != nil {
		return engine.TemplateInfo{}, fmt.Errorf("template %s: %w", alias, err)
	}
	if resp.JSON200 == nil {
		return engine.TemplateInfo{}, templateError("template", alias, resp.StatusCode(), resp.Body)
	}
	b, err := e.newestReadyBuild(ctx, resp.JSON200.TemplateID)
	if err != nil {
		return engine.TemplateInfo{}, err
	}
	info := engine.TemplateInfo{ID: resp.JSON200.TemplateID, Alias: bareAlias(alias)}
	if b != nil {
		info.CPUs, info.MemoryMB, info.Created = int(b.CpuCount), int(b.MemoryMB), b.CreatedAt
		if b.FinishedAt != nil {
			info.Created = *b.FinishedAt
		}
	}
	return info, nil
}

func (e *Engine) newestReadyBuild(ctx context.Context, id string) (*e2bapi.TemplateBuild, error) {
	params := &e2bapi.GetTemplatesTemplateIDParams{Limit: ptr(e2bapi.PaginationLimit(100))}
	for {
		resp, err := e.api.GetTemplatesTemplateIDWithResponse(ctx, id, params)
		if err != nil {
			return nil, fmt.Errorf("template %s: %w", id, err)
		}
		if resp.JSON200 == nil {
			return nil, templateError("template", id, resp.StatusCode(), resp.Body)
		}
		// E2B lists builds newest first.
		builds := resp.JSON200.Builds
		if i := slices.IndexFunc(builds, func(b e2bapi.TemplateBuild) bool { return b.Status == e2bapi.TemplateBuildStatusReady }); i >= 0 {
			return &builds[i], nil
		}
		next := resp.HTTPResponse.Header.Get("X-Next-Token")
		if next == "" {
			return nil, nil
		}
		params.NextToken = &next
	}
}

// SavedTemplates lists E2B's snapshot templates, which only saves make, and leaves out those
// saved without a name.
func (e *Engine) SavedTemplates(ctx context.Context) ([]engine.TemplateInfo, error) {
	params := &e2bapi.GetSnapshotsParams{Limit: ptr(e2bapi.PaginationLimit(100))}
	var out []engine.TemplateInfo
	for {
		resp, err := e.api.GetSnapshotsWithResponse(ctx, params)
		if err != nil {
			return nil, fmt.Errorf("list saved templates: %w", err)
		}
		if resp.JSON200 == nil {
			return nil, apiError("list saved templates", "", resp.StatusCode(), resp.Body)
		}
		for _, s := range *resp.JSON200 {
			if len(s.Names) == 0 {
				continue
			}
			info, err := e.Template(ctx, bareAlias(s.Names[0]))
			if errors.Is(err, engine.ErrNoTemplate) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("list saved templates: %w", err)
			}
			out = append(out, info)
		}
		next := resp.HTTPResponse.Header.Get("X-Next-Token")
		if next == "" {
			break
		}
		params.NextToken = &next
	}
	slices.SortFunc(out, func(a, b engine.TemplateInfo) int { return strings.Compare(a.Alias, b.Alias) })
	return out, nil
}

// DeleteTemplate deletes template id. E2B only marks it deleted, and keeps its files for
// hibd cleanup to free.
func (e *Engine) DeleteTemplate(ctx context.Context, id string) error {
	resp, err := e.api.DeleteTemplatesTemplateIDWithResponse(ctx, id)
	if err != nil {
		return fmt.Errorf("delete template %s: %w", id, err)
	}
	if resp.StatusCode() == http.StatusNoContent {
		return nil
	}
	return templateError("delete template", id, resp.StatusCode(), resp.Body)
}

func templateError(op, name string, status int, body []byte) error {
	msg := apiMessage(body)
	switch {
	case status == http.StatusNotFound:
		return fmt.Errorf("%s %s: %s: %w", op, name, msg, engine.ErrNoTemplate)
	// E2B tells a template in use from other bad requests only by its message.
	case status == http.StatusBadRequest && strings.Contains(msg, "sandboxes using it"):
		return fmt.Errorf("%s %s: %s: %w", op, name, msg, engine.ErrTemplateInUse)
	}
	return fmt.Errorf("%s %s: E2B returned %d: %s", op, name, status, msg)
}

// E2B names templates namespace/alias:tag.
func bareAlias(name string) string {
	name = name[strings.LastIndex(name, "/")+1:]
	name, _, _ = strings.Cut(name, ":")
	return name
}
