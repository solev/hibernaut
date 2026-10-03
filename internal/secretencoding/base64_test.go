package secretencoding

import (
	"bytes"
	"encoding/base64"
	"math/rand/v2"
	"strings"
	"testing"
)

var encodings = map[string]*base64.Encoding{
	"std":     base64.StdEncoding,
	"raw-std": base64.RawStdEncoding,
	"url":     base64.URLEncoding,
	"raw-url": base64.RawURLEncoding,
}

func TestPatternsAppearWhereverTheValueIsEmbedded(t *testing.T) {
	value := []byte("gho_real_secret_value")
	r := rand.New(rand.NewPCG(1, 2))
	for name, enc := range encodings {
		for prefix := range 6 {
			for range 20 {
				before := randomBytes(r, prefix)
				after := randomBytes(r, r.IntN(4)+1)
				embedded := enc.EncodeToString(append(append(before, value...), after...))
				if core := Base64Core(enc, value, prefix); len(core) == 0 || !strings.Contains(embedded, string(core)) {
					t.Fatalf("%s: core %q isn't in %q (prefix %d)", name, core, embedded, prefix)
				}
				ending := enc.EncodeToString(append(before, value...))
				if end := Base64End(enc, value, prefix); len(end) == 0 || !strings.HasSuffix(ending, string(end)) {
					t.Fatalf("%s: end %q doesn't end %q (prefix %d)", name, end, ending, prefix)
				}
			}
		}
	}
}

func TestPatternsAreTotal(t *testing.T) {
	value := []byte("value")
	for name, enc := range encodings {
		for alignment := -4; alignment <= 4; alignment++ {
			if core := Base64Core(enc, nil, alignment); len(core) != 0 {
				t.Errorf("%s: empty value has core %q at alignment %d", name, core, alignment)
			}
			if end := Base64End(enc, nil, alignment); len(end) != 0 {
				t.Errorf("%s: empty value has end %q at alignment %d", name, end, alignment)
			}
		}
		// Three bytes make a whole base64 group, so alignment repeats every three bytes.
		for alignment := range 3 {
			for _, same := range []int{alignment - 3, alignment + 3} {
				if got, want := Base64Core(enc, value, same), Base64Core(enc, value, alignment); !bytes.Equal(got, want) {
					t.Errorf("%s: core at alignment %d = %q, want %q", name, same, got, want)
				}
				if got, want := Base64End(enc, value, same), Base64End(enc, value, alignment); !bytes.Equal(got, want) {
					t.Errorf("%s: end at alignment %d = %q, want %q", name, same, got, want)
				}
			}
		}
	}
}

func randomBytes(r *rand.Rand, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	return b
}
