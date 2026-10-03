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
			for _, suffix := range []string{"", "!tail"} {
				for extra := range 3 {
					t.Run(fmt.Sprintf("%s/prefix=%d/suffix=%d/length=%d", encoding.name, len(prefix), len(suffix), extra), func(t *testing.T) {
						value := "\xfb\xff-token-REAL-secret" + strings.Repeat("z", extra)
						placeholder := "\xfb\xff-token-FAKE-secret" + strings.Repeat("z", extra)
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

func TestScrubBase64EndReplacesPartialGroups(t *testing.T) {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		for _, prefix := range []string{"", "x", "xy"} {
			for extra := range 3 {
				value := "token-real-secret" + strings.Repeat("z", extra)
				placeholder := "token-fake-masked" + strings.Repeat("a", extra)
				sw := newSwapper(t, []swap.Secret{{Name: "token", Value: value, Placeholder: placeholder, Sites: []string{"example.com"}}})
				in := enc.EncodeToString([]byte(prefix + value))
				want := enc.EncodeToString([]byte(prefix + placeholder))
				got, err := io.ReadAll(sw.ScrubBody(oneByteReadCloser{r: strings.NewReader(in)}))
				if err != nil || string(got) != want {
					t.Fatalf("scrub %q = %q, %v, want %q", in, got, err, want)
				}
			}
		}
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
