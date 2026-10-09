package linkguard

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/solev/hibernaut/internal/links"
)

func readCounter(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTokensStartAtOneAndPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	tokens := &Tokens{path: path}
	for want := uint32(1); want <= 3; want++ {
		got, err := tokens.Next()
		if err != nil || got != want {
			t.Fatalf("Next = %d, %v, want %d", got, err, want)
		}
		// The file already names the next token when this one leaves.
		if c := readCounter(t, path); c != strconv.Itoa(int(want)+1)+"\n" {
			t.Fatalf("after token %d the file holds %q", want, c)
		}
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != links.StateMode {
		t.Errorf("token file mode %v, %v", info.Mode(), err)
	}
	// A restarted proxy carries on from the file.
	if got, err := (&Tokens{path: path}).Next(); err != nil || got != 4 {
		t.Fatalf("after a restart Next = %d, %v", got, err)
	}
}

func TestTokensNotWrittenNotReturned(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only directory")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	got, err := (&Tokens{path: filepath.Join(dir, "token")}).Next()
	if err == nil || got != 0 {
		t.Fatalf("Next = %d, %v with the counter unwritable", got, err)
	}
}

func TestTokensRunOut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(strconv.Itoa(links.GuardMaxToken)), 0o600); err != nil {
		t.Fatal(err)
	}
	tokens := &Tokens{path: path}
	if got, err := tokens.Next(); err != nil || got != links.GuardMaxToken {
		t.Fatalf("last token: %d, %v", got, err)
	}
	for range 2 {
		if got, err := tokens.Next(); !errors.Is(err, links.ErrTokensSpent) {
			t.Fatalf("past the last token: %d, %v", got, err)
		}
	}
}

func TestTokensRefuseACorruptCounter(t *testing.T) {
	for _, content := range []string{"", "x\n", "0\n", "-1", "99999999999"} {
		path := filepath.Join(t.TempDir(), "token")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, err := (&Tokens{path: path}).Next(); err == nil {
			t.Errorf("counter %q gave token %d", content, got)
		}
	}
}

func TestTokensAreUnique(t *testing.T) {
	tokens := &Tokens{path: filepath.Join(t.TempDir(), "token")}
	var (
		mu   sync.Mutex
		seen = map[uint32]bool{}
		wg   sync.WaitGroup
	)
	for range 8 {
		wg.Go(func() {
			for range 25 {
				got, err := tokens.Next()
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				if seen[got] {
					t.Errorf("token %d handed out twice", got)
				}
				seen[got] = true
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if len(seen) != 200 {
		t.Errorf("%d tokens", len(seen))
	}
}
