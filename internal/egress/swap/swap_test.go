package swap_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/egress/swap"
)

func testSecrets() []swap.Secret {
	return []swap.Secret{
		{
			Name:        "GH_TOKEN",
			Placeholder: "hib_place_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Value:       `gho_real_"tok\en&#%+`,
			Sites:       []string{"api.github.com", "*.github.com"},
		},
		{
			Name:        "OPENAI_TOKEN",
			Placeholder: "hib_place_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			Value:       "sk-real-token",
			Sites:       []string{"chatgpt.com"},
		},
		{
			Name:        "COPILOT_TOKEN",
			Placeholder: "hib_place_cccccccccccccccccccccccccccccccccccccccc",
			Value:       "copilot-real-token",
			Sites:       []string{"api.github.com"},
		},
	}
}

type testHelper interface {
	Helper()
	Fatal(args ...any)
}

func newSwapper(t testHelper, secrets []swap.Secret) *swap.Swapper {
	t.Helper()
	sw, err := swap.New(secrets)
	if err != nil {
		t.Fatal(err)
	}
	return sw
}

func TestMatch(t *testing.T) {
	for _, tc := range []struct {
		name, site, host string
		want             bool
	}{
		{name: "exact", site: "api.github.com", host: "api.github.com", want: true},
		{name: "exact ignores case and trailing dot", site: "api.github.com", host: "API.GITHUB.COM.", want: true},
		{name: "exact refuses subdomain", site: "api.github.com", host: "x.api.github.com"},
		{name: "wildcard subdomain", site: "*.github.com", host: "api.github.com", want: true},
		{name: "wildcard deep subdomain", site: "*.github.com", host: "x.y.github.com", want: true},
		{name: "wildcard refuses root", site: "*.github.com", host: "github.com"},
		{name: "wildcard refuses suffix trick", site: "*.github.com", host: "evilgithub.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := swap.Match(tc.site, tc.host); got != tc.want {
				t.Errorf("Match(%q, %q) = %v, want %v", tc.site, tc.host, got, tc.want)
			}
		})
	}
}

func TestNewValidatesWithoutLeakingSecrets(t *testing.T) {
	bad := []swap.Secret{
		{Name: "A", Placeholder: "hib_place_same", Value: "value-one", Sites: []string{"example.com"}},
		{Name: "B", Placeholder: "hib_place_same", Value: "value-two", Sites: []string{"example.com"}},
	}
	_, err := swap.New(bad)
	if err == nil {
		t.Fatal("New accepted duplicate placeholders")
	}
	msg := err.Error()
	for _, leak := range []string{"hib_place_same", "value-one", "value-two"} {
		if strings.Contains(msg, leak) {
			t.Errorf("New error leaked %q in %q", leak, msg)
		}
	}
	if !strings.Contains(msg, "A") || !strings.Contains(msg, "B") {
		t.Errorf("New error = %q, want secret names", msg)
	}
}

func TestBound(t *testing.T) {
	sw := newSwapper(t, testSecrets())
	if !sw.Bound("API.GITHUB.COM.") {
		t.Fatal("github host should be bound")
	}
	if sw.Bound("example.com") {
		t.Fatal("example.com should not be bound")
	}
}

func TestRequestSwapsHeadersAndBasicAuth(t *testing.T) {
	secrets := testSecrets()
	sw := newSwapper(t, secrets)
	auth := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + secrets[0].Placeholder))
	req, err := http.NewRequest(http.MethodPost, "https://api.github.com/graphql", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Add("X-Token", "before "+secrets[0].Placeholder)
	req.Header.Add("X-Token", secrets[1].Placeholder)
	req.Header.Set("Authorization", "basic "+auth)
	req.Header.Set("Proxy-Authorization", "Basic !!!")

	got, err := sw.Request(req, "api.github.com")
	if err != nil {
		t.Fatal(err)
	}
	wantNames := []string{"GH_TOKEN"}
	if !slices.Equal(got, wantNames) {
		t.Errorf("Request swapped %q, want %q", got, wantNames)
	}
	if values := req.Header.Values("X-Token"); !slices.Equal(values, []string{"before " + secrets[0].Value, secrets[1].Placeholder}) {
		t.Errorf("X-Token = %q", values)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(req.Header.Get("Authorization"), "basic "))
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != "x-access-token:"+secrets[0].Value {
		t.Errorf("Authorization decoded = %q", decoded)
	}
	if got := req.Header.Get("Proxy-Authorization"); got != "Basic !!!" {
		t.Errorf("invalid Basic auth changed to %q", got)
	}
}

