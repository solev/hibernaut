package cleanup_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/cleanup"
	"github.com/solev/hibernaut/internal/engine/fake"
)

func TestHostSkipsHalfWrittenCacheFiles(t *testing.T) {
	// E2B writes each file of its layer cache's index to a hidden temporary file first, and a
	// crash leaves that half-written.
	cache := t.TempDir()
	index := filepath.Join(cache, "scope", "index")
	hash := strings.Repeat("0a", 32)
	if err := os.MkdirAll(index, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		hash:                     `{"template":{"build_id":"img-1"}}`,
		"." + hash + ".tmp-1234": `{"templ`,
	} {
		if err := os.WriteFile(filepath.Join(index, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h := cleanup.Host{Storage: t.TempDir(), Cache: cache, Engine: fake.New(),
		Query: func(context.Context, string) ([][]string, error) { return nil, nil }}
	if err := h.Clean(t.Context(), time.Now(), true, t.Logf); err != nil {
		t.Errorf("Clean: %v", err)
	}
}
