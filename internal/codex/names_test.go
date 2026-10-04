package codex

import "testing"

func TestNames(t *testing.T) {
	for _, tc := range []struct {
		name               string
		managed, scrubOnly bool
	}{
		{AccessName, true, false},
		{RefreshName, true, true},
		{IDName, true, true},
		{"codex-other", true, false},
		{"github", false, false},
		{"my-codex-key", false, false},
	} {
		if got := Managed(tc.name); got != tc.managed {
			t.Errorf("Managed(%q) = %v, want %v", tc.name, got, tc.managed)
		}
		if got := ScrubOnly(tc.name); got != tc.scrubOnly {
			t.Errorf("ScrubOnly(%q) = %v, want %v", tc.name, got, tc.scrubOnly)
		}
	}
}
