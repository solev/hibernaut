package ca_test

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
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
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/ca"
)

var testNow = time.Now().UTC().Truncate(time.Second)

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
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(certPath), ".*.tmp-*")); len(left) > 0 {
		t.Errorf("temp files left: %v", left)
	}
}

func TestInitRecoversAfterTheKeyWasPublished(t *testing.T) {
	original, certPath, keyPath := initCA(t)
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(certPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(certPath), "."+filepath.Base(certPath)+".tmp-interrupted"), []byte("partial certificate"), 0o600); err != nil {
		t.Fatal(err)
	}

	created, err := ca.Init(certPath, keyPath, "Hibernaut CA tests")
	if err != nil || !created {
		t.Fatalf("recovering Init = %v, %v, want true, nil", created, err)
	}
	recovered, err := ca.Load(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	recovered.Now = func() time.Time { return testNow }
	leaf, err := recovered.Leaf("recovered.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leaf.Leaf.Verify(x509.VerifyOptions{
		Roots: original.Pool(), DNSName: "recovered.example.com", CurrentTime: testNow,
	}); err != nil {
		t.Fatalf("recovered CA no longer uses the published key: %v", err)
	}
	if got, err := os.ReadFile(keyPath); err != nil || !bytes.Equal(got, keyPEM) {
		t.Fatal("recovery changed the published key")
	}
	if created, err := ca.Init(certPath, keyPath, "ignored"); err != nil || created {
		t.Fatalf("completed Init = %v, %v, want false, nil", created, err)
	}
}

