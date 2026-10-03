// Package vault stores bound secrets and makes safe placeholders for machines.
package vault

import (
	"crypto/aes"
	"crypto/cipher"
	crand "crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"

	"github.com/solev/hibernaut/internal/hibapi"
)

const (
	fileVersion = 1
	keySize     = 32
	nonceSize   = 12
	// MaxValue is the largest real secret value accepted by the vault and CLIs.
	MaxValue = 64 << 10
)

// ErrNotFound is wrapped when removing a secret that does not exist.
var ErrNotFound = errors.New("secret not found")

// Secret is the public metadata for a bound secret. It never holds the real value.
type Secret struct {
	Name        string   `json:"name"`
	Env         string   `json:"env"`
	Sites       []string `json:"sites"`
	Placeholder string   `json:"placeholder"`
}

// Revealed is a secret with its real value, for host-only code.
type Revealed struct {
	Secret
	Value string
}

// File is a loaded vault file.
type File struct {
	secrets []entry
}

type entry struct {
	Secret
	Sealed string `json:"sealed"`
}

type diskFile struct {
	Version int     `json:"v"`
	Secrets []entry `json:"secrets"`
}

// CheckName reports whether name is a valid secret name.
func CheckName(name string) error {
	return hibapi.CheckName(name)
}

// CheckEnv reports whether env is a safe environment variable for a secret.
func CheckEnv(env string) error {
	s := Secret{Name: "check", Env: env, Sites: []string{"example.com"}}
	return normalizeSecret(&s)
}

// CheckSite reports whether site is a safe bound site. Wildcards may appear only as "*.".
func CheckSite(site string) error {
	return checkSite(strings.ToLower(site))
}

// CheckSecret reports whether name, env and sites form valid public secret metadata.
func CheckSecret(name, env string, sites []string) error {
	s := Secret{Name: name, Env: env, Sites: slices.Clone(sites)}
	return normalizeSecret(&s)
}

// Key is the AES-256-GCM vault key. It does not print or marshal its bytes.
type Key struct {
	b [keySize]byte
}

// NewKey returns a new random vault key.
func NewKey(rand io.Reader) (Key, error) {
	if rand == nil {
		rand = crand.Reader
	}
	var k Key
	if _, err := io.ReadFull(rand, k.b[:]); err != nil {
		return Key{}, fmt.Errorf("making vault key: %w", err)
	}
	return k, nil
}

// CreateKey makes a new key file with mode 0600. It fails if the file already exists.
func CreateKey(path string, rand io.Reader) (Key, error) {
	k, err := NewKey(rand)
	if err != nil {
		return Key{}, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return Key{}, fmt.Errorf("creating vault key: %w", err)
	}
	remove := true
	defer func() {
		if remove {
			os.Remove(path)
		}
	}()
	n, err := f.Write(k.b[:])
	if err != nil || n != len(k.b) {
		f.Close()
		if err == nil {
			err = io.ErrShortWrite
		}
		return Key{}, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return Key{}, err
	}
	if err := f.Close(); err != nil {
		return Key{}, err
	}
	remove = false
	return k, nil
}

// LoadKey loads a key file and checks its size, owner and mode.
func LoadKey(path string) (Key, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Key{}, fmt.Errorf("loading vault key: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return Key{}, fmt.Errorf("vault key %s: group or others can access it (%v)", path, info.Mode().Perm())
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return Key{}, fmt.Errorf("vault key %s: can't tell its owner", path)
	}
	if uid := os.Geteuid(); int(st.Uid) != uid {
		return Key{}, fmt.Errorf("vault key %s: belongs to uid %d, not %d", path, st.Uid, uid)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Key{}, fmt.Errorf("loading vault key: %w", err)
	}
	if len(data) != keySize {
		return Key{}, fmt.Errorf("vault key %s: got %d bytes, want %d", path, len(data), keySize)
	}
	var k Key
	copy(k.b[:], data)
	return k, nil
}

