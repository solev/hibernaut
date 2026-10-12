package codex

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestRedacted(t *testing.T) {
	const value = "real-token-value"
	tokens := Tokens{IDToken: value, AccessToken: value, RefreshToken: value, AccountID: value}
	old := OldValue{Name: AccessName, Value: value}
	oldMarker := "[redacted old value of " + AccessName + "]"
	for _, tc := range []struct {
		v    any
		want string
	}{
		{tokens, "[redacted codex tokens]"},
		{&tokens, "[redacted codex tokens]"},
		{old, oldMarker},
		{&old, oldMarker},
		{[]OldValue{old}, oldMarker},
	} {
		// %d and %t reach only Format. Without it fmt prints the fields. %p isn't here: fmt
		// prints a value's fields for it whatever its methods, and go vet rejects %p on a value.
		for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d", "%t"} {
			got := fmt.Sprintf(format, tc.v)
			if strings.Contains(got, value) || !strings.Contains(got, tc.want) {
				t.Errorf("Sprintf(%q, %T) = %q, want %q without the real value",
					format, tc.v, got, tc.want)
			}
		}
		if data, err := json.Marshal(tc.v); err == nil {
			t.Errorf("json.Marshal(%T) = %s, want an error", tc.v, data)
		}
	}
}
