package machines_test

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/machines"
)

func TestSweepSharesInitialLockAcrossManagers(t *testing.T) {
	for _, initial := range []string{"missing", "existing"} {
		t.Run(initial, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, "host-keys"), 0o700); err != nil {
				t.Fatal(err)
			}
			if initial == "existing" {
				if err := os.WriteFile(filepath.Join(dir, "hibd.lock"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			e := fake.New()
			start := make(chan struct{})
			errs := make([]error, 32)
			var wg sync.WaitGroup
			for i := range errs {
				m := machines.New(machines.Config{Engine: e, StateDir: dir})
				wg.Go(func() {
					<-start
					errs[i] = m.SweepForks(t.Context())
				})
			}
			close(start)
			wg.Wait()
			for _, err := range errs {
				if err != nil && !errors.Is(err, machines.ErrBusy) {
					t.Errorf("SweepForks sharing %s lock: %v", initial, err)
				}
			}
		})
	}
}
