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
	"mime"
	"net"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
)

const maxBody = 1 << 20

// Secret is one host-held secret and the sites where its real value may be sent.
type Secret struct {
	Name, Placeholder, Value string
	Sites                    []string
}

// Swapper swaps placeholders in requests and scrubs real values from responses.
type Swapper struct {
	secrets []compiledSecret
	scrub   []pattern
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
	from []byte
	to   []byte
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

// Match reports whether host matches site. A wildcard site matches subdomains only.
func Match(site, host string) bool {
	m, err := compileSite(site)
	if err != nil {
		return false
	}
	return m.match(host)
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

// Request swaps placeholders for secrets bound to host in r. It returns the swapped secret names.
func (sw *Swapper) Request(r *http.Request, host string) ([]string, error) {
	bound := sw.bound(host)
	if len(bound) == 0 {
		return nil, nil
	}
	swapped := map[string]bool{}
	for key, values := range r.Header {
		for i, value := range values {
			next := value
			if strings.EqualFold(key, "Authorization") || strings.EqualFold(key, "Proxy-Authorization") {
				next = swapBasic(next, bound, swapped)
			}
			next = replacePlaceholders(next, bound, valueKindRaw, swapped)
			values[i] = next
		}
		r.Header[key] = values
	}
	r.URL.RawQuery = swapQuery(r.URL.RawQuery, bound, swapped)
	if err := sw.swapBody(r, bound, swapped); err != nil {
		return nil, err
	}
	return sortedNames(swapped), nil
}

type valueKind int

const (
	valueKindRaw valueKind = iota
	valueKindJSON
)

func swapBasic(value string, secrets []compiledSecret, swapped map[string]bool) string {
	scheme, token, ok := strings.Cut(value, " ")
	if !ok || !strings.EqualFold(scheme, "Basic") {
		return value
	}
	decoded, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return value
	}
	next := replacePlaceholders(string(decoded), secrets, valueKindRaw, swapped)
	return scheme + " " + base64.StdEncoding.EncodeToString([]byte(next))
}

func replacePlaceholders(in string, secrets []compiledSecret, kind valueKind, swapped map[string]bool) string {
	out := in
	for _, s := range secrets {
		replacement := s.Value
		if kind == valueKindJSON {
			replacement = s.jsonValue
		}
		if strings.Contains(out, s.Placeholder) {
			out = strings.ReplaceAll(out, s.Placeholder, replacement)
			swapped[s.Name] = true
		}
	}
	return out
}

func swapQuery(raw string, secrets []compiledSecret, swapped map[string]bool) string {
	if raw == "" {
		return raw
	}
	parts := strings.Split(raw, "&")
	for i, part := range parts {
		key, value, hasValue := strings.Cut(part, "=")
		key = swapQueryPart(key, secrets, swapped)
		if hasValue {
			value = swapQueryPart(value, secrets, swapped)
			parts[i] = key + "=" + value
		} else {
			parts[i] = key
		}
	}
	return strings.Join(parts, "&")
}

func swapQueryPart(part string, secrets []compiledSecret, swapped map[string]bool) string {
	raw := part
	changed := false
	for _, s := range secrets {
		if strings.Contains(raw, s.Placeholder) {
			raw = strings.ReplaceAll(raw, s.Placeholder, url.QueryEscape(s.Value))
			swapped[s.Name] = true
			changed = true
		}
	}
	if changed {
		return raw
	}
	decoded, err := url.QueryUnescape(part)
	if err != nil {
		return part
	}
	next := decoded
	for _, s := range secrets {
		if strings.Contains(next, s.Placeholder) {
			next = strings.ReplaceAll(next, s.Placeholder, s.Value)
			swapped[s.Name] = true
			changed = true
		}
	}
	if changed {
		return url.QueryEscape(next)
	}
	return part
}

func (sw *Swapper) swapBody(r *http.Request, secrets []compiledSecret, swapped map[string]bool) error {
	if r.Body == nil || r.Body == http.NoBody || !requestBodyText(r.Header.Get("Content-Type")) || encoded(r.Header.Get("Content-Encoding")) {
		return nil
	}
	body := r.Body
	data, err := io.ReadAll(io.LimitReader(body, maxBody+1))
	if err != nil {
		return err
	}
	if len(data) > maxBody {
		r.Body = readCloser{
			Reader: io.MultiReader(bytes.NewReader(data), body),
			close:  body.Close,
		}
		r.GetBody = nil
		return nil
	}
	if err := body.Close(); err != nil {
		return err
	}
	kind := bodyKind(r.Header.Get("Content-Type"))
	next := string(data)
	switch kind {
	case "json":
		next = replacePlaceholders(next, secrets, valueKindJSON, swapped)
	case "form":
		next = swapQuery(next, secrets, swapped)
	default:
		next = replacePlaceholders(next, secrets, valueKindRaw, swapped)
	}
	buf := []byte(next)
	r.Body = io.NopCloser(bytes.NewReader(buf))
	r.ContentLength = int64(len(buf))
	r.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(buf)), nil
	}
	r.Header.Del("Content-Length")
	return nil
}

type readCloser struct {
	io.Reader
	close func() error
}

func (r readCloser) Close() error { return r.close() }

func encoded(contentEncoding string) bool {
	contentEncoding = strings.TrimSpace(contentEncoding)
	return contentEncoding != "" && !strings.EqualFold(contentEncoding, "identity")
}

