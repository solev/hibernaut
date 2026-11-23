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
