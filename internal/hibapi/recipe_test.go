package hibapi

import (
	"strings"
	"testing"
)

func TestRecipeCommandAndTimeoutBounds(t *testing.T) {
	for _, tc := range []struct {
		command string
		seconds int64
		valid   bool
	}{
		{strings.Repeat("a", 16380), 9223372036, true},
		{strings.Repeat("a", 16381), 1, false},
		{"true", 9223372037, false},
		{"true", -1, false},
		{"true", 0, true},
	} {
		r := Recipe{Name: "check", Command: []string{tc.command}, TimeoutSeconds: tc.seconds}
		if err := r.Check(); (err == nil) != tc.valid {
			t.Errorf("command of %d bytes, timeout %d = %v; want valid %v",
				len(tc.command), tc.seconds, err, tc.valid)
		}

	}
}

func TestPrintRecipeDefaults(t *testing.T) {
	for _, tc := range []struct {
		name   string
		recipe Recipe
		want   []string
	}{
		{"unset", Recipe{Name: "check", Command: []string{"true"}},
			[]string{"  image (default)\n", "  size (default)\n", "  timeout (default)\n"}},
		{"named-default", Recipe{Name: "check", Command: []string{"true"}, Image: "default"},
			[]string{"  image default\n", "  size (default)\n", "  timeout (default)\n"}},
		{"set", Recipe{Name: "check", Command: []string{"true"}, Image: "custom", Size: "large", TimeoutSeconds: 3600},
			[]string{"  image custom\n", "  size large\n", "  timeout 1h0m0s\n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			if err := PrintRecipe(&out, tc.recipe); err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.want {
				if !strings.Contains(out.String(), want) {
					t.Errorf("recipe = %q; want %q", out.String(), want)
				}
			}
		})
	}
}
