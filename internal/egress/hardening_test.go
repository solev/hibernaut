package egress

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/audit"
	"github.com/solev/hibernaut/internal/egress/swap"
)

func TestOnlyAuthorizationReachesUpstreamAsASecret(t *testing.T) {
	t.Parallel()
	for _, auth := range []struct{ name, in, want string }{
		{"Bearer", "Bearer " + placeholder, "Bearer " + realValue},
		{"token", "token " + placeholder, "token " + realValue},
		{"Basic", "Basic " + base64.StdEncoding.EncodeToString([]byte("user:"+placeholder)),
			"Basic " + base64.StdEncoding.EncodeToString([]byte("user:"+realValue))},
		{"no credentials", "", ""},
	} {
		for _, contentType := range []string{"application/json", "application/x-www-form-urlencoded"} {
			t.Run(auth.name+"/"+contentType, func(t *testing.T) {
				body := `{"files":{"token.txt":{"content":"` + placeholder + `"}}}`
				if contentType == "application/x-www-form-urlencoded" {
					body = "content=" + placeholder
				}

				entries := make(chan audit.Entry, 2)
				fx := newFixtureConfigured(t, "localhost", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					data, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					if r.Header.Get("Authorization") != auth.want ||
						r.Header.Get("X-Content") != placeholder ||
						r.URL.Path != "/"+placeholder ||
						r.URL.RawQuery != "content="+placeholder ||
						string(data) != body {
						t.Errorf("upstream got auth %q, header %q, URL %s, body %q",
							r.Header.Get("Authorization"), r.Header.Get("X-Content"), r.URL, data)
					}
					_, _ = io.WriteString(w, "stored "+string(data))
				}), func(s *Server) {
					s.Audit = func(e audit.Entry) error { entries <- e; return nil }
				})
				defer fx.close()
				req, err := http.NewRequest(http.MethodPost, fx.url("/"+placeholder+"?content="+placeholder), strings.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Content-Type", contentType)
				req.Header.Set("Authorization", auth.in)
				req.Header.Set("X-Content", placeholder)
				resp, err := fx.client(false).Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				data, err := io.ReadAll(resp.Body)
				if err != nil || resp.StatusCode != http.StatusOK || string(data) != "stored "+body {
					t.Fatalf("response = %d %q, %v", resp.StatusCode, data, err)
				}
				if auth.in == "" {
					select {
					case e := <-entries:
						t.Fatalf("content produced an audit entry: %+v", e)
					default:
					}
					return
				}
				select {
				case e := <-entries:
					if e.Machine != "machine" || e.MachineID != "sbx" ||
						e.Method != "POST" || e.Site != "localhost" || e.Path != "/"+placeholder ||
						!slices.Equal(e.Secrets, []string{"github"}) {
						t.Fatalf("audit = %+v", e)
					}
				default:
					t.Fatal("Authorization swap has no audit entry")
				}
			})
		}
	}
}

func TestEveryBoundResponseScrubsHeadersBinaryBodyAndTrailers(t *testing.T) {
	t.Parallel()
	for _, clientHTTP2 := range []bool{false, true} {
		for _, upstreamHTTP2 := range []bool{false, true} {
			t.Run(fmt.Sprintf("client-h2=%t/upstream-h2=%t", clientHTTP2, upstreamHTTP2), func(t *testing.T) {
				sw, err := swap.New([]swap.Secret{
					{Name: secretName, Placeholder: placeholder, Value: realValue, Sites: []string{"localhost"}},
					{Name: "elsewhere", Placeholder: "other-placeholder-longer", Value: "other-real", Sites: []string{"other.test"}},
				})
				if err != nil {
					t.Fatal(err)
				}
				fx := newFixtureProtocols(t, "localhost", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					proto := "HTTP/1.1"
					if upstreamHTTP2 {
						proto = "HTTP/2.0"
					}
					if r.Proto != proto {
						t.Errorf("upstream protocol = %s, want %s", r.Proto, proto)
					}
					w.Header().Set("X-Secret", realValue+" other-real")
					w.Header().Set("Content-Type", "application/octet-stream")
					w.Header().Set("Trailer", "X-Trailer-Secret")
					_, _ = io.WriteString(w, "\x00"+realValue+"\xffother-real")
					w.(http.Flusher).Flush()
					w.Header().Set("X-Trailer-Secret", realValue+" other-real")
				}), func(s *Server) { s.Swapper = func() *swap.Swapper { return sw } }, upstreamHTTP2)
				defer fx.close()
				resp, err := fx.client(clientHTTP2).Get(fx.url("/binary-trailer"))
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				data, err := io.ReadAll(resp.Body)
				if err != nil || string(data) != "\x00"+placeholder+"\xffother-placeholder-longer" {
					t.Fatalf("body = %q, %v", data, err)
				}
				want := placeholder + " other-placeholder-longer"
				if resp.Header.Get("X-Secret") != want || resp.Trailer.Get("X-Trailer-Secret") != want {
					t.Fatalf("header = %q, trailer = %q, want %q", resp.Header.Get("X-Secret"), resp.Trailer.Get("X-Trailer-Secret"), want)
				}
				proto := "HTTP/1.1"
				if clientHTTP2 {
					proto = "HTTP/2.0"
				}
				if resp.Proto != proto {
					t.Fatalf("client protocol = %s, want %s", resp.Proto, proto)
				}
			})
		}
	}
}

