package vault_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/solev/hibernaut/internal/vault"
)

func TestPlaceholderShapes(t *testing.T) {
	jwt := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJlbWFpbCI6Im0uaGliQGV4YW1wbGUuY29tIiwiZXhwIjo0MTAyNDQ0ODAwfQ.signaturepart"
	for _, tc := range []struct {
		name        string
		value       string
		prefix      string
		containsHib bool
		keeps       func(string) bool
	}{
		{name: "gho", value: "gho_" + strings.Repeat("Az", 18), prefix: "gho_", containsHib: true},
		{name: "ghp", value: "ghp_" + strings.Repeat("bz", 18), prefix: "ghp_", containsHib: true},
		{
			name:   "github pat",
			value:  "github_pat_" + strings.Repeat("A", 22) + "_" + strings.Repeat("B", 59),
			prefix: "github_pat_",
			keeps:  func(p string) bool { return p[len("github_pat_")+22] == '_' },
		},
		{name: "sk", value: "sk-" + strings.Repeat("az", 20), prefix: "sk-", containsHib: true},
		{name: "sk proj", value: "sk-proj-" + strings.Repeat("az", 5) + "-_" + strings.Repeat("bz", 16), prefix: "sk-proj-", containsHib: true},
		{
			name:   "jwt",
			value:  jwt,
			prefix: strings.Join(strings.Split(jwt, ".")[:2], ".") + ".",
			keeps: func(p string) bool {
				return strings.Join(strings.Split(p, ".")[:2], ".") == strings.Join(strings.Split(jwt, ".")[:2], ".")
			},
		},
		{name: "aws access key", value: "AKIA" + strings.Repeat("A", 16), prefix: "AKIA"},
		{name: "lowercase hex", value: strings.Repeat("abcdef0123456789", 3), prefix: ""},
		{name: "base64 padding", value: base64.StdEncoding.EncodeToString([]byte("this is a long enough byte string.")), prefix: "", keeps: func(p string) bool { return strings.HasSuffix(p, "=") }},
		{name: "generic prefix", value: "token-" + strings.Repeat("az", 16), prefix: "token-", containsHib: true},
		{name: "digits only", value: strings.Repeat("1", 20), prefix: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := vault.Placeholder(tc.value, seededRand())
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.value) {
				t.Fatalf("placeholder length = %d, want %d", len(got), len(tc.value))
			}
			if !strings.HasPrefix(got, tc.prefix) {
				t.Fatalf("placeholder %q does not have prefix %q", got, tc.prefix)
			}
			if got == tc.value || strings.Contains(got, tc.value) || strings.Contains(tc.value, got) {
				t.Fatalf("placeholder and value are not distinct: %q", got)
			}
			if !safePlaceholder(got) {
				t.Fatalf("placeholder %q has unsafe bytes", got)
			}
			if tc.containsHib && !strings.HasPrefix(got[len(tc.prefix):], "hib") {
				t.Fatalf("placeholder body %q does not start with hib", got[len(tc.prefix):])
			}
			if tc.keeps != nil && !tc.keeps(got) {
				t.Fatalf("placeholder %q did not keep the required shape", got)
			}
			if !vault.Compatible(got, tc.value) {
				t.Fatalf("Compatible(%q, value) = false, want true", got)
			}
		})
	}
}

func TestPlaceholderRefusesTooShortValues(t *testing.T) {
	if got, err := vault.Placeholder("gho_short", seededRand()); err == nil {
		t.Fatalf("Placeholder = %q, want an error", got)
	} else if !strings.Contains(err.Error(), "too short") {
		t.Fatalf("Placeholder error = %v, want too short", err)
	}
}

