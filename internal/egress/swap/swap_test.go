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

func TestBoundMatchesSites(t *testing.T) {
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
			sw := newSwapper(t, []swap.Secret{{
				Name:        "TOKEN",
				Placeholder: "placeholder",
				Value:       "value",
				Sites:       []string{tc.site},
			}})
			if got := sw.Bound(tc.host); got != tc.want {
				t.Errorf("Bound(%q) with site %q = %v, want %v", tc.host, tc.site, got, tc.want)
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

func TestRequestSwapsOnlyAuthorizationAndBasicAuth(t *testing.T) {
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
	if values := req.Header.Values("X-Token"); !slices.Equal(values, []string{"before " + secrets[0].Placeholder, secrets[1].Placeholder}) {
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

func TestRequestKeepsMalformedBasicAuthorization(t *testing.T) {
	secrets := testSecrets()
	sw := newSwapper(t, secrets)
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/user", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Basic "+secrets[0].Placeholder)
	names, err := sw.Request(req, "api.github.com")
	if err != nil || len(names) != 0 || req.Header.Get("Authorization") != "Basic "+secrets[0].Placeholder {
		t.Fatalf("malformed Basic = %q, names %v, %v", req.Header.Get("Authorization"), names, err)
	}
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("user:"+secrets[0].Placeholder)))
	names, err = sw.Request(req, "api.github.com")
	if err != nil || !slices.Equal(names, []string{"GH_TOKEN"}) ||
		req.Header.Get("Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("user:"+secrets[0].Value)) {
		t.Fatalf("valid Basic = %q, names %v, %v", req.Header.Get("Authorization"), names, err)
	}
}

func TestRequestKeepsQueryPerComponent(t *testing.T) {
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
	req.Header.Set("Authorization", "Bearer "+secrets[0].Placeholder)

	got, err := sw.Request(req, "api.github.com")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"GH_TOKEN"}) || req.Header.Get("Authorization") != "Bearer "+secrets[0].Value {
		t.Errorf("Request swapped %q", got)
	}
	want := raw
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
	req.Header.Set("Authorization", "token "+secrets[0].Placeholder)
	if names, err := sw.Request(req, "api.github.com"); err != nil || !slices.Equal(names, []string{"GH_TOKEN"}) {
		t.Fatalf("Request = %v, %v", names, err)
	}
	if req.Header.Get("Authorization") != "token "+secrets[0].Value {
		t.Fatalf("Authorization = %q", req.Header.Get("Authorization"))
	}
	if !strings.Contains(req.URL.Path, secrets[0].Placeholder) {
		t.Errorf("path was swapped to %q", req.URL.Path)
	}
	if req.URL.RawQuery != "q="+secrets[0].Placeholder {
		t.Errorf("query changed: %q", req.URL.RawQuery)
	}
}

