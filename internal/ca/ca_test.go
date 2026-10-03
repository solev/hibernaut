package ca_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/ca"
)

var testNow = time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)

func caFiles(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key")
}

func initCA(t *testing.T) (*ca.CA, string, string) {
	t.Helper()
	certPath, keyPath := caFiles(t)
	created, err := ca.Init(certPath, keyPath, "Hibernaut CA tests")
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("Init created = false, want true")
	}
	c, err := ca.Load(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	c.Now = func() time.Time { return testNow }
	return c, certPath, keyPath
}

func TestInitMakesTheCAOnce(t *testing.T) {
	certPath, keyPath := caFiles(t)
	created, err := ca.Init(certPath, keyPath, "Hibernaut CA e2b-test")
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("Init created = false, want true")
	}
	keyInfo, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if keyInfo.Mode().Perm() != 0o600 {
		t.Errorf("key mode = %v, want 0600", keyInfo.Mode().Perm())
	}
	certInfo, err := os.Stat(certPath)
	if err != nil {
		t.Fatal(err)
	}
	if certInfo.Mode().Perm() != 0o644 {
		t.Errorf("cert mode = %v, want 0644", certInfo.Mode().Perm())
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}

	created, err = ca.Init(certPath, keyPath, "ignored")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("second Init created = true, want false")
	}
	if got, err := os.ReadFile(keyPath); err != nil || !bytes.Equal(got, keyPEM) {
		t.Fatalf("key changed on second Init")
	}
	if got, err := os.ReadFile(certPath); err != nil || !bytes.Equal(got, certPEM) {
		t.Fatalf("cert changed on second Init")
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(certPath), ".hibernaut-ca-*")); len(left) > 0 {
		t.Errorf("temp files left: %v", left)
	}
}

func TestInitRefusesHalfState(t *testing.T) {
	for name, write := range map[string]func(certPath, keyPath string) error{
		"only key":  func(_ string, keyPath string) error { return os.WriteFile(keyPath, []byte("key"), 0o600) },
		"only cert": func(certPath, _ string) error { return os.WriteFile(certPath, []byte("cert"), 0o644) },
	} {
		t.Run(name, func(t *testing.T) {
			certPath, keyPath := caFiles(t)
			if err := write(certPath, keyPath); err != nil {
				t.Fatal(err)
			}
			created, err := ca.Init(certPath, keyPath, "Hibernaut CA tests")
			if err == nil {
				t.Fatalf("Init created = %v, want an error", created)
			}
			if !strings.Contains(err.Error(), "remove both files or restore the missing one") {
				t.Fatalf("Init error = %q", err)
			}
		})
	}
}

func TestLoadRefusesUnsafeOrInvalidCA(t *testing.T) {
	t.Run("group-readable key", func(t *testing.T) {
		_, certPath, keyPath := initCA(t)
		if err := os.Chmod(keyPath, 0o640); err != nil {
			t.Fatal(err)
		}
		if _, err := ca.Load(certPath, keyPath); err == nil || !strings.Contains(err.Error(), "key file") {
			t.Fatalf("Load = %v, want a key mode error", err)
		}
	})
	t.Run("mismatched key and cert", func(t *testing.T) {
		_, certPath, _ := initCA(t)
		_, _, otherKeyPath := initCA(t)
		if _, err := ca.Load(certPath, otherKeyPath); err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("Load = %v, want a mismatch error", err)
		}
	})
	t.Run("non-CA cert", func(t *testing.T) {
		certPath, keyPath := caFiles(t)
		writeTestCert(t, certPath, keyPath, certSpec{isCA: false, notAfter: testNow.Add(time.Hour)})
		if _, err := ca.Load(certPath, keyPath); err == nil || !strings.Contains(err.Error(), "not a CA") {
			t.Fatalf("Load = %v, want a non-CA error", err)
		}
	})
	t.Run("expired cert", func(t *testing.T) {
		certPath, keyPath := caFiles(t)
		writeTestCert(t, certPath, keyPath, certSpec{isCA: true, notAfter: time.Now().Add(-time.Hour)})
		if _, err := ca.Load(certPath, keyPath); err == nil || !strings.Contains(err.Error(), "expired") {
			t.Fatalf("Load = %v, want an expired error", err)
		}
	})
}

func TestLeafVerifiesForItsHostOnly(t *testing.T) {
	c, _, _ := initCA(t)
	cert, err := c.Leaf("API.Example.COM")
	if err != nil {
		t.Fatal(err)
	}
	if cert.Leaf == nil {
		t.Fatal("Leaf left tls.Certificate.Leaf nil")
	}
	if got := cert.Leaf.DNSNames; !slices.Equal(got, []string{"api.example.com"}) {
		t.Fatalf("DNSNames = %q, want api.example.com", got)
	}
	opts := x509.VerifyOptions{Roots: c.Pool(), DNSName: "api.example.com", KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, CurrentTime: testNow}
	if _, err := cert.Leaf.Verify(opts); err != nil {
		t.Fatalf("Verify(api.example.com): %v", err)
	}
	opts.DNSName = "other.example.com"
	if _, err := cert.Leaf.Verify(opts); err == nil {
		t.Fatal("Verify(other.example.com) succeeded, want failure")
	}
	for _, host := range []string{"", "127.0.0.1", "::1", "*.example.com", "example.com:443", "bad_name.example.com", "-bad.example.com"} {
		t.Run(host, func(t *testing.T) {
			if cert, err := c.Leaf(host); err == nil {
				t.Fatalf("Leaf(%q) = %+v, want an error", host, cert)
			}
		})
	}
}