func requestBodyText(contentType string) bool {
	switch bodyKind(contentType) {
	case "json", "form", "text":
		return true
	default:
		return false
	}
}

func bodyKind(contentType string) string {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mt = strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	}
	switch {
	case mt == "application/json" || strings.HasSuffix(mt, "+json"):
		return "json"
	case mt == "application/x-www-form-urlencoded":
		return "form"
	case strings.HasPrefix(mt, "text/"):
		return "text"
	default:
		return ""
	}
}

// Text reports whether a response media type should be scrubbed as text.
func Text(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mt = strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	}
	return mt == "application/json" ||
		strings.HasSuffix(mt, "+json") ||
		mt == "application/x-www-form-urlencoded" ||
		strings.HasPrefix(mt, "text/") ||
		mt == "application/xml" ||
		strings.HasSuffix(mt, "+xml") ||
		mt == "application/javascript" ||
		mt == "text/javascript"
}

// ScrubHeader replaces real values in h with placeholders.
func (sw *Swapper) ScrubHeader(h http.Header) {
	for key, values := range h {
		for i, value := range values {
			values[i] = string(replaceBytes([]byte(value), sw.scrub))
		}
		h[key] = values
	}
}

// ScrubBody returns a stream that replaces real values with placeholders.
func (sw *Swapper) ScrubBody(body io.ReadCloser) io.ReadCloser {
	pats := slices.Clone(sw.scrub)
	sort.SliceStable(pats, func(i, j int) bool {
		if len(pats[i].from) != len(pats[j].from) {
			return len(pats[i].from) > len(pats[j].from)
		}
		return string(pats[i].from) < string(pats[j].from)
	})
	first := map[byte][]pattern{}
	for _, p := range pats {
		first[p.from[0]] = append(first[p.from[0]], p)
	}
	return &scrubReader{src: body, patterns: pats, first: first, buf: make([]byte, 0, 32<<10)}
}

type scrubReader struct {
	src      io.ReadCloser
	patterns []pattern
	first    map[byte][]pattern
	buf      []byte
	out      []byte
	eof      bool
	err      error
}

func (r *scrubReader) Read(p []byte) (int, error) {
	for len(r.out) == 0 && r.err == nil {
		r.fill()
	}
	if len(r.out) > 0 {
		n := copy(p, r.out)
		r.out = r.out[n:]
		return n, nil
	}
	return 0, r.err
}

func (r *scrubReader) Close() error {
	return r.src.Close()
}

func (r *scrubReader) fill() {
	if !r.eof {
		var tmp [32 << 10]byte
		n, err := r.src.Read(tmp[:])
		if n > 0 {
			r.buf = append(r.buf, tmp[:n]...)
		}
		if err != nil {
			r.eof = true
			if err != io.EOF {
				r.err = err
			}
		}
	}
	hold := 0
	if !r.eof {
		hold = r.hold()
	}
	limit := len(r.buf) - hold
	if limit > 0 {
		r.out = append(r.out, r.replace(r.buf[:limit])...)
		r.buf = append([]byte(nil), r.buf[limit:]...)
		return
	}
	if r.eof && len(r.buf) > 0 {
		r.out = append(r.out, r.replace(r.buf)...)
		r.buf = nil
		return
	}
	if r.eof && len(r.buf) == 0 && r.err == nil {
		r.err = io.EOF
	}
}

func (r *scrubReader) replace(in []byte) []byte {
	return replaceWithFirst(in, r.first)
}

func replaceBytes(in []byte, patterns []pattern) []byte {
	pats := slices.Clone(patterns)
	sort.SliceStable(pats, func(i, j int) bool {
		if len(pats[i].from) != len(pats[j].from) {
			return len(pats[i].from) > len(pats[j].from)
		}
		return string(pats[i].from) < string(pats[j].from)
	})
	first := map[byte][]pattern{}
	for _, p := range pats {
		first[p.from[0]] = append(first[p.from[0]], p)
	}
	return replaceWithFirst(in, first)
}

func replaceWithFirst(in []byte, first map[byte][]pattern) []byte {
	out := make([]byte, 0, len(in))
	for i := 0; i < len(in); {
		var match pattern
		for _, p := range first[in[i]] {
			if len(p.from) > len(match.from) && bytes.HasPrefix(in[i:], p.from) {
				match = p
			}
		}
		if match.from != nil {
			out = append(out, match.to...)
			i += len(match.from)
			continue
		}
		out = append(out, in[i])
		i++
	}
	return out
}

func (r *scrubReader) hold() int {
	var hold int
	for _, p := range r.patterns {
		max := min(len(p.from)-1, len(r.buf))
		for n := max; n > hold; n-- {
			if p.from[n-1] == r.buf[len(r.buf)-1] && bytes.HasSuffix(r.buf, p.from[:n]) {
				hold = n
				break
			}
		}
	}
	return hold
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
	seen := map[string]bool{}
	for _, s := range order {
		first := firstByValue[s.Value]
		to := []byte(first.Placeholder)
		forms := []string{s.Value, s.jsonValue, jsonHTMLEscapedStringContent(s.Value), url.QueryEscape(s.Value)}
		for _, form := range forms {
			if form == "" || seen[form] {
				continue
			}
			seen[form] = true
			pats = append(pats, pattern{from: []byte(form), to: to})
		}
	}
	return pats
}

func jsonStringContent(s string) string {
	q := strconv.Quote(s)
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