func TestUninspectableResponseEncodingFailsClosed(t *testing.T) {
	t.Parallel()
	for _, encoding := range []string{"br", "zstd", "deflate", "gzip", "identity, br"} {
		for _, h2 := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/h2=%t", encoding, h2), func(t *testing.T) {
				fx := newFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Encoding", encoding)
					w.Header().Set("X-Secret", realValue)
					_, _ = io.WriteString(w, realValue)
				}))
				defer fx.close()
				req, err := http.NewRequest(http.MethodGet, fx.url("/encoded"), nil)
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Range", "bytes=0-100")
				resp, err := fx.client(h2).Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				data, err := io.ReadAll(resp.Body)
				if err != nil || resp.StatusCode != http.StatusBadGateway || string(data) != "bad gateway\n\n" || resp.Header.Get("X-Secret") != "" {
					t.Fatalf("response = %d %q, %v, headers %v", resp.StatusCode, data, err, resp.Header)
				}
			})
		}
	}
}

func TestLargeRequestStreamsBeforeClientFinishes(t *testing.T) {
	t.Parallel()
	for _, h2 := range []bool{false, true} {
		for _, knownLength := range []bool{false, true} {
			t.Run(fmt.Sprintf("h2=%t/length=%t", h2, knownLength), func(t *testing.T) {
				first := bytes.Repeat([]byte("a"), 16<<10)
				rest := strings.Repeat("b", 2<<20) + placeholder
				upstreamSawFirst := make(chan struct{})
				fx := newFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					buf := make([]byte, len(first))
					if _, err := io.ReadFull(r.Body, buf); err != nil {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
					if !bytes.Equal(buf, first) {
						t.Errorf("first request chunk = %q", buf)
					}
					close(upstreamSawFirst)
					tail, err := io.ReadAll(r.Body)
					if err != nil || string(tail) != rest {
						t.Errorf("tail changed: length %d, %v", len(tail), err)
					}
					_, _ = fmt.Fprintf(w, "received %d", len(buf)+len(tail))
				}))
				defer fx.close()
				pr, pw := io.Pipe()
				defer pr.Close()
				defer pw.Close()
				req, err := http.NewRequest(http.MethodPost, fx.url("/stream"), pr)
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Content-Type", "application/json")
				if knownLength {
					req.ContentLength = int64(len(first) + len(rest))
				}
				result := make(chan string, 1)
				client := fx.client(h2)
				client.Timeout = 10 * time.Second
				go func() {
					resp, err := client.Do(req)
					if err != nil {
						result <- err.Error()
						return
					}
					defer resp.Body.Close()
					data, err := io.ReadAll(resp.Body)
					result <- fmt.Sprintf("%d %s %s %v", resp.StatusCode, resp.Proto, data, err)
				}()
				if _, err := pw.Write(first); err != nil {
					t.Fatal(err)
				}
				select {
				case <-upstreamSawFirst:
				case <-time.After(3 * time.Second):
					_ = pw.CloseWithError(fmt.Errorf("upstream buffered request"))
					t.Fatal("upstream did not receive the first chunk before the client finished")
				}
				if _, err := io.WriteString(pw, rest); err != nil {
					t.Fatal(err)
				}
				_ = pw.Close()
				proto := "HTTP/1.1"
				if h2 {
					proto = "HTTP/2.0"
				}
				want := fmt.Sprintf("200 %s received %d <nil>", proto, len(first)+len(rest))
				if got := <-result; got != want {
					t.Fatalf("response = %q, want %q", got, want)
				}
			})
		}
	}
}

