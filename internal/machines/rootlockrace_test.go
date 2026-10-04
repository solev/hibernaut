package machines

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestSweepSharesInitialLockWithRoot(t *testing.T) {
	for _, callers := range []string{"root", "mixed"} {
		t.Run(callers, func(t *testing.T) {
			state, e := sharedStateManager(t)
			if err := os.Mkdir(filepath.Join(state.dir, "host-keys"), 0o700); err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			errs := make([]error, 32)
			var wg sync.WaitGroup
			for i := range errs {
				m := New(Config{Engine: e, StateDir: state.dir})
				if callers == "root" || i%2 == 0 {
					m.euid = func() int { return 0 }
				}
				wg.Go(func() {
					<-start
					errs[i] = m.SweepForks(t.Context())
				})
			}
			close(start)
			wg.Wait()
			for _, err := range errs {
				if err != nil && !errors.Is(err, ErrBusy) {
					t.Errorf("%s SweepForks sharing initial lock: %v", callers, err)
				}
			}
		})
	}
}