func TestCompatible(t *testing.T) {
	jwtA := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJhIn0." + strings.Repeat("a", 43)
	jwtB := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJiIn0." + strings.Repeat("b", 43)
	for _, tc := range []struct {
		name        string
		placeholder string
		value       string
		want        bool
	}{
		{name: "same known prefix", placeholder: "gho_hib" + strings.Repeat("A", 33), value: "gho_" + strings.Repeat("b", 40), want: true},
		{name: "different known prefix", placeholder: "gho_hib" + strings.Repeat("A", 33), value: "ghp_" + strings.Repeat("b", 40)},
		{name: "generic prefix", placeholder: "token-hib" + strings.Repeat("A", 28), value: "token-" + strings.Repeat("b", 40), want: true},
		{name: "jwt same public claims", placeholder: jwtA, value: jwtA[:len(jwtA)-10] + strings.Repeat("c", 10), want: true},
		{name: "jwt changed public claims", placeholder: jwtA, value: jwtB},
		{name: "jwt versus opaque", placeholder: jwtA, value: "token-" + strings.Repeat("b", 40)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := vault.Compatible(tc.placeholder, tc.value); got != tc.want {
				t.Fatalf("Compatible = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestVaultRoundTripAndTamper(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.json")
	key := newKey(t)
	f := new(vault.File)
	first, err := f.Put(key, "github", "GH_TOKEN", []string{"API.GITHUB.COM", "github.com", "api.github.com"}, "gho_"+strings.Repeat("A", 40))
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.Put(key, "github", "GH_TOKEN", []string{"github.com"}, "gho_"+strings.Repeat("B", 44))
	if err != nil {
		t.Fatal(err)
	}
	if second.Placeholder != first.Placeholder {
		t.Fatalf("rotating a compatible value changed placeholder from %q to %q", first.Placeholder, second.Placeholder)
	}
	if _, err := f.Put(key, "openai", "GH_TOKEN", []string{"chatgpt.com"}, "sk-"+strings.Repeat("a", 40)); err == nil {
		t.Fatal("Put reused another secret's env, want an error")
	}
	if err := f.Save(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"gho_" + strings.Repeat("B", 44), base64.StdEncoding.EncodeToString([]byte("gho_" + strings.Repeat("B", 44))), base64.URLEncoding.EncodeToString([]byte("gho_" + strings.Repeat("B", 44)))} {
		if bytes.Contains(data, []byte(leak)) {
			t.Fatalf("vault file contains leaked value encoding %q in %s", leak, data)
		}
	}
	loaded, err := vault.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	revealed, err := loaded.Open(key)
	if err != nil {
		t.Fatal(err)
	}
	if len(revealed) != 1 || revealed[0].Name != "github" || revealed[0].Value != "gho_"+strings.Repeat("B", 44) {
		t.Fatalf("Open = %#v, want github value", revealed)
	}

	for _, tc := range []struct {
		name   string
		tamper func(map[string]any)
	}{
		{name: "format version", tamper: func(m map[string]any) { m["v"] = float64(2) }},
		{name: "name", tamper: func(m map[string]any) { firstSecret(m)["name"] = "other" }},
		{name: "env", tamper: func(m map[string]any) { firstSecret(m)["env"] = "OTHER_TOKEN" }},
		{name: "sites", tamper: func(m map[string]any) { firstSecret(m)["sites"] = []any{"example.com"} }},
		{name: "placeholder", tamper: func(m map[string]any) { firstSecret(m)["placeholder"] = "gho_hib" + strings.Repeat("Z", 37) }},
		{name: "nonce", tamper: func(m map[string]any) { flipSealedByte(t, firstSecret(m), 0) }},
		{name: "ciphertext", tamper: func(m map[string]any) { flipSealedByte(t, firstSecret(m), 20) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tampered := filepath.Join(dir, "tampered-"+tc.name+".json")
			var doc map[string]any
			if err := json.Unmarshal(data, &doc); err != nil {
				t.Fatal(err)
			}
			tc.tamper(doc)
			out, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(tampered, out, 0o644); err != nil {
				t.Fatal(err)
			}
			tf, err := vault.Load(tampered)
			if tc.name == "format version" {
				if err == nil || !strings.Contains(err.Error(), "version") {
					t.Fatalf("Load tampered version error = %v, want version", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tf.Open(key); err == nil || !strings.Contains(err.Error(), "secret") {
				t.Fatalf("Open tampered vault error = %v, want secret context", err)
			}
		})
	}
}

func TestValidation(t *testing.T) {
	key := newKey(t)
	for _, tc := range []struct {
		name  string
		put   func(*vault.File) error
		match string
	}{
		{name: "name", put: func(f *vault.File) error {
			_, err := f.Put(key, "Bad", "TOKEN", []string{"example.com"}, strings.Repeat("a", 20))
			return err
		}, match: "bad name"},
		{name: "env", put: func(f *vault.File) error {
			_, err := f.Put(key, "ok", "Path", []string{"example.com"}, strings.Repeat("a", 20))
			return err
		}, match: "environment variable"},
		{name: "dangerous env", put: func(f *vault.File) error {
			_, err := f.Put(key, "ok", "LD_PRELOAD", []string{"example.com"}, strings.Repeat("a", 20))
			return err
		}, match: "refused"},
		{name: "site", put: func(f *vault.File) error {
			_, err := f.Put(key, "ok", "TOKEN", []string{"127.0.0.1"}, strings.Repeat("a", 20))
			return err
		}, match: "site"},
		{name: "value", put: func(f *vault.File) error {
			_, err := f.Put(key, "ok", "TOKEN", []string{"example.com"}, "line\nbreak")
			return err
		}, match: "visible ASCII"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.put(new(vault.File))
			if err == nil || !strings.Contains(err.Error(), tc.match) {
				t.Fatalf("Put error = %v, want %q", err, tc.match)
			}
		})
	}
}

func TestRemoveAndSecrets(t *testing.T) {
	key := newKey(t)
	f := new(vault.File)
	if _, err := f.Put(key, "z", "Z_TOKEN", []string{"z.example.com"}, strings.Repeat("z", 20)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Put(key, "a", "A_TOKEN", []string{"a.example.com"}, strings.Repeat("a", 20)); err != nil {
		t.Fatal(err)
	}
	if got := f.Secrets(); len(got) != 2 || got[0].Name != "a" || got[1].Name != "z" {
		t.Fatalf("Secrets = %#v, want sorted metadata", got)
	}
	if err := f.Remove("missing"); !errors.Is(err, vault.ErrNotFound) {
		t.Fatalf("Remove missing error = %v, want ErrNotFound", err)
	}
	if err := f.Remove("a"); err != nil {
		t.Fatal(err)
	}
	if got := f.Secrets(); len(got) != 1 || got[0].Name != "z" {
		t.Fatalf("after Remove, Secrets = %#v", got)
	}
}

func TestKeyFileChecksAndRedaction(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.key")
	key, err := vault.CreateKey(path, seededRand())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vault.LoadKey(path); err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		if got := fmt.Sprintf(format, key); strings.Contains(got, "000102") || strings.Contains(got, "hib") || got == "" {
			t.Fatalf("fmt.Sprintf(%q, key) = %q, want redacted", format, got)
		}
	}
	if data, err := json.Marshal(key); err == nil {
		t.Fatalf("json.Marshal(Key) = %s, want an error", data)
	}

	for _, tc := range []struct {
		name string
		data []byte
		mode os.FileMode
	}{
		{name: "short", data: bytes.Repeat([]byte{1}, 31), mode: 0o600},
		{name: "long", data: bytes.Repeat([]byte{1}, 33), mode: 0o600},
		{name: "group readable", data: bytes.Repeat([]byte{1}, 32), mode: 0o640},
		{name: "other readable", data: bytes.Repeat([]byte{1}, 32), mode: 0o604},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := filepath.Join(dir, tc.name)
			if err := os.WriteFile(bad, tc.data, tc.mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(bad, tc.mode); err != nil {
				t.Fatal(err)
			}
			if _, err := vault.LoadKey(bad); err == nil {
				t.Fatal("LoadKey succeeded, want an error")
			}
		})
	}
}

func TestUpdateKeepsConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.json")
	keyPath := filepath.Join(dir, "vault.key")
	const n = 12
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := vault.Update(path, keyPath, func(f *vault.File, key vault.Key) error {
				name := fmt.Sprintf("secret-%02d", i)
				env := fmt.Sprintf("TOKEN_%02d", i)
				_, err := f.Put(key, name, env, []string{fmt.Sprintf("s%d.example.com", i)}, "gho_"+strings.Repeat(fmt.Sprintf("%x", i), 40))
				return err
			})
			if err != nil {
				t.Errorf("Update %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	f, err := vault.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Secrets(); len(got) != n {
		t.Fatalf("Update wrote %d secrets, want %d: %#v", len(got), n, got)
	}
	key, err := vault.LoadKey(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := f.Open(key); err != nil || len(got) != n {
		t.Fatalf("Open after concurrent Update = %d, %v", len(got), err)
	}
}

func FuzzPlaceholder(f *testing.F) {
	for _, seed := range []string{
		"gho_" + strings.Repeat("A", 40),
		"sk-" + strings.Repeat("a", 40),
		strings.Repeat("0123456789", 8),
		"token-" + strings.Repeat("a", 32),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if !validValue(value) {
			t.Skip()
		}
		placeholder, err := vault.Placeholder(value, seededRand())
		if err != nil {
			t.Skip()
		}
		if len(placeholder) != len(value) {
			t.Fatalf("len = %d, want %d", len(placeholder), len(value))
		}
		if placeholder == value || strings.Contains(placeholder, value) || strings.Contains(value, placeholder) {
			t.Fatalf("placeholder %q is not safely distinct from value", placeholder)
		}
		if !safePlaceholder(placeholder) {
			t.Fatalf("placeholder %q has unsafe bytes", placeholder)
		}
		if !vault.Compatible(placeholder, value) {
			t.Fatalf("Compatible(%q, value) = false", placeholder)
		}
	})
}

func FuzzPutOpen(f *testing.F) {
	f.Add("github", "GH_TOKEN", "github.com", "gho_"+strings.Repeat("A", 40))
	f.Fuzz(func(t *testing.T, name, env, site, value string) {
		if !validValue(value) {
			t.Skip()
		}
		key := newKey(t)
		var vf vault.File
		if _, err := vf.Put(key, name, env, []string{site}, value); err != nil {
			t.Skip()
		}
		got, err := vf.Open(key)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Value != value {
			t.Fatalf("Open = %#v, want %q", got, value)
		}
	})
}

func seededRand() *bytes.Reader {
	return bytes.NewReader(bytes.Repeat([]byte{
		0, 1, 2, 3, 4, 5, 6, 7, 8, 9,
		10, 11, 12, 13, 14, 15, 16, 17, 18, 19,
		20, 21, 22, 23, 24, 25, 26, 27, 28, 29,
		30, 31, 32, 33, 34, 35, 36, 37, 38, 39,
	}, 1024))
}

func newKey(t *testing.T) vault.Key {
	t.Helper()
	key, err := vault.NewKey(seededRand())
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func safePlaceholder(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool {
		return !strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._~+/=:-", r)
	}) == -1
}

func validValue(s string) bool {
	if len(s) == 0 || len(s) > 64<<10 {
		return false
	}
	for _, c := range []byte(s) {
		if c < 0x21 || c > 0x7e {
			return false
		}
	}
	return true
}

func firstSecret(m map[string]any) map[string]any {
	return m["secrets"].([]any)[0].(map[string]any)
}

func flipSealedByte(t *testing.T, secret map[string]any, offset int) {
	t.Helper()
	sealed, err := base64.StdEncoding.DecodeString(secret["sealed"].(string))
	if err != nil {
		t.Fatal(err)
	}
	sealed[offset] ^= 1
	secret["sealed"] = base64.StdEncoding.EncodeToString(sealed)
}

func TestNormalizeSites(t *testing.T) {
	key := newKey(t)
	var f vault.File
	secret, err := f.Put(key, "sites", "SITES_TOKEN", []string{"*.EXAMPLE.com", "api.example.com", "*.example.com"}, strings.Repeat("a", 20))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"*.example.com", "api.example.com"}; !slices.Equal(secret.Sites, want) {
		t.Fatalf("Sites = %q, want %q", secret.Sites, want)
	}
}