func TestInitPreservesACertificateWhoseKeyIsMissing(t *testing.T) {
	original, certPath, keyPath := initCA(t)
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	if created, err := ca.Init(certPath, keyPath, "replacement"); err == nil || created {
		t.Fatalf("Init with a missing key = %v, %v, want false and an error", created, err)
	}
	if got, err := os.ReadFile(certPath); err != nil || !bytes.Equal(got, original.CertPEM()) {
		t.Fatal("Init changed an existing trusted certificate")
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

func TestLeafReusesValidCertificatesAndRenewsBeforeExpiry(t *testing.T) {
	c, _, _ := initCA(t)
	first, err := c.Leaf("one.example.com")
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.Leaf("one.example.com")
	if err != nil {
		t.Fatal(err)
	}
	verifyLeaf(t, c, second, "one.example.com", testNow)
	if !bytes.Equal(first.Certificate[0], second.Certificate[0]) {
		t.Fatal("repeat Leaf changed a still-valid certificate")
	}

	c.Now = func() time.Time { return first.Leaf.NotAfter.Add(-23 * time.Hour) }
	renewed, err := c.Leaf("one.example.com")
	if err != nil {
		t.Fatal(err)
	}
	afterExpiry := first.Leaf.NotAfter.Add(time.Minute)
	verifyLeaf(t, c, renewed, "one.example.com", afterExpiry)
	if _, err := first.Leaf.Verify(x509.VerifyOptions{
		Roots: c.Pool(), DNSName: "one.example.com", CurrentTime: afterExpiry,
	}); err == nil {
		t.Fatal("the original certificate is still valid after its expiry")
	}
}

func TestLeafLifetimeDoesNotExceedCALifetime(t *testing.T) {
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
	verifyLeaf(t, shortCA, capped, "two.example.com", testNow)
}

func TestLeafVerifiesAfterCacheOverflow(t *testing.T) {
	c, _, _ := initCA(t)
	c.MaxHosts = 2
	oldest, err := c.Leaf("oldest.example.com")
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"newest.example.com", "overflow.example.com"} {
		leaf, err := c.Leaf(host)
		if err != nil {
			t.Fatal(err)
		}
		verifyLeaf(t, c, leaf, host, testNow)
	}
	reloaded, err := c.Leaf("oldest.example.com")
	if err != nil {
		t.Fatal(err)
	}
	verifyLeaf(t, c, reloaded, "oldest.example.com", testNow)
	if bytes.Equal(reloaded.Certificate[0], oldest.Certificate[0]) {
		t.Fatal("Leaf returned the evicted certificate")
	}
}

func TestLeafConcurrentCallsReturnTrustedCertificates(t *testing.T) {
	c, _, _ := initCA(t)
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
	var verified int
	for cert := range certs {
		verifyLeaf(t, c, cert, "same.example.com", testNow)
		verified++
	}
	if verified != 32 {
		t.Fatalf("received %d trusted certificates, want 32", verified)
	}
}

func verifyLeaf(t *testing.T, c *ca.CA, cert *tls.Certificate, host string, now time.Time) {
	t.Helper()
	if _, err := cert.Leaf.Verify(x509.VerifyOptions{
		Roots: c.Pool(), DNSName: host, CurrentTime: now,
	}); err != nil {
		t.Fatalf("Verify(%s): %v", host, err)
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
		NotBefore:             testNow.Add(-time.Hour),
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

func TestLoadAcceptsP256KeysInSEC1AndPKCS8(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sec1, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	for _, block := range []*pem.Block{{Type: "EC PRIVATE KEY", Bytes: sec1}, {Type: "PRIVATE KEY", Bytes: pkcs8}} {
		t.Run(block.Type, func(t *testing.T) {
			certPath, keyPath := caFiles(t)
			writeKey(t, keyPath, pem.EncodeToMemory(block))
			if created, err := ca.Init(certPath, keyPath, "Hibernaut CA tests"); err != nil || !created {
				t.Fatalf("Init with a %s = %v, %v, want true, nil", block.Type, created, err)
			}
			c, err := ca.Load(certPath, keyPath)
			if err != nil {
				t.Fatal(err)
			}
			c.Now = func() time.Time { return testNow }
			leaf, err := c.Leaf("key.example.com")
			if err != nil {
				t.Fatal(err)
			}
			verifyLeaf(t, c, leaf, "key.example.com", testNow)
			certBlock, _ := pem.Decode(c.CertPEM())
			if certBlock == nil {
				t.Fatal("CertPEM holds no certificate")
			}
			cert, err := x509.ParseCertificate(certBlock.Bytes)
			if err != nil || !key.PublicKey.Equal(cert.PublicKey) {
				t.Fatalf("CA does not use the key on disk: %v", err)
			}
		})
	}
}

func TestLoadRefusesKeysItCannotUse(t *testing.T) {
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, edKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	p384SEC1, err := x509.MarshalECPrivateKey(p384)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		key  []byte
		want string
	}{
		{name: "no PEM", key: []byte("not a key\n"), want: "missing PEM block"},
		{name: "other PEM type", key: pemBlock("RSA PRIVATE KEY", []byte("rsa")), want: `PEM block is "RSA PRIVATE KEY"`},
		{name: "corrupt SEC1", key: pemBlock("EC PRIVATE KEY", []byte("corrupt"))},
		{name: "SEC1 P-384", key: pemBlock("EC PRIVATE KEY", p384SEC1), want: "key is not ECDSA P-256"},
		{name: "corrupt PKCS8", key: pemBlock("PRIVATE KEY", []byte("corrupt"))},
		{name: "PKCS8 P-384", key: pkcs8PEM(t, p384), want: "key is not ECDSA P-256"},
		{name: "PKCS8 Ed25519", key: pkcs8PEM(t, edKey), want: "key is ed25519.PrivateKey, want ECDSA"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			certPath, keyPath := caFiles(t)
			writeKey(t, keyPath, tc.key)
			_, err := ca.Load(certPath, keyPath)
			if err == nil || !strings.HasPrefix(err.Error(), "reading CA key: ") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load = %v, want a CA key error with %q", err, tc.want)
			}
			if created, err := ca.Init(certPath, keyPath, "Hibernaut CA tests"); err == nil || created {
				t.Fatalf("Init with an unusable key = %v, %v, want false and an error", created, err)
			}
			if _, err := os.Stat(certPath); !os.IsNotExist(err) {
				t.Fatalf("Init wrote a certificate for an unusable key: %v", err)
			}
		})
	}
}

func writeKey(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func pemBlock(typ string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
}

func pkcs8PEM(t *testing.T, key crypto.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pemBlock("PRIVATE KEY", der)
}