func TestRequestSwapsQueryPerComponent(t *testing.T) {
	secrets := testSecrets()
	sw := newSwapper(t, secrets)
	escapedUpper := strings.ReplaceAll(url.QueryEscape(secrets[0].Placeholder), "%3A", "%3a")
	raw := "keep=%2f&" +
		secrets[0].Placeholder + "=raw&" +
		"escaped=" + escapedUpper + "&" +
		"lower=%68%69%62_place_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa&" +
		"other=" + secrets[1].Placeholder
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/search?"+raw, nil)
	if err != nil {
		t.Fatal(err)
	}

	got, err := sw.Request(req, "api.github.com")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"GH_TOKEN"}) {
		t.Errorf("Request swapped %q", got)
	}
	wantValue := url.QueryEscape(secrets[0].Value)
	want := "keep=%2f&" +
		wantValue + "=raw&" +
		"escaped=" + wantValue + "&" +
		"lower=" + wantValue + "&" +
		"other=" + secrets[1].Placeholder
	if req.URL.RawQuery != want {
		t.Errorf("RawQuery = %q, want %q", req.URL.RawQuery, want)
	}
}

func TestRequestDoesNotSwapPath(t *testing.T) {
	secrets := testSecrets()
	sw := newSwapper(t, secrets)
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/"+secrets[0].Placeholder+"?q="+secrets[0].Placeholder, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sw.Request(req, "api.github.com"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(req.URL.Path, secrets[0].Placeholder) {
		t.Errorf("path was swapped to %q", req.URL.Path)
	}
	if strings.Contains(req.URL.RawQuery, secrets[0].Placeholder) {
		t.Errorf("query was not swapped: %q", req.URL.RawQuery)
	}
}

func TestRequestSwapsBodies(t *testing.T) {
	secrets := testSecrets()
	for _, tc := range []struct {
		name        string
		contentType string
		in          string
		want        string
	}{
		{
			name:        "json",
			contentType: "application/json",
			in:          `{"token":"` + secrets[0].Placeholder + `"}`,
			want:        `{"token":"gho_real_\"tok\\en&#%+"}`,
		},
		{
			name:        "json suffix",
			contentType: "application/vnd.api+json; charset=utf-8",
			in:          `{"token":"` + secrets[0].Placeholder + `"}`,
			want:        `{"token":"gho_real_\"tok\\en&#%+"}`,
		},
		{
			name:        "form",
			contentType: "application/x-www-form-urlencoded",
			in:          "token=" + secrets[0].Placeholder + "&keep=%2f",
			want:        "token=" + url.QueryEscape(secrets[0].Value) + "&keep=%2f",
		},
		{
			name:        "text",
			contentType: "text/plain",
			in:          "token=" + secrets[0].Placeholder,
			want:        "token=" + secrets[0].Value,
		},
		{
			name:        "chunked",
			contentType: "text/plain",
			in:          "chunk " + secrets[0].Placeholder,
			want:        "chunk " + secrets[0].Value,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sw := newSwapper(t, secrets)
			req, err := http.NewRequest(http.MethodPost, "https://api.github.com/upload", io.NopCloser(strings.NewReader(tc.in)))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", tc.contentType)
			if tc.name == "chunked" {
				req.ContentLength = -1
			} else {
				req.ContentLength = int64(len(tc.in))
				req.Header.Set("Content-Length", "999")
			}
			got, err := sw.Request(req, "api.github.com")
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, []string{"GH_TOKEN"}) {
				t.Errorf("Request swapped %q", got)
			}
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != tc.want {
				t.Errorf("body = %q, want %q", body, tc.want)
			}
			if req.ContentLength != int64(len(tc.want)) {
				t.Errorf("ContentLength = %d, want %d", req.ContentLength, len(tc.want))
			}
			if got := req.Header.Get("Content-Length"); got != "" {
				t.Errorf("Content-Length header = %q", got)
			}
			again, err := req.GetBody()
			if err != nil {
				t.Fatal(err)
			}
			body, err = io.ReadAll(again)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != tc.want {
				t.Errorf("GetBody body = %q, want %q", body, tc.want)
			}
			if strings.HasPrefix(tc.contentType, "application/json") || strings.Contains(tc.contentType, "+json") {
				var js map[string]string
				if err := json.Unmarshal(body, &js); err != nil {
					t.Errorf("swapped JSON is invalid: %v", err)
				}
			}
		})
	}
}