func TestBoundResponsesScrubReflectedBasicAndEncodedForms(t *testing.T) {
	t.Parallel()
	for _, h2 := range []bool{false, true} {
		for _, compressed := range []bool{false, true} {
			t.Run(fmt.Sprintf("h2=%t/gzip=%t", h2, compressed), func(t *testing.T) {
				value := `token-REAL-"\&<>%+-secret`
				placeholder := `token-FAKE-"\&<>%+-secret`
				forms := func(token string) string {
					html, _ := json.Marshal(token)
					var noHTML bytes.Buffer
					encoder := json.NewEncoder(&noHTML)
					encoder.SetEscapeHTML(false)
					if err := encoder.Encode(token); err != nil {
						t.Fatal(err)
					}
					parts := []string{token, string(html), noHTML.String(), url.QueryEscape(token)}
					for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
						for _, prefix := range []string{"", "x", "xy"} {
							parts = append(parts, enc.EncodeToString([]byte(prefix+token)))
						}
					}
					return strings.Join(parts, "\n")
				}
				sw, err := swap.New([]swap.Secret{{Name: "token", Value: value, Placeholder: placeholder, Sites: []string{"localhost"}}})
				if err != nil {
					t.Fatal(err)
				}
				fx := newFixtureConfigured(t, "localhost", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("user:"+value)) {
						t.Errorf("upstream Authorization = %q", r.Header.Get("Authorization"))
					}
					w.Header().Set("X-Echo-Authorization", r.Header.Get("Authorization"))
					w.Header().Set("Content-Type", "application/octet-stream")
					w.Header().Set("Trailer", "X-Encoded-Trailer")
					data := forms(value)
					var writer io.Writer = w
					if compressed {
						w.Header().Set("Content-Encoding", "gzip")
						gz := gzip.NewWriter(w)
						defer gz.Close()
						writer = gz
					}
					_, _ = io.WriteString(writer, data)
					w.Header().Set("X-Encoded-Trailer", base64.RawURLEncoding.EncodeToString([]byte(value)))
				}), func(s *Server) { s.Swapper = func() *swap.Swapper { return sw } })
				defer fx.close()
				req, err := http.NewRequest(http.MethodGet, fx.url("/echo-basic"), nil)
				if err != nil {
					t.Fatal(err)
				}
				req.SetBasicAuth("user", placeholder)
				resp, err := fx.client(h2).Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				data, err := io.ReadAll(resp.Body)
				if err != nil || string(data) != forms(placeholder) {
					t.Fatalf("body = %q, %v, want %q", data, err, forms(placeholder))
				}
				wantBasic := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:"+placeholder))
				if resp.Header.Get("X-Echo-Authorization") != wantBasic ||
					resp.Trailer.Get("X-Encoded-Trailer") != base64.RawURLEncoding.EncodeToString([]byte(placeholder)) {
					t.Fatalf("header = %q, trailer = %q", resp.Header.Get("X-Echo-Authorization"), resp.Trailer.Get("X-Encoded-Trailer"))
				}
			})
		}
	}
}

func TestScrubbedBinaryResponseLengthMayChange(t *testing.T) {
	t.Parallel()
	for _, h2 := range []bool{false, true} {
		t.Run(fmt.Sprintf("h2=%t", h2), func(t *testing.T) {
			sw, err := swap.New([]swap.Secret{
				{Name: "longer", Value: "first-real", Placeholder: "first-placeholder-longer", Sites: []string{"localhost"}},
				{Name: "shorter", Value: "second-real-longer", Placeholder: "second-mask", Sites: []string{"elsewhere.test"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			fx := newFixtureConfigured(t, "localhost", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body := "\x00first-real\xffsecond-real-longer"
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("Content-Length", fmt.Sprint(len(body)))
				_, _ = io.WriteString(w, body)
			}), func(s *Server) { s.Swapper = func() *swap.Swapper { return sw } })
			defer fx.close()
			resp, err := fx.client(h2).Get(fx.url("/length"))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			data, err := io.ReadAll(resp.Body)
			if err != nil || string(data) != "\x00first-placeholder-longer\xffsecond-mask" {
				t.Fatalf("body = %q, %v", data, err)
			}
			if resp.ContentLength != -1 || resp.Header.Get("Content-Length") != "" {
				t.Fatalf("scrubbed framing kept length %d, header %q", resp.ContentLength, resp.Header.Get("Content-Length"))
			}
		})
	}
}
