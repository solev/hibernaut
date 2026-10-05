package machines_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/machines"
)

type lookupEngine struct {
	engine.Engine
	failListing bool
	fullReads   *int
}

// List fails only the unfiltered listing used for suggestions.
func (e lookupEngine) List(ctx context.Context, opts engine.ListOptions) ([]engine.Machine, error) {
	if len(opts.Metadata) == 0 && len(opts.States) == 0 && e.fullReads != nil {
		*e.fullReads++
	}
	if e.failListing && len(opts.Metadata) == 0 && len(opts.States) == 0 {
		return nil, errors.New("listing failed")
	}
	return e.Engine.List(ctx, opts)
}

func TestLookupSuggestsMachineNamesWithoutChangingErrorIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, typo, want string
		names            []string
		failListing      bool
	}{
		{"one edit", "ap", "Did you mean app?", []string{"app"}, false},
		{"swap", "apepr", "Did you mean apper?", []string{"apper"}, false},
		{"prefix", "ap", "Did you mean application?", []string{"application"}, false},
		{"ambiguous", "ap", "", []string{"app", "api"}, false},
		{"too far", "other", "", []string{"app"}, false},
		{"listing failed", "ap", "", []string{"app"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := fake.New()
			for i, name := range tc.names {
				e.Add(engine.Machine{ID: fmt.Sprint(i), State: engine.Paused, Metadata: map[string]string{"hib.name": name}})
			}
			_, err := machines.Lookup(t.Context(), lookupEngine{Engine: e, failListing: tc.failListing}, tc.typo)
			if !errors.Is(err, machines.ErrNoMachine) {
				t.Fatalf("lookup = %v, want ErrNoMachine", err)
			}
			if tc.want != "" {
				if !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("lookup = %v, want %q", err, tc.want)
				}
			} else if err.Error() != fmt.Sprintf("no machine called %q", tc.typo) {
				t.Fatalf("lookup altered the original error: %v", err)
			}
		})
	}
}

func TestLookupUsesAtMostOneThousandNamesAndExactMatchesFirst(t *testing.T) {
	e := fake.New()
	for i := range 1000 {
		e.Add(engine.Machine{ID: fmt.Sprint(i), State: engine.Paused,
			Metadata: map[string]string{"hib.name": fmt.Sprintf("machine%04d", i)}})
	}
	e.Add(engine.Machine{ID: "app-id", State: engine.Running, Metadata: map[string]string{"hib.name": "app"}})
	if _, err := machines.Lookup(t.Context(), e, "ap"); !errors.Is(err, machines.ErrNoMachine) ||
		strings.Contains(err.Error(), "Did you mean") {
		t.Fatalf("more than one thousand names used: %v", err)
	}
	m, err := machines.Lookup(t.Context(), e, "app")
	if err != nil || m.ID != "app-id" {
		t.Fatalf("exact lookup = %+v, %v", m, err)
	}
}

func TestCreateDoesNotFetchSuggestionsForANewName(t *testing.T) {
	v := newEnv(t)
	fullReads := 0
	m := machines.New(machines.Config{
		Engine: lookupEngine{Engine: v.e, fullReads: &fullReads}, StateDir: v.dir,
		AuthorizedKeys: v.keys, ReadRoom: roomy,
	})
	if _, err := m.Create(t.Context(), "app", "dev", machines.DefaultIdle); err != nil {
		t.Fatal(err)
	}
	if fullReads != 0 {
		t.Fatalf("creating a new name fetched suggestions %d times", fullReads)
	}
}