// String returns a redacted key marker.
func (Key) String() string { return "[redacted vault key]" }

// GoString returns a redacted key marker.
func (Key) GoString() string { return "[redacted vault key]" }

// Format prints a redacted key marker.
func (k Key) Format(s fmt.State, verb rune) {
	fmt.Fprint(s, k.String())
}

// MarshalJSON always refuses to marshal a key.
func (Key) MarshalJSON() ([]byte, error) {
	return nil, errors.New("vault key cannot be marshaled")
}

// Load loads path. A missing file is an empty vault.
func Load(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return new(File), nil
	}
	if err != nil {
		return nil, err
	}
	var d diskFile
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("reading vault: %w", err)
	}
	if d.Version != fileVersion {
		return nil, fmt.Errorf("vault format version %d is not supported", d.Version)
	}
	f := &File{secrets: d.Secrets}
	for i := range f.secrets {
		if err := normalizeSecret(&f.secrets[i].Secret); err != nil {
			return nil, fmt.Errorf("vault secret %q: %w", f.secrets[i].Name, err)
		}
	}
	return f, nil
}

// Secrets returns the vault metadata sorted by name.
func (f *File) Secrets() []Secret {
	out := make([]Secret, len(f.secrets))
	for i, e := range f.secrets {
		out[i] = cloneSecret(e.Secret)
	}
	slices.SortFunc(out, func(a, b Secret) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Put adds or updates a secret and seals value with key.
func (f *File) Put(key Key, name, env string, sites []string, value string) (Secret, error) {
	if err := validateValue(value); err != nil {
		return Secret{}, err
	}
	s := Secret{Name: name, Env: env, Sites: slices.Clone(sites)}
	if err := normalizeSecret(&s); err != nil {
		return Secret{}, err
	}
	revealed, err := f.Open(key)
	if err != nil {
		return Secret{}, err
	}
	for _, e := range f.secrets {
		if e.Secret.Name != s.Name && strings.EqualFold(e.Secret.Env, s.Env) {
			return Secret{}, fmt.Errorf("environment variable %s is already used by secret %s", s.Env, e.Secret.Name)
		}
	}
	placeholders := f.placeholdersExcept("")
	if overlapsAny(value, placeholders) {
		return Secret{}, errors.New("that value is a placeholder, or holds one: add the real value")
	}
	values := revealedValues(revealed)
	values = append(values, value)
	otherPlaceholders := f.placeholdersExcept(s.Name)
	if i := f.index(name); i >= 0 && Compatible(f.secrets[i].Placeholder, value) {
		s.Placeholder = f.secrets[i].Placeholder
	} else {
		for range 16 {
			p, err := Placeholder(value, crand.Reader)
			if err != nil {
				return Secret{}, err
			}
			if placeholderSafe(p, values, otherPlaceholders) {
				s.Placeholder = p
				break
			}
		}
		if s.Placeholder == "" {
			return Secret{}, errors.New("could not make a placeholder that does not overlap existing values")
		}
	}
	if !placeholderSafe(s.Placeholder, values, otherPlaceholders) {
		return Secret{}, errors.New("that value is a placeholder, or holds one: add the real value")
	}
	sealed, err := seal(key, s, value)
	if err != nil {
		return Secret{}, err
	}
	e := entry{Secret: cloneSecret(s), Sealed: sealed}
	if i := f.index(name); i >= 0 {
		f.secrets[i] = e
	} else {
		f.secrets = append(f.secrets, e)
	}
	return cloneSecret(s), nil
}

// Remove deletes a secret by name.
func (f *File) Remove(name string) error {
	if err := hibapi.CheckName(name); err != nil {
		return err
	}
	i := f.index(name)
	if i < 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	f.secrets = slices.Delete(f.secrets, i, i+1)
	return nil
}

// Open returns every secret with its real value. It fails if any value does not authenticate.
func (f *File) Open(key Key) ([]Revealed, error) {
	out := make([]Revealed, 0, len(f.secrets))
	for _, e := range f.secrets {
		value, err := open(key, e.Secret, e.Sealed)
		if err != nil {
			return nil, fmt.Errorf("opening secret %q: value does not authenticate", e.Name)
		}
		out = append(out, Revealed{Secret: cloneSecret(e.Secret), Value: value})
	}
	slices.SortFunc(out, func(a, b Revealed) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// Save writes the vault atomically with mode 0644.
func (f *File) Save(path string) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".vault-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	d := diskFile{Version: fileVersion, Secrets: slices.Clone(f.secrets)}
	slices.SortFunc(d.Secrets, func(a, b entry) int { return strings.Compare(a.Name, b.Name) })
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(d); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return syncDir(dir)
}

// Update locks the vault, loads or makes its key, runs fn, then saves the vault.
func Update(path, keyPath string, fn func(*File, Key) error) error {
	lock, err := os.OpenFile(path+".lock", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("locking vault: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	f, err := Load(path)
	if err != nil {
		return err
	}
	key, err := LoadKey(keyPath)
	if errors.Is(err, fs.ErrNotExist) {
		if len(f.secrets) > 0 {
			return fmt.Errorf("vault key is missing: existing secrets can't be opened without it")
		}
		key, err = CreateKey(keyPath, crand.Reader)
	}
	if err != nil {
		return err
	}
	if err := fn(f, key); err != nil {
		return err
	}
	return f.Save(path)
}

func (f *File) index(name string) int {
	return slices.IndexFunc(f.secrets, func(e entry) bool { return e.Name == name })
}

func (f *File) placeholdersExcept(name string) []string {
	var out []string
	for _, e := range f.secrets {
		if e.Name != name {
			out = append(out, e.Placeholder)
		}
	}
	return out
}

func revealedValues(revealed []Revealed) []string {
	out := make([]string, len(revealed))
	for i, r := range revealed {
		out[i] = r.Value
	}
	return out
}

func placeholderSafe(placeholder string, values, otherPlaceholders []string) bool {
	return !overlapsAny(placeholder, values) && !overlapsAny(placeholder, otherPlaceholders)
}

func overlapsAny(s string, xs []string) bool {
	return slices.ContainsFunc(xs, func(x string) bool { return overlaps(s, x) })
}

func overlaps(a, b string) bool {
	return a == b || strings.Contains(a, b) || strings.Contains(b, a)
}

func cloneSecret(s Secret) Secret {
	s.Sites = slices.Clone(s.Sites)
	return s
}

func seal(key Key, s Secret, value string) (string, error) {
	gcm, err := aead(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(crand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nil, nonce, []byte(value), additionalData(s))
	return base64.StdEncoding.EncodeToString(append(nonce, ciphertext...)), nil
}

func open(key Key, s Secret, sealed string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return "", err
	}
	if len(data) < nonceSize {
		return "", errors.New("sealed value is too short")
	}
	gcm, err := aead(key)
	if err != nil {
		return "", err
	}
	plain, err := gcm.Open(nil, data[:nonceSize], data[nonceSize:], additionalData(s))
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func aead(key Key) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key.b[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func additionalData(s Secret) []byte {
	b, err := json.Marshal(struct {
		Version     int      `json:"v"`
		Name        string   `json:"name"`
		Env         string   `json:"env"`
		Sites       []string `json:"sites"`
		Placeholder string   `json:"placeholder"`
	}{fileVersion, s.Name, s.Env, s.Sites, s.Placeholder})
	if err != nil {
		panic(err)
	}
	return b
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

var envRE = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

var forbiddenEnv = map[string]bool{
	"PATH":                 true,
	"HOME":                 true,
	"SHELL":                true,
	"USER":                 true,
	"LOGNAME":              true,
	"TERM":                 true,
	"LANG":                 true,
	"LANGUAGE":             true,
	"TZ":                   true,
	"MAIL":                 true,
	"PWD":                  true,
	"IFS":                  true,
	"ENV":                  true,
	"BASH_ENV":             true,
	"PROMPT_COMMAND":       true,
	"SSL_CERT_FILE":        true,
	"SSL_CERT_DIR":         true,
	"NODE_EXTRA_CA_CERTS":  true,
	"NODE_OPTIONS":         true,
	"REQUESTS_CA_BUNDLE":   true,
	"CODEX_CA_CERTIFICATE": true,
	"HTTP_PROXY":           true,
	"HTTPS_PROXY":          true,
	"NO_PROXY":             true,
	"ALL_PROXY":            true,
}

func normalizeSecret(s *Secret) error {
	if err := hibapi.CheckName(s.Name); err != nil {
		return err
	}
	if len(s.Env) > 128 || !envRE.MatchString(s.Env) {
		return fmt.Errorf("bad environment variable %s: use A-Z, 0-9 and _, starting with A-Z or _", s.Env)
	}
	env := strings.ToUpper(s.Env)
	if forbiddenEnv[env] || strings.HasPrefix(env, "LD_") || strings.HasPrefix(env, "LC_") {
		return fmt.Errorf("environment variable %s is refused because it can hijack logins or TLS", s.Env)
	}
	sites, err := normalizeSites(s.Sites)
	if err != nil {
		return err
	}
	s.Sites = sites
	return nil
}

func normalizeSites(sites []string) ([]string, error) {
	if len(sites) == 0 || len(sites) > 16 {
		return nil, fmt.Errorf("bad sites: want 1 to 16 hostnames")
	}
	out := make([]string, 0, len(sites))
	seen := map[string]bool{}
	for _, site := range sites {
		site = strings.ToLower(site)
		if err := checkSite(site); err != nil {
			return nil, err
		}
		if !seen[site] {
			seen[site] = true
			out = append(out, site)
		}
	}
	slices.Sort(out)
	return out, nil
}

func checkSite(site string) error {
	if len(site) == 0 || len(site) > 253 {
		return fmt.Errorf("bad site %q: want a hostname up to 253 bytes", site)
	}
	if strings.HasSuffix(site, ".") || strings.ContainsAny(site, ":/") {
		return fmt.Errorf("bad site %q: no ports, paths or trailing dots", site)
	}
	host := site
	if strings.HasPrefix(host, "*.") {
		host = strings.TrimPrefix(host, "*.")
	} else if strings.Contains(host, "*") {
		return fmt.Errorf("bad site %q: wildcard is only allowed as the whole leftmost label", site)
	}
	if net.ParseIP(host) != nil {
		return fmt.Errorf("bad site %q: IP addresses are not allowed", site)
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return fmt.Errorf("bad site %q: want at least two labels", site)
	}
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 {
			return fmt.Errorf("bad site %q: labels must be 1 to 63 bytes", site)
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return fmt.Errorf("bad site %q: labels must not start or end with hyphen", site)
		}
		for _, c := range []byte(label) {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return fmt.Errorf("bad site %q: use lowercase ASCII letters, digits and hyphens", site)
			}
		}
	}
	return nil
}

func validateValue(value string) error {
	if len(value) == 0 || len(value) > MaxValue {
		return fmt.Errorf("secret value must be 1 byte to 64 KiB")
	}
	for _, c := range []byte(value) {
		if c < 0x21 || c > 0x7e {
			return fmt.Errorf("secret value must be visible ASCII")
		}
	}
	return nil
}

// Placeholder returns a safe placeholder with the same length and prefix as value. Body
// separators stay in place.
func Placeholder(value string, rand io.Reader) (string, error) {
	if err := validateValue(value); err != nil {
		return "", err
	}
	if rand == nil {
		rand = crand.Reader
	}
	if jwt, ok := splitJWT(value); ok {
		return jwtPlaceholder(jwt, rand)
	}
	prefix := placeholderPrefix(value)
	body := value[len(prefix):]
	mask := fixedMask(prefix, body)
	alphabet := chooseAlphabet(body, mask)
	randomPositions := countRandom(mask)
	hib := canSpellHIB(alphabet) && startsWithThreeRandom(mask)
	randomBits := float64(randomPositions) * math.Log2(float64(len(alphabet)))
	if hib {
		randomBits -= 3 * math.Log2(float64(len(alphabet)))
	}
	if randomBits < 64 {
		return "", fmt.Errorf("secret value is too short for a safe placeholder")
	}
	for range 100 {
		p, err := fillPlaceholder(prefix, body, mask, alphabet, hib, rand)
		if err != nil {
			return "", err
		}
		if p != value && !strings.Contains(p, value) && !strings.Contains(value, p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("could not make a safe placeholder")
}

// Compatible reports whether placeholder can stand for value after rotation.
func Compatible(placeholder, value string) bool {
	pJWT, pOK := splitJWT(placeholder)
	vJWT, vOK := splitJWT(value)
	if pOK || vOK {
		return pOK && vOK && pJWT.public == vJWT.public
	}
	pp := placeholderPrefix(placeholder)
	vp := placeholderPrefix(value)
	if pp == vp {
		return true
	}
	return pp != "" && !hasKnownPrefix(placeholder) && !hasKnownPrefix(value) &&
		strings.HasPrefix(pp, vp) && len(value) >= len(pp) && value[len(pp)-1] == pp[len(pp)-1]
}

type jwtParts struct {
	public, signature string
}

func splitJWT(s string) (jwtParts, bool) {
	parts := strings.Split(s, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return jwtParts{}, false
	}
	header, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return jwtParts{}, false
	}
	var h map[string]any
	if err := json.Unmarshal(header, &h); err != nil || h["alg"] == nil {
		return jwtParts{}, false
	}
	return jwtParts{public: parts[0] + "." + parts[1] + ".", signature: parts[2]}, true
}

func jwtPlaceholder(jwt jwtParts, rand io.Reader) (string, error) {
	if float64(len(jwt.signature))*math.Log2(64) < 64 {
		return "", fmt.Errorf("secret value is too short for a safe placeholder")
	}
	sig, err := randomString(len(jwt.signature), alphabetBase64URL, rand)
	if err != nil {
		return "", err
	}
	return jwt.public + sig, nil
}

var knownPrefixes = []string{
	"github_pat_", "sk-ant-api03-", "sk-or-v1-", "sk-proj-", "sk-ant-", "ghp_", "gho_", "ghu_", "ghs_", "ghr_",
	"xoxb-", "xoxp-", "xapp-", "glpat-", "npm_", "pypi-", "dop_v1_", "PMAK-", "AIza", "HRKU-", "xai-",
	"gsk_", "glsa_", "shpat_", "dckr_pat_", "sbp_", "r8_", "pplx-", "lin_api_", "hf_", "at-", "AKIA", "ASIA",
}

func placeholderPrefix(value string) string {
	best := ""
	for _, p := range knownPrefixes {
		if strings.HasPrefix(value, p) && len(p) > len(best) {
			best = p
		}
	}
	if best != "" {
		return best
	}
	limit := min(16, len(value)-16)
	if limit <= 0 {
		return ""
	}
	last := -1
	for i := 0; i < limit; i++ {
		c := value[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c == '_' || c == '-':
			if i == 0 {
				return ""
			}
			last = i
		default:
			return value[:last+1]
		}
	}
	return value[:last+1]
}

func hasKnownPrefix(value string) bool {
	return slices.ContainsFunc(knownPrefixes, func(p string) bool { return strings.HasPrefix(value, p) })
}

func fixedMask(prefix, body string) []bool {
	mask := make([]bool, len(body))
	for i, c := range []byte(body) {
		if strings.ContainsRune("-_.~:", rune(c)) {
			mask[i] = true
		}
	}
	for i := len(body) - 1; i >= 0 && body[i] == '='; i-- {
		mask[i] = true
	}
	return mask
}

func countRandom(mask []bool) int {
	n := 0
	for _, fixed := range mask {
		if !fixed {
			n++
		}
	}
	return n
}

func startsWithThreeRandom(mask []bool) bool {
	return len(mask) >= 3 && !mask[0] && !mask[1] && !mask[2]
}

const (
	alphabetDigits    = "0123456789"
	alphabetLowerHex  = "0123456789abcdef"
	alphabetUpperHex  = "0123456789ABCDEF"
	alphabetBase62    = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	alphabetBase64URL = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	alphabetBase64    = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
)

func chooseAlphabet(body string, fixed []bool) string {
	full := randomBytes(body, fixed)
	chars := significant(body, fixed)
	switch {
	case allIn(chars, alphabetDigits):
		return alphabetDigits
	case allIn(chars, alphabetLowerHex) && hasAny(chars, "abcdef"):
		return alphabetLowerHex
	case allIn(chars, alphabetUpperHex) && hasAny(chars, "ABCDEF"):
		return alphabetUpperHex
	case allIn(chars, alphabetBase62):
		return alphabetBase62
	case strings.ContainsAny(full, "+/") && allIn(chars, alphabetBase64):
		return alphabetBase64
	default:
		return alphabetBase62
	}
}

func randomBytes(body string, fixed []bool) string {
	var b strings.Builder
	for i, c := range []byte(body) {
		if !fixed[i] {
			b.WriteByte(c)
		}
	}
	return b.String()
}

func significant(body string, fixed []bool) string {
	var b strings.Builder
	for i, c := range []byte(body) {
		if !fixed[i] {
			b.WriteByte(c)
		}
	}
	if b.Len() == 0 {
		for i, c := range []byte(body) {
			if !fixed[i] {
				b.WriteByte(c)
			}
		}
	}
	return b.String()
}

func allIn(s, alphabet string) bool {
	for _, c := range s {
		if !strings.ContainsRune(alphabet, c) {
			return false
		}
	}
	return s != ""
}

func hasAny(s, chars string) bool {
	return strings.ContainsAny(s, chars)
}

func canSpellHIB(alphabet string) bool {
	return strings.ContainsRune(alphabet, 'h') && strings.ContainsRune(alphabet, 'i') && strings.ContainsRune(alphabet, 'b')
}

func fillPlaceholder(prefix, body string, fixed []bool, alphabet string, hib bool, rand io.Reader) (string, error) {
	out := []byte(prefix + body)
	bodyStart := len(prefix)
	hibAt := 0
	for i := range body {
		if fixed[i] {
			continue
		}
		if hib && hibAt < 3 {
			out[bodyStart+i] = "hib"[hibAt]
			hibAt++
			continue
		}
		c, err := randomChar(alphabet, rand)
		if err != nil {
			return "", err
		}
		out[bodyStart+i] = c
	}
	for i, c := range out {
		if !strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._~+/=:-", rune(c)) {
			repl, err := randomChar(alphabetBase62, rand)
			if err != nil {
				return "", err
			}
			out[i] = repl
		}
	}
	return string(out), nil
}

func randomString(n int, alphabet string, rand io.Reader) (string, error) {
	out := make([]byte, n)
	for i := range out {
		c, err := randomChar(alphabet, rand)
		if err != nil {
			return "", err
		}
		out[i] = c
	}
	return string(out), nil
}

func randomChar(alphabet string, rand io.Reader) (byte, error) {
	limit := 256 - 256%len(alphabet)
	var b [1]byte
	for {
		if _, err := io.ReadFull(rand, b[:]); err != nil {
			return 0, err
		}
		if int(b[0]) < limit {
			return alphabet[int(b[0])%len(alphabet)], nil
		}
	}
}
