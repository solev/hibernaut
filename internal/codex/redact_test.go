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
	for _, v := range []any{tokens, &tokens, old, &old, []OldValue{old}} {
		for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x"} {
			if got := fmt.Sprintf(format, v); strings.Contains(got, value) {
				t.Errorf("Sprintf(%q, %T) = %q, which holds the real value", format, v, got)
			}
		}
		if data, err := json.Marshal(v); err == nil {
			t.Errorf("json.Marshal(%T) = %s, want an error", v, data)
		}
	}
	if got := fmt.Sprint(old); !strings.Contains(got, AccessName) {
		t.Errorf("Sprint(OldValue) = %q, want the secret's name", got)
	}
}
