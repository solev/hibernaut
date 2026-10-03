// Package swap replaces machine placeholders with host secrets for bound sites, and scrubs
// host secrets out of responses.
package swap

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/solev/hibernaut/internal/secretencoding"
)

// Secret is one host-held secret and the sites where its real value may be sent.
type Secret struct {
	Name, Placeholder, Value string
	Sites                    []string
}

// Swapper swaps placeholders in requests and scrubs real values from responses.
type Swapper struct {
	secrets    []compiledSecret
	scrub      []pattern
	scrubIndex [256][]pattern
}

type compiledSecret struct {
	Secret
	jsonValue string
	sites     []siteMatcher
}

type siteMatcher struct {
	name     string
	wildcard bool
}

type pattern struct {
	from     []byte
	to       []byte
	terminal bool
}

// New validates secrets and precomputes request and response matchers.
func New(secrets []Secret) (*Swapper, error) {
	names := map[string]string{}
	placeholders := map[string]string{}
	for i, s := range secrets {
		if s.Name == "" {
			return nil, fmt.Errorf("secret at index %d has no name", i)
		}
		if s.Placeholder == "" {
			return nil, fmt.Errorf("secret %s has no placeholder", s.Name)
		}
		if s.Value == "" {
			return nil, fmt.Errorf("secret %s has no value", s.Name)
		}
		if len(s.Sites) == 0 {
			return nil, fmt.Errorf("secret %s has no sites", s.Name)
		}
		if other := names[s.Name]; other != "" {
			return nil, fmt.Errorf("secret %s has the same name as secret %s", s.Name, other)
		}
		names[s.Name] = s.Name
		if other := placeholders[s.Placeholder]; other != "" {
			return nil, fmt.Errorf("secret %s has the same placeholder as secret %s", s.Name, other)
		}
		placeholders[s.Placeholder] = s.Name
	}
	for i, a := range secrets {
		for j, b := range secrets {
			if i != j && (strings.Contains(a.Placeholder, b.Placeholder) || strings.Contains(b.Placeholder, a.Placeholder)) {
				return nil, fmt.Errorf("secret %s placeholder overlaps secret %s placeholder", a.Name, b.Name)
			}
			if strings.Contains(a.Placeholder, b.Value) || strings.Contains(b.Value, a.Placeholder) {
				return nil, fmt.Errorf("secret %s placeholder overlaps secret %s value", a.Name, b.Name)
			}
		}
	}

	sw := &Swapper{}
	for _, s := range secrets {
		cs := compiledSecret{Secret: s, jsonValue: jsonStringContent(s.Value)}
		for _, site := range s.Sites {
			m, err := compileSite(site)
			if err != nil {
				return nil, fmt.Errorf("secret %s has bad site: %w", s.Name, err)
			}
			cs.sites = append(cs.sites, m)
		}
		sw.secrets = append(sw.secrets, cs)
	}
	sw.scrub = scrubPatterns(sw.secrets)
	sw.scrubIndex = indexPatterns(sw.scrub)
	return sw, nil
}

func compileSite(site string) (siteMatcher, error) {
	site = normalizeHost(site)
	if site == "" {
		return siteMatcher{}, errors.New("empty site")
	}
	if strings.HasPrefix(site, "*.") {
		site = strings.TrimPrefix(site, "*.")
		if site == "" || strings.Contains(site, "*") {
			return siteMatcher{}, errors.New("bad wildcard site")
		}
		return siteMatcher{name: site, wildcard: true}, nil
	}
	if strings.Contains(site, "*") {
		return siteMatcher{}, errors.New("bad wildcard site")
	}
	return siteMatcher{name: site}, nil
}

func (m siteMatcher) match(host string) bool {
	host = normalizeHost(host)
	if !m.wildcard {
		return host == m.name
	}
	return strings.HasSuffix(host, "."+m.name)
}

