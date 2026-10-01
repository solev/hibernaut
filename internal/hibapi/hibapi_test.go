package hibapi_test

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/solev/hibernaut/internal/hibapi"
)

func TestCheckName(t *testing.T) {
	for _, name := range []string{"a", "app", "my-app", "a1", "0", strings.Repeat("a", 63)} {
		if err := hibapi.CheckName(name); err != nil {
			t.Errorf("CheckName(%q) = %v, want nil", name, err)
		}
	}
	for _, name := range []string{
		"", "-a", "a-", "A", "my_app", "my.app", "a b", "a/b", "../x", "a;b", "$(x)", "é",
		strings.Repeat("a", 64), "a\n", "\na",
	} {
		if err := hibapi.CheckName(name); err == nil {
			t.Errorf("CheckName(%q) = nil, want an error", name)
		}
	}
}

func TestDecodeResponse(t *testing.T) {
	r, err := hibapi.DecodeResponse([]byte(`{"v":1,"machine":{"name":"app","id":"abc","state":"running","startedAt":"2025-01-02T03:04:05Z","endAt":"2025-01-02T04:04:05Z"}}`))
	if err != nil || r.Machine == nil || r.Machine.Name != "app" || r.Machine.ID != "abc" || r.Machine.StartedAt.Year() != 2025 {
		t.Errorf("DecodeResponse = %+v, %v", r, err)
	}

	_, err = hibapi.DecodeResponse([]byte(`{"v":1,"error":"no machine \"x\""}`))
	var remote *hibapi.RemoteError
	if !errors.As(err, &remote) || remote.Msg != `no machine "x"` {
		t.Errorf("DecodeResponse(error) = %v, want RemoteError", err)
	}

	for _, data := range []string{`{"v":2}`, `{"v":2,"error":"x"}`, `{}`} {
		if _, err := hibapi.DecodeResponse([]byte(data)); !errors.Is(err, hibapi.ErrVersion) {
			t.Errorf("DecodeResponse(%s) = %v, want ErrVersion", data, err)
		}
	}

	for _, data := range []string{``, `not json`, `[1]`, `{"v":"1"}`} {
		if _, err := hibapi.DecodeResponse([]byte(data)); err == nil || errors.Is(err, hibapi.ErrVersion) {
			t.Errorf("DecodeResponse(%q) = %v, want an unreadable-reply error", data, err)
		}
	}
}

func newKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestBridgeHeader(t *testing.T) {
	key := newKey(t)
	var buf bytes.Buffer
	if err := hibapi.WriteBridgeHeader(&buf, key); err != nil {
		t.Fatal(err)
	}
	buf.WriteString("SSH-2.0-OpenSSH\r\n")

	r := bufio.NewReaderSize(&buf, hibapi.MaxHeader)
	got, err := hibapi.ReadBridgeHeader(r)
	if err != nil {
		t.Fatalf("ReadBridgeHeader: %v", err)
	}
	if !bytes.Equal(got.Marshal(), key.Marshal()) {
		t.Errorf("host key = %s, want %s", hibapi.FormatHostKey(got), hibapi.FormatHostKey(key))
	}
	if rest, _ := io.ReadAll(r); string(rest) != "SSH-2.0-OpenSSH\r\n" {
		t.Errorf("after the header: %q, want the machine's bytes", rest)
	}
}

func TestReadBridgeHeaderRejects(t *testing.T) {
	key := hibapi.FormatHostKey(newKey(t))
	for name, input := range map[string]string{
		"empty":        "",
		"no newline":   `{"v":1,"hostKey":"` + key + `"}`,
		"not json":     "SSH-2.0-OpenSSH\r\n",
		"other v":      `{"v":2,"hostKey":"` + key + `"}` + "\n",
		"no key":       `{"v":1}` + "\n",
		"bad key":      `{"v":1,"hostKey":"ssh-ed25519 !!!"}` + "\n",
		"too long":     strings.Repeat(" ", hibapi.MaxHeader+1) + "\n",
		"key, newline": `{"v":1,"hostKey":"` + key + `\nevil.hib ssh-ed25519 AAAA"}` + "\n",
	} {
		_, err := hibapi.ReadBridgeHeader(bufio.NewReaderSize(strings.NewReader(input), hibapi.MaxHeader))
		if err == nil {
			t.Errorf("%s: ReadBridgeHeader = nil, want an error", name)
		}
		if name == "other v" && !errors.Is(err, hibapi.ErrVersion) {
			t.Errorf("%s: ReadBridgeHeader = %v, want ErrVersion", name, err)
		}
	}
}

func TestParseHostKey(t *testing.T) {
	key := newKey(t)
	s := hibapi.FormatHostKey(key)
	if !strings.HasPrefix(s, "ssh-ed25519 AAAA") || strings.ContainsAny(s, "\n\r") {
		t.Errorf("FormatHostKey = %q", s)
	}
	got, err := hibapi.ParseHostKey(s)
	if err != nil || !bytes.Equal(got.Marshal(), key.Marshal()) {
		t.Errorf("ParseHostKey(FormatHostKey(key)) = %v, %v; want key", got, err)
	}

	b64 := strings.TrimPrefix(s, "ssh-ed25519 ")
	ecPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecKey, err := ssh.NewPublicKey(&ecPriv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	ecB64 := base64.StdEncoding.EncodeToString(ecKey.Marshal())
	for _, bad := range []string{
		"",
		b64,
		s + " comment",
		s + "\n",
		" " + s,
		"ssh-ed25519  " + b64,
		"ssh-rsa " + b64,
		"ssh-ed25519 " + ecB64,
		"ecdsa-sha2-nistp256 " + ecB64,
		"ssh-ed25519 " + b64[:len(b64)-4],
		"ssh-ed25519 " + b64 + "==",
		"ssh-ed25519 " + b64[:20] + "\n" + b64[20:],
		"ssh-ed25519 " + b64[:20] + "\r" + b64[20:],
		"ssh-ed25519 " + base64.StdEncoding.EncodeToString(append(key.Marshal(), 0)),
		"ssh-ed25519 not-base64",
	} {
		if _, err := hibapi.ParseHostKey(bad); err == nil {
			t.Errorf("ParseHostKey(%q) = nil, want an error", bad)
		}
	}
}
