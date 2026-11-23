package taskguest

import (
	"strings"
	"testing"
)

func TestPublicationErrorBound(t *testing.T) {
	value := strings.Repeat("\x00", 8<<20)
	for _, prefix := range []string{"", "unknown", "changes=", "commit=", "start="} {
		t.Run(prefix, func(t *testing.T) {
			_, err := ParsePublication([]byte(prefix + value))
			if err == nil {
				t.Fatal("accepted an invalid publication")
			}
			if n := len(err.Error()); n > 1250 {
				t.Fatalf("publication error has %d bytes; want at most 200 quoted input bytes", n)
			}
		})
	}
}
