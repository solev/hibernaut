package swap

import "testing"

func TestFingerprint(t *testing.T) {
	a, b := Fingerprint("real-value-one"), Fingerprint("real-value-two")
	if len(a) != 32 || a == b || a != Fingerprint("real-value-one") {
		t.Fatalf("Fingerprint = %q and %q, want 32 hex digits, stable, and different for different values", a, b)
	}
	sw, err := New([]Secret{{Name: "one", Placeholder: "placeholder-one", Value: "real-value-one", Sites: []string{"example.com"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := sw.Fingerprint("one"); got != a {
		t.Fatalf("Swapper.Fingerprint(one) = %q, want %q", got, a)
	}
	if got := sw.Fingerprint("two"); got != "" {
		t.Fatalf("Swapper.Fingerprint(two) = %q, want empty", got)
	}
}
