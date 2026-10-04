package hibd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/solev/hibernaut/internal/engine"
)

func TestImageAndForkArgumentErrors(t *testing.T) {
	for _, args := range [][]string{
		{"image"}, {"image", "other"},
		{"image", "save"}, {"image", "save", "web"}, {"image", "save", "web", "myimg", "extra"},
		{"image", "ls", "extra"}, {"image", "rm"}, {"image", "rm", "myimg", "extra"},
		{"image", "save", "-unknown"}, {"image", "ls", "-unknown"}, {"image", "rm", "-unknown"},
		{"fork"}, {"fork", "web", "extra"}, {"fork", "web", "-count", "0"},
		{"fork", "web", "-count", "11"}, {"fork", "web", "-count", "many"}, {"fork", "web", "-unknown"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			a, _, _, errOut := imageApp(t)
			if code := a.Run(t.Context(), args); code != 2 || errOut.Len() == 0 {
				t.Fatalf("exit %d; stderr %q, want argument error", code, errOut)
			}
		})
	}
}

func TestSaveAndForkNeedRoot(t *testing.T) {
	for _, args := range [][]string{
		{"image", "save", "web", "myimg", "-json"}, {"fork", "web", "-json"},
	} {
		a, _, out, _ := imageApp(t)
		a.EUID = func() int { return 501 }
		if code, r, err := runJSON(t, a, out, args...); code != 1 || err == nil ||
			!strings.Contains(r.Error, "run it as root: sudo hibd") {
			t.Fatalf("%v = %d, %+v, %v", args, code, r, err)
		}
	}
}

func TestImageSaveRefusesAnUnconfirmedSnapshot(t *testing.T) {
	for _, broken := range []string{"failed", "incomplete files"} {
		t.Run(broken, func(t *testing.T) {
			a, e, out, _ := imageApp(t)
			code, r, err := runJSON(t, a, out, "new", "web", "-json")
			if code != 0 || err != nil || r.Machine == nil {
				t.Fatalf("new = %d, %v", code, err)
			}
			id := r.Machine.ID
			if broken == "failed" {
				a.Query = func(_ context.Context, sql string) ([][]string, error) {
					if strings.HasPrefix(sql, "SELECT status_group") {
						return [][]string{{"failed"}}, nil
					}
					return [][]string{{leakBuildA}}, nil
				}
			} else if err := os.WriteFile(filepath.Join(a.Getenv("HIBD_E2B_STORAGE"), leakBuildA, "metadata.json"), []byte("invalid"), 0o600); err != nil {
				t.Fatal(err)
			}
			code, r, err = runJSON(t, a, out, "image", "save", "web", "myimg", "-json")
			if code != 1 || err == nil || !strings.Contains(r.Error, "hibd couldn't confirm web's snapshot reached disk") {
				t.Fatalf("save = %d, %+v, %v", code, r, err)
			}
			if m, err := e.Get(t.Context(), id); err != nil || m.State != engine.Running {
				t.Fatalf("origin = %+v, %v", m, err)
			}
			if saved, err := e.SavedTemplates(t.Context()); err != nil || len(saved) != 0 {
				t.Fatalf("templates = %+v, %v, want nothing saved", saved, err)
			}
		})
	}
}
