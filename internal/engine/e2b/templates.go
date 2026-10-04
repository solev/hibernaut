package e2b

import (
	"context"
	"errors"

	"github.com/solev/hibernaut/internal/engine"
)

// SaveTemplate isn't implemented yet (M6).
func (e *Engine) SaveTemplate(ctx context.Context, id, alias string) (engine.TemplateInfo, error) {
	return engine.TemplateInfo{}, errors.ErrUnsupported
}

// Template isn't implemented yet (M6).
func (e *Engine) Template(ctx context.Context, alias string) (engine.TemplateInfo, error) {
	return engine.TemplateInfo{}, errors.ErrUnsupported
}

// SavedTemplates isn't implemented yet (M6).
func (e *Engine) SavedTemplates(ctx context.Context) ([]engine.TemplateInfo, error) {
	return nil, errors.ErrUnsupported
}

// DeleteTemplate isn't implemented yet (M6).
func (e *Engine) DeleteTemplate(ctx context.Context, id string) error {
	return errors.ErrUnsupported
}
