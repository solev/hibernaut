package hibd

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/hibapi"
)

func TestFailedImageSaveRestoresOriginAndLeavesNoImage(t *testing.T) {
	a, e, out, errOut := imageApp(t)
	code, r, err := runJSON(t, a, out, "new", "web", "-json")
	if code != 0 || err != nil || r.Machine == nil {
		t.Fatalf("new = %d, %v; stderr %s", code, err, errOut)
	}
	id := r.Machine.ID
	e.FailNextSave(engine.ErrCheckpoint)
	code, r, err = runJSON(t, a, out, "image", "save", "web", "myimg", "-json")
	if code != 1 || err == nil || !strings.Contains(r.Error, "checkpoint failed") ||
		!strings.Contains(r.Error, "web is running again as it was when the save began") ||
		!strings.Contains(r.Error, "Nothing was saved. Try again.") {
		t.Fatalf("failed save = %d, %+v, %v", code, r, err)
	}
	if m, err := e.Get(t.Context(), id); err != nil || m.State != engine.Running {
		t.Fatalf("origin = %+v, %v, want running", m, err)
	}
	code, r, err = runJSON(t, a, out, "image", "ls", "-json")
	if code != 0 || err != nil || len(r.Images) != 1 || r.Images[0].Name != "dev" {
		t.Fatalf("image ls after failed save = %d, %+v, %v", code, r, err)
	}
	if code, _, err := runJSON(t, a, out, "image", "save", "web", "myimg", "-json"); code != 0 || err != nil {
		t.Fatalf("save after failed save = %d, %v", code, err)
	}
}

func TestForkFailureKeepsCopiesInJSONAndText(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "json"}[asJSON], func(t *testing.T) {
			a, e, out, errOut := imageApp(t)
			if code, _, err := runJSON(t, a, out, "new", "web", "-json"); code != 0 || err != nil {
				t.Fatalf("new = %d, %v", code, err)
			}
			e.FailCreateAfterStart(map[string]string{"hib.name": "web-2"})
			out.Reset()
			args := []string{"fork", "web", "-count", "3"}
			if asJSON {
				args = append(args, "-json")
			}
			if code := a.Run(t.Context(), args); code != 1 {
				t.Fatalf("fork = %d, want 1; stdout %s; stderr %s", code, out, errOut)
			}
			if asJSON {
				r, err := hibapi.DecodeResponse(out.Bytes())
				if err == nil || len(r.Machines) != 1 || r.Machines[0].Name != "web-1" ||
					!strings.Contains(r.Error, "couldn't make web-2") {
					t.Fatalf("partial fork = %+v, %v", r, err)
				}
			} else if out.String() != "made web-1 from web. connect with: ssh web-1.hib\n" ||
				!strings.Contains(errOut.String(), "couldn't make web-2") {
				t.Fatalf("partial fork stdout %q; stderr %q", out, errOut)
			}
			ms, err := e.List(t.Context(), engine.ListOptions{})
			if err != nil || len(ms) != 2 {
				t.Fatalf("machines = %+v, %v, want origin and first copy", ms, err)
			}
			for _, m := range ms {
				if n := m.Metadata["hib.name"]; n != "web" && n != "web-1" {
					t.Fatalf("incomplete copy survived: %s", n)
				}
			}
		})
	}
}

func TestImageRemoveRefusesMachinesUsingIt(t *testing.T) {
	a, _, out, _ := imageApp(t)
	for _, args := range [][]string{
		{"new", "web", "-json"}, {"image", "save", "web", "myimg", "-json"},
		{"new", "web2", "-image", "myimg", "-json"}, {"pause", "web2", "-json"},
	} {
		if code, _, err := runJSON(t, a, out, args...); code != 0 || err != nil {
			t.Fatalf("%v = %d, %v", args, code, err)
		}
	}
	if code, r, err := runJSON(t, a, out, "image", "rm", "myimg", "-json"); code != 1 || err == nil ||
		r.Error != "image myimg is used by machines web2: remove them first" {
		t.Fatalf("remove used image = %d, %+v, %v", code, r, err)
	}
	for _, args := range [][]string{{"rm", "web2", "-json"}, {"image", "rm", "myimg", "-json"}} {
		if code, _, err := runJSON(t, a, out, args...); code != 0 || err != nil {
			t.Fatalf("%v = %d, %v", args, code, err)
		}
	}
	if code, r, err := runJSON(t, a, out, "image", "ls", "-json"); code != 0 || err != nil || len(r.Images) != 1 {
		t.Fatalf("images after remove = %d, %+v, %v", code, r, err)
	}
}

func TestImageOperationsRefuseQuiescing(t *testing.T) {
	for _, args := range [][]string{
		{"image", "save", "web", "myimg", "-json"},
		{"fork", "web", "-json"},
	} {
		a, e, out, _ := imageApp(t)
		code, r, err := runJSON(t, a, out, "new", "web", "-json")
		if code != 0 || err != nil || r.Machine == nil {
			t.Fatalf("new = %d, %v", code, err)
		}
		id := r.Machine.ID
		if err := os.WriteFile(a.Getenv("HIBD_QUIESCING"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		a.Query = func(_ context.Context, _ string) ([][]string, error) {
			t.Error("quiescing operation tried to prepare a snapshot")
			return nil, nil
		}
		if code, r, err := runJSON(t, a, out, args...); code != 1 || err == nil ||
			!strings.Contains(r.Error, "the host is going to sleep: try again in a few minutes") {
			t.Fatalf("%v = %d, %+v, %v", args, code, r, err)
		}
		if m, err := e.Get(t.Context(), id); err != nil || m.State != engine.Running {
			t.Fatalf("origin = %+v, %v", m, err)
		}
	}
}
