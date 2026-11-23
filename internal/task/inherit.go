package task

import "fmt"

// Inherit fills omitted task settings and refuses changes to the first run's settings.
func Inherit(first, next Spec) (Spec, error) {
	if next.Repo == (Repo{}) {
		next.Repo = first.Repo
	}
	if next.Base == "" {
		next.Base = first.Base
	}
	if next.Publish == "" {
		next.Publish = first.Publish
	}
	switch {
	case next.Repo != first.Repo:
		return Spec{}, fmt.Errorf("%w: repo", ErrTaskSpec)
	case next.Base != first.Base:
		return Spec{}, fmt.Errorf("%w: base", ErrTaskSpec)
	case next.Publish != first.Publish:
		return Spec{}, fmt.Errorf("%w: publish", ErrTaskSpec)
	}
	return next, nil
}