func TestRequestLeavesLargeAndUnsupportedBodiesStreaming(t *testing.T) {
	secrets := testSecrets()
	sw := newSwapper(t, secrets)
	large := strings.Repeat("a", 1<<20) + secrets[0].Placeholder
	req, err := http.NewRequest(http.MethodPost, "https://api.github.com/upload", io.NopCloser(strings.NewReader(large)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "text/plain")
	req.ContentLength = int64(len(large))
	if got, err := sw.Request(req, "api.github.com"); err != nil || len(got) != 0 {
		t.Fatalf("Request large = %q, %v", got, err)
	}
	if req.ContentLength != int64(len(large)) {
		t.Errorf("ContentLength = %d, want %d", req.ContentLength, len(large))
	}
	if req.GetBody != nil {
		t.Error("GetBody set for large body")
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != large {
		t.Error("large body changed")
	}

	unread := &failReader{}
	req, err = http.NewRequest(http.MethodPost, "https://api.github.com/upload", unread)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "gzip")
	if got, err := sw.Request(req, "api.github.com"); err != nil || len(got) != 0 {
		t.Fatalf("Request unsupported = %q, %v", got, err)
	}
	if unread.read {
		t.Error("unsupported body was read")
	}
}

type failReader struct{ read bool }

func (r *failReader) Read([]byte) (int, error) {
	r.read = true
	return 0, errors.New("should not read")
}

func (r *failReader) Close() error { return nil }

func TestRequestBodyReadError(t *testing.T) {
	sw := newSwapper(t, testSecrets())
	req, err := http.NewRequest(http.MethodPost, "https://api.github.com/upload", &failReader{})
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "text/plain")
	if _, err := sw.Request(req, "api.github.com"); err == nil {
		t.Fatal("Request returned nil error for body read error")
	}
}

func TestScrubHeaderAndBody(t *testing.T) {
	secrets := []swap.Secret{
		{Name: "A_TOKEN", Placeholder: "hib_place_a", Value: "same-real-value", Sites: []string{"a.example.com"}},
		{Name: "B_TOKEN", Placeholder: "hib_place_b", Value: "same-real-value", Sites: []string{"b.example.com"}},
		{Name: "C_TOKEN", Placeholder: "hib_place_c", Value: `needs "json"\and&<>%url+escape`, Sites: []string{"c.example.com"}},
	}
	sw := newSwapper(t, secrets)
	header := http.Header{"X-Token": []string{"before same-real-value after"}}
	sw.ScrubHeader(header)
	if got := header.Get("X-Token"); got != "before hib_place_a after" {
		t.Errorf("ScrubHeader = %q", got)
	}

	encoded := url.QueryEscape(secrets[2].Value)
	quoted, err := json.Marshal(secrets[2].Value)
	if err != nil {
		t.Fatal(err)
	}
	in := "raw " + secrets[2].Value + " json " + strings.Trim(string(quoted), `"`) + " url " + encoded + " same same-real-value"
	want := "raw " + secrets[2].Placeholder + " json " + secrets[2].Placeholder + " url " + secrets[2].Placeholder + " same " + secrets[0].Placeholder
	got, err := io.ReadAll(sw.ScrubBody(oneByteReadCloser{r: strings.NewReader(in)}))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("ScrubBody = %q, want %q", got, want)
	}
}