func normalizeHost(host string) string {
	host = strings.TrimSpace(host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	return host
}

// Bound reports whether any secret may be sent to host.
func (sw *Swapper) Bound(host string) bool {
	return len(sw.bound(host)) > 0
}

func (sw *Swapper) bound(host string) []compiledSecret {
	var out []compiledSecret
	for _, s := range sw.secrets {
		if slices.ContainsFunc(s.sites, func(m siteMatcher) bool { return m.match(host) }) {
			out = append(out, s)
		}
	}
	return out
}

// Request swaps Authorization placeholders for secrets bound to host.
// Bodies and all other request fields remain untouched. It returns the swapped secret names.
func (sw *Swapper) Request(r *http.Request, host string) ([]string, error) {
	bound := sw.bound(host)
	if len(bound) == 0 {
		return nil, nil
	}
	swapped := map[string]bool{}
	for key, values := range r.Header {
		if !strings.EqualFold(key, "Authorization") {
			continue
		}
		for i, value := range values {
			values[i] = swapAuthorization(value, bound, swapped)
		}
	}
	return sortedNames(swapped), nil
}

func swapAuthorization(value string, secrets []compiledSecret, swapped map[string]bool) string {
	scheme, token, ok := strings.Cut(value, " ")
	if !ok || !strings.EqualFold(scheme, "Basic") {
		return replacePlaceholders(value, secrets, swapped)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(token))
	if err != nil {
		return value
	}
	next := replacePlaceholders(string(decoded), secrets, swapped)
	return scheme + " " + base64.StdEncoding.EncodeToString([]byte(next))
}

func replacePlaceholders(in string, secrets []compiledSecret, swapped map[string]bool) string {
	out := in
	for _, s := range secrets {
		if strings.Contains(out, s.Placeholder) {
			out = strings.ReplaceAll(out, s.Placeholder, s.Value)
			swapped[s.Name] = true
		}
	}
	return out
}

// ScrubHeader replaces real values in h with placeholders.
func (sw *Swapper) ScrubHeader(h http.Header) {
	for _, values := range h {
		for i, value := range values {
			values[i] = string(replaceBytes([]byte(value), sw.scrubIndex))
		}
	}
}

// ScrubBody returns a stream that replaces real values with placeholders.
func (sw *Swapper) ScrubBody(body io.ReadCloser) io.ReadCloser {
	return &scrubReader{src: body, first: sw.scrubIndex, buf: make([]byte, 0, 32<<10), out: make([]byte, 0, 32<<10)}
}

type scrubReader struct {
	src   io.ReadCloser
	first [256][]pattern
	buf   []byte
	out   []byte
	sent  int
	eof   bool
	err   error
}

func (r *scrubReader) Read(p []byte) (int, error) {
	for r.sent == len(r.out) && r.err == nil {
		r.fill()
	}
	if r.sent < len(r.out) {
		n := copy(p, r.out[r.sent:])
		r.sent += n
		return n, nil
	}
	return 0, r.err
}

func (r *scrubReader) Close() error {
	return r.src.Close()
}

func (r *scrubReader) fill() {
	if !r.eof {
		if cap(r.buf)-len(r.buf) < 32<<10 {
			next := make([]byte, len(r.buf), len(r.buf)+(32<<10))
			copy(next, r.buf)
			r.buf = next
		}
		n, err := r.src.Read(r.buf[len(r.buf):cap(r.buf)])
		r.buf = r.buf[:len(r.buf)+n]
		if err != nil {
			r.eof = true
			if err != io.EOF {
				r.err = err
			}
		}
	}
	var consumed int
	r.out, consumed = scanScrub(r.out[:0], r.buf, r.eof, r.first)
	r.sent = 0
	if consumed > 0 {
		copy(r.buf, r.buf[consumed:])
		r.buf = r.buf[:len(r.buf)-consumed]
		return
	}
	if r.eof && len(r.buf) == 0 && r.err == nil {
		r.err = io.EOF
	}
}

func replaceBytes(in []byte, first [256][]pattern) []byte {
	out, _ := scanScrub(make([]byte, 0, len(in)), in, true, first)
	return out
}

func scanScrub(out, in []byte, eof bool, first [256][]pattern) ([]byte, int) {
	for i := 0; i < len(in); {
		var match pattern
		for _, p := range first[in[i]] {
			if !bytes.HasPrefix(in[i:], p.from) {
				continue
			}
			next := i + len(p.from)
			if p.terminal && next < len(in) && base64Byte(in[next]) {
				continue
			}
			if len(p.from) > len(match.from) || len(p.from) == len(match.from) && p.terminal {
				match = p
			}
		}
		if !eof && possibleLongerMatch(in[i:], len(match.from), first[in[i]]) {
			return out, i
		}
		if match.from != nil {
			out = append(out, match.to...)
			i += len(match.from)
			continue
		}
		out = append(out, in[i])
		i++
	}
	return out, len(in)
}

func possibleLongerMatch(rest []byte, matched int, patterns []pattern) bool {
	for _, p := range patterns {
		if len(p.from) > len(rest) && len(p.from) > matched && bytes.HasPrefix(p.from, rest) {
			return true
		}
		if p.terminal && len(p.from) == len(rest) && bytes.Equal(p.from, rest) {
			return true
		}
	}
	return false
}

func base64Byte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' ||
		b == '+' || b == '/' || b == '-' || b == '_' || b == '='
}

