package swap_test

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/solev/hibernaut/internal/egress/swap"
)

func TestScrubBase64AtEveryAlignmentAndEnding(t *testing.T) {
	for _, token := range []struct {
		name, value, placeholder, padding string
		suffixes                          []string
	}{
		{"binary", "\xfb\xff-token-REAL-secret", "\xfb\xff-token-FAKE-secret", "z", []string{"", "!tail"}},
		{"partial groups", "token-real-secret", "token-fake-masked", "a", []string{""}},
	} {
		for _, encoding := range []struct {
			name string
			enc  *base64.Encoding
		}{
			{"standard padded", base64.StdEncoding},
			{"standard raw", base64.RawStdEncoding},
			{"url padded", base64.URLEncoding},
			{"url raw", base64.RawURLEncoding},
		} {
			for _, prefix := range []string{"", "x", "xy"} {
				for _, suffix := range token.suffixes {
					for extra := range 3 {
						t.Run(fmt.Sprintf("%s/%s/prefix=%d/suffix=%d/length=%d", token.name, encoding.name, len(prefix), len(suffix), extra), func(t *testing.T) {
							value := token.value + strings.Repeat("z", extra)
							placeholder := token.placeholder + strings.Repeat(token.padding, extra)
							sw := newSwapper(t, []swap.Secret{{
								Name: "token", Value: value, Placeholder: placeholder, Sites: []string{"example.com"},
							}})
							in := encoding.enc.EncodeToString([]byte(prefix + value + suffix))
							want := encoding.enc.EncodeToString([]byte(prefix + placeholder + suffix))
							header := http.Header{"X-Echo": {"before " + in + " after"}}
							sw.ScrubHeader(header)
							if got := header.Get("X-Echo"); got != "before "+want+" after" {
								t.Fatalf("header = %q, want %q", got, "before "+want+" after")
							}
							got, err := io.ReadAll(sw.ScrubBody(oneByteReadCloser{r: strings.NewReader(in)}))
							if err != nil || string(got) != want {
								t.Fatalf("body = %q, %v, want %q", got, err, want)
							}
						})
					}
				}
			}
		}
	}
}

func TestScrubBase64SwapsTheLastCharacterOfAValueExactly(t *testing.T) {
	value, placeholder := "sk-real-0123456789abcdefg", "sk-hib0-aaaaaaaaaaaaaaaaZ"
	sw := newSwapper(t, []swap.Secret{{
		Name: "token", Value: value, Placeholder: placeholder, Sites: []string{"example.com"},
	}})
	// In each case the value ends partway through a base64 group, so its last character also
	// holds the leading bits of the next byte. Those are zero here, as in the placeholder's last
	// character, so swapping that character leaves none of the value.
	for _, tc := range []struct {
		name          string
		enc           *base64.Encoding
		before, after string
		// bits is how many of the value's bits its last character holds.
		bits int
	}{
		{"env file", base64.StdEncoding, "TOKEN=", "\nDEBUG=1\n", 2},
		{"env file", base64.StdEncoding, "A=1\nTOKEN=", "\nDEBUG=1\n", 4},
		{"jwt payload", base64.RawURLEncoding, `{"token":"`, `","n":1}`, 4},
	} {
		t.Run(fmt.Sprintf("%s/%d bits", tc.name, tc.bits), func(t *testing.T) {
			if bits := (len(tc.before) + len(value)) % 3 * 2; bits != tc.bits {
				t.Fatalf("the value's last character holds %d of its bits, want %d", bits, tc.bits)
			}
			in := tc.enc.EncodeToString([]byte(tc.before + value + tc.after))
			header := http.Header{"X-Echo": {in}}
			sw.ScrubHeader(header)
			body, err := io.ReadAll(sw.ScrubBody(io.NopCloser(strings.NewReader(in))))
			if err != nil {
				t.Fatal(err)
			}
			// One byte per read puts a read boundary inside the value's ending and right after it.
			streamed, err := io.ReadAll(sw.ScrubBody(oneByteReadCloser{r: strings.NewReader(in)}))
			if err != nil {
				t.Fatal(err)
			}
			want := tc.before + placeholder + tc.after
			for _, got := range []struct{ name, scrubbed string }{
				{"header", header.Get("X-Echo")},
				{"body", string(body)},
				{"body read a byte at a time", string(streamed)},
			} {
				decoded, err := tc.enc.DecodeString(got.scrubbed)
				if err != nil || string(decoded) != want {
					t.Errorf("decoded %s = %q, %v, want %q", got.name, decoded, err, want)
				}
			}
		})
	}
}

func TestScrubBase64RotatedLengths(t *testing.T) {
	for _, tc := range []struct{ value, placeholder, in, want string }{
		{"long-real-value", "old-placeholder-long", "dXNlcjpsb25nLXJlYWwtdmFsdWU=", "dXNlcjpvbGQtcGxhY2Vob2xkZXItbG9uZw=="},
		{"long-real-value", "old-placeholder-long", "dXNlcjpsb25nLXJlYWwtdmFsdWU", "dXNlcjpvbGQtcGxhY2Vob2xkZXItbG9uZw"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			sw := newSwapper(t, []swap.Secret{{Name: "rotated", Value: tc.value, Placeholder: tc.placeholder, Sites: []string{"example.com"}}})
			got, err := io.ReadAll(sw.ScrubBody(oneByteReadCloser{r: strings.NewReader(tc.in)}))
			if err != nil || string(got) != tc.want {
				t.Fatalf("body = %q, %v, want %q", got, err, tc.want)
			}
		})
	}
}

func TestScrubDoesNotReplaceTinyBase64Patterns(t *testing.T) {
	sw := newSwapper(t, []swap.Secret{{Name: "short", Value: "ok", Placeholder: "placeholder", Sites: []string{"example.com"}}})
	got, err := io.ReadAll(sw.ScrubBody(io.NopCloser(strings.NewReader("ok b2s= b2s plain"))))
	if err != nil || string(got) != "placeholder b2s= b2s plain" {
		t.Fatalf("body = %q, %v", got, err)
	}
}

func TestScrubBase64EndKeepsOnlyMixedLeadingBits(t *testing.T) {
	sw := newSwapper(t, []swap.Secret{{Name: "token", Value: "A-real-value", Placeholder: "0-mask-token", Sites: []string{"example.com"}}})
	for _, tc := range []struct{ prefix, want string }{
		{"", "0-mask-token"},
		{"x", "x@-mask-token"},
		{"xy", "xyp-mask-token"},
	} {
		in := base64.StdEncoding.EncodeToString([]byte(tc.prefix + "A-real-value"))
		got, err := io.ReadAll(sw.ScrubBody(oneByteReadCloser{r: strings.NewReader(in)}))
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := base64.StdEncoding.DecodeString(string(got))
		if err != nil || string(decoded) != tc.want {
			t.Fatalf("decoded body = %q, %v, want %q", decoded, err, tc.want)
		}
	}
}
