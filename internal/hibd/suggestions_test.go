package hibd

import (
	"strings"
	"testing"

	"github.com/solev/hibernaut/internal/engine"
)

func TestEveryMachineLookupSuggestsTypos(t *testing.T) {
	for _, args := range [][]string{
		{"rm", "ap", "-json"},
		{"pause", "ap", "-json"},
		{"bridge", "ap"},
		{"fork", "ap", "-json"},
		{"image", "save", "ap", "copy", "-json"},
		{"leakscan", "ap", "-json"},
	} {
		t.Run(strings.Join(args[:2], " "), func(t *testing.T) {
			a, e, out, errOut := imageApp(t)
			withSecretConfig(t, a)
			e.Add(engine.Machine{ID: "app-id", State: engine.Paused, Metadata: map[string]string{"hib.name": "app"}})
			a.Stdin = strings.NewReader("aSecretAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n")
			if code := a.Run(t.Context(), []string{"secret", "add", "test", "-env", "TEST", "-bind", "example.com"}); code != 0 {
				t.Fatalf("secret add exit %d: %s", code, errOut)
			}
			out.Reset()
			errOut.Reset()
			if code := a.Run(t.Context(), args); code != 1 {
				t.Fatalf("args %v exit %d: %s", args, code, errOut)
			}
			if !strings.Contains(out.String()+errOut.String(), "Did you mean app?") {
				t.Fatalf("missing suggestion for %v: stdout %s, stderr %s", args, out, errOut)
			}
		})
	}
}
