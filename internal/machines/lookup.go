package machines

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/suggest"
	"github.com/solev/hibernaut/internal/termsafe"
)

// Lookup finds a machine by exact name. A miss wraps ErrNoMachine and may suggest a name from
// at most 1,000 machines. A failed suggestion listing leaves the original error unchanged.
func Lookup(ctx context.Context, e engine.Engine, name string) (engine.Machine, error) {
	em, err := lookupExact(ctx, e, name)
	if !errors.Is(err, ErrNoMachine) {
		return em, err
	}
	ems, listErr := e.List(ctx, engine.ListOptions{})
	if listErr != nil {
		return engine.Machine{}, err
	}
	names := make([]string, 0, min(len(ems), 1000))
	for _, em := range ems {
		if candidate := Name(em); hibapi.CheckName(candidate) == nil {
			names = append(names, candidate)
			if len(names) == 1000 {
				break
			}
		}
	}
	if closest, ok := suggest.Closest(name, names); ok {
		err = fmt.Errorf("%w. Did you mean %s?", err, termsafe.Line(closest))
	}
	return engine.Machine{}, err
}

func lookupExact(ctx context.Context, e engine.Engine, name string) (engine.Machine, error) {
	if err := hibapi.CheckName(name); err != nil {
		return engine.Machine{}, err
	}
	ems, err := e.List(ctx, engine.ListOptions{Metadata: map[string]string{labelName: name}})
	if err != nil {
		return engine.Machine{}, safeError(err)
	}
	var found []engine.Machine
	for _, em := range ems {
		if em.Metadata[labelName] == name {
			found = append(found, em)
		}
	}
	switch len(found) {
	case 0:
		return engine.Machine{}, fmt.Errorf("%w called %q", ErrNoMachine, name)
	case 1:
		return found[0], nil
	}
	ids := make([]string, len(found))
	for i, em := range found {
		ids[i] = termsafe.Line(em.ID)
	}
	return engine.Machine{}, fmt.Errorf("%d machines are called %q (%s)", len(found), name, strings.Join(ids, ", "))
}