func TestRequestKeepsBodies(t *testing.T) {
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
			want:        `{"token":"` + secrets[0].Placeholder + `"}`,
		},
		{
			name:        "json suffix",
			contentType: "application/vnd.api+json; charset=utf-8",
			in:          `{"token":"` + secrets[0].Placeholder + `"}`,
			want:        `{"token":"` + secrets[0].Placeholder + `"}`,
		},
		{
			name:        "form",
			contentType: "application/x-www-form-urlencoded",
			in:          "token=" + secrets[0].Placeholder + "&keep=%2f",
			want:        "token=" + secrets[0].Placeholder + "&keep=%2f",
		},
		{
			name:        "text",
			contentType: "text/plain",
			in:          "token=" + secrets[0].Placeholder,
			want:        "token=" + secrets[0].Placeholder,
		},
		{
			name:        "chunked",
			contentType: "text/plain",
			in:          "chunk " + secrets[0].Placeholder,
			want:        "chunk " + secrets[0].Placeholder,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sw := newSwapper(t, secrets)
			req, err := http.NewRequest(http.MethodPost, "https://api.github.com/upload", io.NopCloser(strings.NewReader(tc.in)))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", tc.contentType)
			req.Header.Set("Authorization", "Bearer "+secrets[0].Placeholder)
			if tc.name == "chunked" {
				req.ContentLength = -1
			} else {
				req.ContentLength = int64(len(tc.in))
			}
			got, err := sw.Request(req, "api.github.com")
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, []string{"GH_TOKEN"}) || req.Header.Get("Authorization") != "Bearer "+secrets[0].Value {
				t.Errorf("Request swapped %q", got)
			}
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != tc.want {
				t.Errorf("body = %q, want %q", body, tc.want)
			}
			wantLength := int64(len(tc.in))
			if tc.name == "chunked" {
				wantLength = -1
			}
			if req.ContentLength != wantLength {
				t.Errorf("ContentLength = %d, want %d", req.ContentLength, wantLength)
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
	req.Header.Set("Authorization", "token "+secrets[0].Placeholder)
	req.ContentLength = int64(len(large))
	if got, err := sw.Request(req, "api.github.com"); err != nil || !slices.Equal(got, []string{"GH_TOKEN"}) {
		t.Fatalf("Request large = %q, %v", got, err)
	}
	if req.Header.Get("Authorization") != "token "+secrets[0].Value {
		t.Fatalf("Authorization = %q", req.Header.Get("Authorization"))
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
	if _, err := io.ReadAll(req.Body); err == nil {
		t.Error("transport did not receive the unsupported body's error")
	}
}

type failReader struct{}

func (*failReader) Read([]byte) (int, error) {
	return 0, errors.New("should not read")
}

func (r *failReader) Close() error { return nil }

func TestRequestDefersBodyReadErrorsToTransport(t *testing.T) {
	sw := newSwapper(t, testSecrets())
	req, err := http.NewRequest(http.MethodPost, "https://api.github.com/upload", &failReader{})
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Authorization", "token "+testSecrets()[0].Placeholder)
	if names, err := sw.Request(req, "api.github.com"); err != nil || !slices.Equal(names, []string{"GH_TOKEN"}) {
		t.Fatalf("Request = %v, %v", names, err)
	}
	if got := req.Header.Get("Authorization"); got != "token "+testSecrets()[0].Value {
		t.Fatalf("Authorization = %q", got)
	}
	if _, err := io.ReadAll(req.Body); err == nil {
		t.Fatal("transport did not receive the body read error")
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

func TestScrubBodyScrubsValueSplitBeforeSuffixThatIsAPrefix(t *testing.T) {
	value := "gho_" + strings.Repeat("Z", 31) + "g"
	placeholder := "gho_" + strings.Repeat("h", 32)
	sw := newSwapper(t, []swap.Secret{{
		Name:        "gh",
		Placeholder: placeholder,
		Value:       value,
		Sites:       []string{"api.github.com"},
	}})
	body := sw.ScrubBody(&chunkReadCloser{chunks: []string{`{"token":"` + value, `"}`}})
	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"token":"` + placeholder + `"}`
	if string(got) != want {
		t.Fatalf("ScrubBody = %q, want %q", got, want)
	}
}

func TestScrubBodyWaitsForLongestMatchAcrossBoundary(t *testing.T) {
	secrets := []swap.Secret{
		{Name: "short", Placeholder: "short-placeholder", Value: "token-prefix", Sites: []string{"api.github.com"}},
		{Name: "long", Placeholder: "long-placeholder", Value: "token-prefix-long", Sites: []string{"api.github.com"}},
	}
	sw := newSwapper(t, secrets)
	body := sw.ScrubBody(&chunkReadCloser{chunks: []string{`{"token":"token-prefix`, `-long"}`}})
	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"token":"long-placeholder"}`
	if string(got) != want {
		t.Fatalf("ScrubBody = %q, want %q", got, want)
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

type chunkReadCloser struct {
	chunks []string
}

func (r *chunkReadCloser) Read(p []byte) (int, error) {
	if len(r.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.chunks[0])
	r.chunks[0] = r.chunks[0][n:]
	if r.chunks[0] == "" {
		r.chunks = r.chunks[1:]
	}
	return n, nil
}

func (r *chunkReadCloser) Close() error { return nil }

type sizedReadCloser struct {
	data  string
	sizes []byte
	pos   int
	step  int
}

func newSizedReadCloser(data string, sizes []byte) *sizedReadCloser {
	if len(sizes) == 0 {
		sizes = []byte{1}
	}
	return &sizedReadCloser{data: data, sizes: sizes}
}

func (r *sizedReadCloser) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	size := int(r.sizes[r.step%len(r.sizes)])
	r.step++
	if size == 0 {
		size = 1
	}
	size = min(size, len(p), len(r.data)-r.pos)
	n := copy(p, r.data[r.pos:r.pos+size])
	r.pos += n
	return n, nil
}

func (r *sizedReadCloser) Close() error { return nil }

func FuzzRequest(f *testing.F) {
	secrets := []swap.Secret{
		{Name: "A", Placeholder: "hib_place_Aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Value: "real-A-token", Sites: []string{"a.example.com"}},
		{Name: "B", Placeholder: "hib_place_Bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Value: "real-B-token", Sites: []string{"b.example.com"}},
	}
	for _, host := range []string{"a.example.com", "b.example.com", "unbound.example.com"} {
		for _, seed := range []string{"", "plain", secrets[0].Placeholder, secrets[1].Placeholder,
			secrets[0].Placeholder + " " + secrets[1].Placeholder, url.QueryEscape(secrets[0].Placeholder)} {
			f.Add(host, seed, seed, seed, "text/plain")
		}
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
		req.Header.Set("Authorization", "Bearer "+header)
		wantAuth := "Bearer " + header
		var wantNames []string
		siteSecrets := map[string]int{"a.example.com": 0, "b.example.com": 1}
		if index, bound := siteSecrets[host]; bound {
			s := secrets[index]
			wantAuth = strings.ReplaceAll(wantAuth, s.Placeholder, s.Value)
			if strings.Contains(header, s.Placeholder) {
				wantNames = []string{s.Name}
			}
		}
		names, err := sw.Request(req, host)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(req.Body)
		if err != nil || string(data) != body || req.Header.Get("X-Test") != header ||
			req.Header.Get("Authorization") != wantAuth || req.URL.RawQuery != "x="+url.QueryEscape(query) ||
			!slices.Equal(names, wantNames) {
			t.Fatalf("request = auth %q, header %q, query %q, body %q, names %v, error %v",
				req.Header.Get("Authorization"), req.Header.Get("X-Test"), req.URL.RawQuery, data, names, err)
		}
	})
}

func FuzzScrub(f *testing.F) {
	f.Add("plain", []byte{1})
	f.Add("base64", []byte{1, 2, 3})
	f.Add("url", []byte{4, 1})
	f.Fuzz(func(t *testing.T, in string, sizes []byte) {
		value := `token-REAL-"<>-secret`
		placeholder := `token-FAKE-"<>-secret`
		sw := newSwapper(t, []swap.Secret{{Name: "token", Value: value, Placeholder: placeholder, Sites: []string{"example.com"}}})
		prefix := strings.Repeat("x", len(in)%3)
		enc := []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding}[len(in)%4]
		forms := func(token string) string {
			quoted, _ := json.Marshal(token)
			return token + "\n" + string(quoted) + "\n" + jsonContentNoHTML(token) + "\n" +
				url.QueryEscape(token) + "\n" + enc.EncodeToString([]byte(prefix+token)) + "\n"
		}
		want := forms(placeholder)
		got, err := io.ReadAll(sw.ScrubBody(newSizedReadCloser(forms(value), sizes)))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("chunked scrub = %q, want %q", got, want)
		}
	})
}

func jsonContentNoHTML(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return ""
	}
	quoted := strings.TrimSuffix(b.String(), "\n")
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