func scrubPatterns(secrets []compiledSecret) []pattern {
	firstByValue := map[string]compiledSecret{}
	order := slices.Clone(secrets)
	slices.SortFunc(order, func(a, b compiledSecret) int { return strings.Compare(a.Name, b.Name) })
	for _, s := range order {
		if _, ok := firstByValue[s.Value]; !ok {
			firstByValue[s.Value] = s
		}
	}
	var pats []pattern
	type patternKey struct {
		from     string
		terminal bool
	}
	seen := map[patternKey]bool{}
	add := func(from, to []byte, terminal bool) {
		key := patternKey{string(from), terminal}
		if len(from) == 0 || seen[key] {
			return
		}
		seen[key] = true
		pats = append(pats, pattern{from: from, to: to, terminal: terminal})
	}
	for _, s := range order {
		first := firstByValue[s.Value]
		for _, form := range [][2]string{
			{s.Value, first.Placeholder},
			{s.jsonValue, jsonStringContent(first.Placeholder)},
			{jsonHTMLEscapedStringContent(s.Value), jsonHTMLEscapedStringContent(first.Placeholder)},
			{url.QueryEscape(s.Value), url.QueryEscape(first.Placeholder)},
		} {
			add([]byte(form[0]), []byte(form[1]), false)
		}
		for _, enc := range []*base64.Encoding{
			base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding,
		} {
			for alignment := range 3 {
				core := secretencoding.Base64Core(enc, []byte(s.Value), alignment)
				if len(core) < 8 {
					continue
				}
				add(core, secretencoding.Base64Core(enc, []byte(first.Placeholder), alignment), false)
				add(secretencoding.Base64End(enc, []byte(s.Value), alignment),
					secretencoding.Base64End(enc, []byte(first.Placeholder), alignment), true)
			}
		}
	}
	slices.SortStableFunc(pats, func(a, b pattern) int {
		if len(a.from) != len(b.from) {
			return len(b.from) - len(a.from)
		}
		return bytes.Compare(a.from, b.from)
	})
	return pats
}

func indexPatterns(patterns []pattern) [256][]pattern {
	var first [256][]pattern
	for _, p := range patterns {
		first[p.from[0]] = append(first[p.from[0]], p)
	}
	return first
}

func jsonStringContent(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return ""
	}
	q := strings.TrimSuffix(b.String(), "\n")
	return q[1 : len(q)-1]
}

func jsonHTMLEscapedStringContent(s string) string {
	q, err := json.Marshal(s)
	if err != nil {
		return jsonStringContent(s)
	}
	return string(q[1 : len(q)-1])
}

func sortedNames(names map[string]bool) []string {
	out := make([]string, 0, len(names))
	for name := range names {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}