func TestLeafWorksInTLSHandshake(t *testing.T) {
	c, _, _ := initCA(t)
	host := "api.example.com"
	serverConfig := &tls.Config{
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			return c.Leaf(hello.ServerName)
		},
	}
	clientConfig := &tls.Config{RootCAs: c.Pool(), ServerName: host, Time: func() time.Time { return testNow }}
	if err := handshake(serverConfig, clientConfig); err != nil {
		t.Fatalf("trusted handshake: %v", err)
	}
	if err := handshake(serverConfig, &tls.Config{ServerName: host, Time: func() time.Time { return testNow }}); err == nil {
		t.Fatal("system roots trusted the Hibernaut CA, want refusal")
	}
}

func TestLeafCacheRenewsCapsAndEvicts(t *testing.T) {
	c, _, _ := initCA(t)
	first, err := c.Leaf("one.example.com")
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.Leaf("one.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("repeat Leaf returned a different pointer")
	}

	c.Now = func() time.Time { return first.Leaf.NotAfter.Add(-23 * time.Hour) }
	renewed, err := c.Leaf("one.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if renewed == first {
		t.Fatal("Leaf reused a cert with less than one day left")
	}

	certPath, keyPath := caFiles(t)
	writeTestCert(t, certPath, keyPath, certSpec{isCA: true, notAfter: testNow.Add(3 * time.Hour)})
	shortCA, err := ca.Load(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	shortCA.Now = func() time.Time { return testNow }
	capped, err := shortCA.Leaf("two.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if want := testNow.Add(3 * time.Hour); !capped.Leaf.NotAfter.Equal(want) {
		t.Fatalf("leaf NotAfter = %v, want CA NotAfter %v", capped.Leaf.NotAfter, want)
	}

	c.MaxHosts = 2
	oldest, err := c.Leaf("oldest.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Leaf("newest.example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Leaf("overflow.example.com"); err != nil {
		t.Fatal(err)
	}
	c.Now = func() time.Time { return testNow }
	reloaded, err := c.Leaf("oldest.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded == oldest {
		t.Fatal("oldest host stayed in a full cache")
	}
}

func TestLeafConcurrentCallsMintOnce(t *testing.T) {
	c, _, _ := initCA(t)
	var minted atomic.Int64
	c.OnMint = func(string) { minted.Add(1) }
	var wg sync.WaitGroup
	certs := make(chan *tls.Certificate, 32)
	errs := make(chan error, 32)
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cert, err := c.Leaf("same.example.com")
			if err != nil {
				errs <- err
				return
			}
			certs <- cert
		}()
	}
	wg.Wait()
	close(certs)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var first *tls.Certificate
	for cert := range certs {
		if first == nil {
			first = cert
		}
		if cert != first {
			t.Fatal("concurrent Leaf calls returned different cert pointers")
		}
	}
	if minted.Load() != 1 {
		t.Fatalf("minted %d certs, want 1", minted.Load())
	}
}

func handshake(serverConfig, clientConfig *tls.Config) error {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()
	errs := make(chan error, 2)
	go func() {
		conn := tls.Server(serverConn, serverConfig)
		defer conn.Close()
		errs <- conn.Handshake()
	}()
	go func() {
		conn := tls.Client(clientConn, clientConfig)
		defer conn.Close()
		errs <- conn.Handshake()
	}()
	var out error
	for range 2 {
		if err := <-errs; err != nil && out == nil {
			out = err
		}
	}
	return out
}

type certSpec struct {
	isCA     bool
	notAfter time.Time
}

func writeTestCert(t *testing.T, certPath, keyPath string, spec certSpec) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Hibernaut CA tests", Organization: []string{"Hibernaut"}},
		NotBefore:             spec.notAfter.Add(-time.Hour),
		NotAfter:              spec.notAfter,
		IsCA:                  spec.isCA,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature,
	}
	if spec.isCA {
		tmpl.KeyUsage |= x509.KeyUsageCertSign | x509.KeyUsageCRLSign
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCertPEMAndPoolHoldOnlyThePublicCertificate(t *testing.T) {
	c, _, _ := initCA(t)
	pemBytes := c.CertPEM()
	if !bytes.Contains(pemBytes, []byte("BEGIN CERTIFICATE")) {
		t.Fatalf("CertPEM = %q, want a certificate", pemBytes)
	}
	if bytes.Contains(pemBytes, []byte("PRIVATE KEY")) {
		t.Fatal("CertPEM returned a private key")
	}
	cert, err := c.Leaf("pool.example.com")
	if err != nil {
		t.Fatal(err)
	}
	_, err = cert.Leaf.Verify(x509.VerifyOptions{Roots: c.Pool(), DNSName: "pool.example.com", CurrentTime: testNow})
	if err != nil {
		t.Fatalf("Verify with Pool: %v", err)
	}
}