func TestScrubBodyDoesNotDelayWhenThereIsNoPossiblePrefix(t *testing.T) {
	sw := newSwapper(t, testSecrets())
	pr, pw := io.Pipe()
	body := sw.ScrubBody(pr)
	done := make(chan string, 1)
	go func() {
		buf := make([]byte, len("data: x\n\n"))
		n, err := io.ReadFull(body, buf)
		if err != nil {
			done <- "error: " + err.Error()
			return
		}
		done <- string(buf[:n])
	}()
	if _, err := pw.Write([]byte("data: x\n\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got != "data: x\n\n" {
			t.Fatalf("first read = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("ScrubBody waited for more source input")
	}
	if err := pw.Close(); err != nil {
		t.Fatal(err)
	}
}

type oneByteReadCloser struct{ r *strings.Reader }

func (r oneByteReadCloser) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	var b [1]byte
	n, err := r.r.Read(b[:])
	if n > 0 {
		p[0] = b[0]
	}
	return n, err
}

func (r oneByteReadCloser) Close() error { return nil }

func TestText(t *testing.T) {
	for _, ct := range []string{
		"application/json",
		"application/vnd.api+json",
		"application/x-www-form-urlencoded",
		"text/event-stream",
		"application/xml",
		"application/javascript",
	} {
		if !swap.Text(ct) {
			t.Errorf("Text(%q) = false", ct)
		}
	}
	for _, ct := range []string{"application/octet-stream", "application/gzip", ""} {
		if swap.Text(ct) {
			t.Errorf("Text(%q) = true", ct)
		}
	}
}

func FuzzRequest(f *testing.F) {
	secrets := []swap.Secret{
		{Name: "A", Placeholder: "hib_place_Aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Value: "real-A-token", Sites: []string{"a.example.com"}},
		{Name: "B", Placeholder: "hib_place_Bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Value: "real-B-token", Sites: []string{"b.example.com"}},
	}
	for _, seed := range []string{"", "plain", secrets[0].Placeholder, url.QueryEscape(secrets[0].Placeholder)} {
		f.Add("a.example.com", seed, seed, seed, "text/plain")
	}
	f.Fuzz(func(t *testing.T, host, query, header, body, contentType string) {
		if host != "a.example.com" && host != "b.example.com" {
			host = "evil.example.com"
		}
		sw := newSwapper(t, secrets)
		req, err := http.NewRequest(http.MethodPost, "https://"+host+"/path?x="+url.QueryEscape(query), io.NopCloser(strings.NewReader(body)))
		if err != nil {
			t.Skip()
		}
		req.Header.Set("X-Test", header)
		req.Header.Set("Content-Type", contentType)
		before := requestText(req)
		if _, err := sw.Request(req, host); err != nil {
			t.Skip()
		}
		after := requestText(req)
		for _, s := range secrets {
			if !swap.Match(s.Sites[0], host) && strings.Count(after, s.Value) > strings.Count(before, s.Value) {
				t.Fatalf("unbound value for %s appeared", s.Name)
			}
			if swap.Match(s.Sites[0], host) {
				if strings.Contains(req.Header.Get("X-Test"), s.Placeholder) {
					t.Fatalf("bound header placeholder for %s remains", s.Name)
				}
				if strings.Contains(req.URL.RawQuery, s.Placeholder) {
					t.Fatalf("bound query placeholder for %s remains", s.Name)
				}
				if strings.HasPrefix(contentType, "text/") && len(body) <= 1<<20 {
					bodyAfter, _ := io.ReadAll(req.Body)
					req.Body = io.NopCloser(bytes.NewReader(bodyAfter))
					if strings.Contains(string(bodyAfter), s.Placeholder) {
						t.Fatalf("bound body placeholder for %s remains", s.Name)
					}
				}
				for _, other := range secrets {
					if other.Name != s.Name && strings.Contains(after, other.Value) && !strings.Contains(before, other.Value) {
						t.Fatalf("other value for %s appeared", other.Name)
					}
				}
			}
		}
	})
}

func requestText(req *http.Request) string {
	var b strings.Builder
	b.WriteString(req.URL.RawQuery)
	for _, values := range req.Header {
		for _, value := range values {
			b.WriteString(value)
		}
	}
	if req.Body != nil {
		data, _ := io.ReadAll(req.Body)
		b.Write(data)
		req.Body = io.NopCloser(bytes.NewReader(data))
	}
	return b.String()
}

func FuzzScrub(f *testing.F) {
	secrets := testSecrets()
	f.Add("plain")
	f.Add("before " + secrets[0].Value + " after")
	f.Add(url.QueryEscape(secrets[0].Value))
	f.Fuzz(func(t *testing.T, in string) {
		sw := newSwapper(t, secrets)
		want := referenceScrub(in, secrets)
		got, err := io.ReadAll(sw.ScrubBody(oneByteReadCloser{r: strings.NewReader(in)}))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("chunked scrub = %q, want %q", got, want)
		}
		for _, s := range secrets {
			if strings.Contains(string(got), s.Value) {
				t.Fatalf("output contains value for %s", s.Name)
			}
		}
	})
}

func referenceScrub(in string, secrets []swap.Secret) string {
	type pat struct{ from, to string }
	first := map[string]string{}
	ordered := slices.Clone(secrets)
	slices.SortFunc(ordered, func(a, b swap.Secret) int {
		return strings.Compare(a.Name, b.Name)
	})
	for _, s := range ordered {
		if _, ok := first[s.Value]; !ok {
			first[s.Value] = s.Placeholder
		}
	}
	var pats []pat
	seen := map[string]bool{}
	for _, s := range secrets {
		quoted, _ := json.Marshal(s.Value)
		for _, form := range []string{
			s.Value,
			strconvQuoteContent(s.Value),
			strings.Trim(string(quoted), `"`),
			url.QueryEscape(s.Value),
		} {
			if !seen[form] {
				pats = append(pats, pat{from: form, to: first[s.Value]})
				seen[form] = true
			}
		}
	}
	var out strings.Builder
	for i := 0; i < len(in); {
		var match pat
		for _, p := range pats {
			if len(p.from) > len(match.from) && strings.HasPrefix(in[i:], p.from) {
				match = p
			}
		}
		if match.from != "" {
			out.WriteString(match.to)
			i += len(match.from)
			continue
		}
		out.WriteByte(in[i])
		i++
	}
	return out.String()
}

func strconvQuoteContent(s string) string {
	quoted := strconv.Quote(s)
	return quoted[1 : len(quoted)-1]
}

func BenchmarkScrubBody(b *testing.B) {
	secrets := benchSecrets(20)
	sw := newSwapper(b, secrets)
	chunk := strings.Repeat("x", 1024)
	var src strings.Builder
	for src.Len() < 16<<20 {
		src.WriteString(chunk)
		src.WriteString(secrets[src.Len()%len(secrets)].Value)
	}
	data := src.String()[:16<<20]
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for b.Loop() {
		r := sw.ScrubBody(io.NopCloser(strings.NewReader(data)))
		if _, err := io.Copy(io.Discard, r); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRequest(b *testing.B) {
	secrets := benchSecrets(20)
	sw := newSwapper(b, secrets)
	body := strings.Repeat("hello ", (16<<20)/len("hello "))
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for b.Loop() {
		req, err := http.NewRequest(http.MethodPost, "https://api.example.com/path?q="+secrets[0].Placeholder, io.NopCloser(strings.NewReader(body)))
		if err != nil {
			b.Fatal(err)
		}
		req.Header.Set("Content-Type", "text/plain")
		if _, err := sw.Request(req, "api.example.com"); err != nil {
			b.Fatal(err)
		}
	}
}

func benchSecrets(n int) []swap.Secret {
	secrets := make([]swap.Secret, n)
	for i := range secrets {
		secrets[i] = swap.Secret{
			Name:        "TOKEN_" + string(rune('A'+i)),
			Placeholder: "hib_place_" + strings.Repeat(string(rune('a'+i)), 40),
			Value:       "real_value_" + strings.Repeat(string(rune('A'+i)), 30),
			Sites:       []string{"api.example.com"},
		}
	}
	return secrets
}
