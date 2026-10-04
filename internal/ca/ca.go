// Package ca makes the Hibernaut CA and mints short-lived certificates for bound sites.
package ca

import (
	"bytes"
	"container/list"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/solev/hibernaut/internal/atomicfile"
)

const (
	leafLifetime    = 7 * 24 * time.Hour
	renewLeafAfter  = 24 * time.Hour
	defaultMaxHosts = 1024
)

// CA is the Hibernaut certificate authority.
//
// Its private key stays inside this process.
type CA struct {
	// Now tells the time. Default time.Now.
	Now func() time.Time
	// OnMint is called after Leaf mints a new certificate. It is for tests and metrics.
	OnMint func(host string)
	// MaxHosts is the cache size. Zero uses the default.
	MaxHosts int

	mu      sync.Mutex
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM []byte
	cache   map[string]*cacheEntry
	order   *list.List
}

type cacheEntry struct {
	host string
	cert *tls.Certificate
	elem *list.Element
}

// Init makes the Hibernaut CA, reusing a key left by an interrupted initialization.
//
// If both files exist and Load accepts them, Init leaves them alone.
func Init(certPath, keyPath, commonName string) (bool, error) {
	certExists, err := exists(certPath)
	if err != nil {
		return false, err
	}
	keyExists, err := exists(keyPath)
	if err != nil {
		return false, err
	}
	switch {
	case certExists && keyExists:
		if _, err := Load(certPath, keyPath); err != nil {
			return false, err
		}
		return false, nil
	case certExists:
		return false, fmt.Errorf("CA key is missing at %s: restore it to keep trusting %s", keyPath, certPath)
	}

	var key *ecdsa.PrivateKey
	if keyExists {
		key, err = loadKey(keyPath)
	} else {
		key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	}
	if err != nil {
		return false, err
	}
	now := time.Now()
	serial, err := randomSerial()
	if err != nil {
		return false, err
	}
	subjectKeyID, err := subjectKeyID(&key.PublicKey)
	if err != nil {
		return false, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName, Organization: []string{"Hibernaut"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
		SubjectKeyId:          subjectKeyID,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return false, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	if !keyExists {
		keyDER, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			return false, err
		}
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
		if err := writeNewFile(keyPath, keyPEM, 0o600); err != nil {
			return false, err
		}
	}
	if err := writeNewFile(certPath, certPEM, 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// Load reads the Hibernaut CA from disk.
func Load(certPath, keyPath string) (*CA, error) {
	key, err := loadKey(keyPath)
	if err != nil {
		return nil, err
	}
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, err
	}
	cert, err := parseCert(certPEM)
	if err != nil {
		return nil, fmt.Errorf("reading CA cert: %w", err)
	}
	if !publicKeysEqual(&key.PublicKey, cert.PublicKey) {
		return nil, errors.New("CA key does not match CA cert")
	}
	if !cert.IsCA || cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		return nil, errors.New("CA cert is not a CA")
	}
	if time.Now().After(cert.NotAfter) {
		return nil, fmt.Errorf("CA cert expired at %s", cert.NotAfter.Format(time.RFC3339))
	}
	return &CA{
		Now:     time.Now,
		cert:    cert,
		key:     key,
		certPEM: bytes.Clone(certPEM),
		cache:   map[string]*cacheEntry{},
		order:   list.New(),
	}, nil
}

func loadKey(path string) (*ecdsa.PrivateKey, error) {
	if err := checkKeyFile(path); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	key, err := parseKey(data)
	if err != nil {
		return nil, fmt.Errorf("reading CA key: %w", err)
	}
	return key, nil
}

// Leaf returns a certificate for host.
func (c *CA) Leaf(host string) (*tls.Certificate, error) {
	host, err := cleanHost(host)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if !now.Before(c.cert.NotAfter) {
		return nil, fmt.Errorf("CA cert expired at %s", c.cert.NotAfter.Format(time.RFC3339))
	}
	if entry := c.cache[host]; entry != nil && entry.cert.Leaf.NotAfter.Sub(now) >= renewLeafAfter {
		c.order.MoveToBack(entry.elem)
		return entry.cert, nil
	}
	cert, err := c.mint(host, now)
	if err != nil {
		return nil, err
	}
	if entry := c.cache[host]; entry != nil {
		entry.cert = cert
		c.order.MoveToBack(entry.elem)
	} else {
		elem := c.order.PushBack(host)
		c.cache[host] = &cacheEntry{host: host, cert: cert, elem: elem}
	}
	c.evict()
	if c.OnMint != nil {
		c.OnMint(host)
	}
	return cert, nil
}

// CertPEM returns the public CA certificate in PEM form.
func (c *CA) CertPEM() []byte {
	return bytes.Clone(c.certPEM)
}

// Pool returns a pool that trusts the CA.
func (c *CA) Pool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(c.cert)
	return pool
}

func (c *CA) now() time.Time {
	if c.Now == nil {
		return time.Now()
	}
	return c.Now()
}

func (c *CA) mint(host string, now time.Time) (*tls.Certificate, error) {
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	notAfter := now.Add(leafLifetime)
	if c.cert.NotAfter.Before(notAfter) {
		notAfter = c.cert.NotAfter
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{
		Certificate: [][]byte{der, c.cert.Raw},
		PrivateKey:  key,
		Leaf:        mustParseCert(der),
	}, nil
}

func (c *CA) evict() {
	limit := c.MaxHosts
	if limit == 0 {
		limit = defaultMaxHosts
	}
	for len(c.cache) > limit {
		elem := c.order.Front()
		if elem == nil {
			return
		}
		host := elem.Value.(string)
		delete(c.cache, host)
		c.order.Remove(elem)
	}
}

func exists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func writeNewFile(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return atomicfile.WriteNewFile(path, data, mode)
}

func checkKeyFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("key file %s is not a regular file", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("key file %s is accessible by group or others", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("key file %s owner is unknown", path)
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("key file %s is owned by uid %d, want uid %d", path, stat.Uid, os.Geteuid())
	}
	return nil
}

func parseKey(data []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("missing PEM block")
	}
	if block.Type == "EC PRIVATE KEY" {
		key, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		if key.Curve != elliptic.P256() {
			return nil, errors.New("key is not ECDSA P-256")
		}
		return key, nil
	}
	if block.Type == "PRIVATE KEY" {
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		ecKey, ok := key.(*ecdsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("key is %T, want ECDSA", key)
		}
		if ecKey.Curve != elliptic.P256() {
			return nil, errors.New("key is not ECDSA P-256")
		}
		return ecKey, nil
	}
	return nil, fmt.Errorf("PEM block is %q, want EC PRIVATE KEY", block.Type)
}

func parseCert(data []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("missing certificate PEM block")
	}
	return x509.ParseCertificate(block.Bytes)
}

func mustParseCert(der []byte) *x509.Certificate {
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		panic(err)
	}
	return cert
}

func publicKeysEqual(key *ecdsa.PublicKey, other any) bool {
	otherKey, ok := other.(*ecdsa.PublicKey)
	if !ok {
		return false
	}
	return key.Equal(otherKey)
}

func randomSerial() (*big.Int, error) {
	for {
		serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		if err != nil {
			return nil, err
		}
		if serial.Sign() > 0 {
			return serial, nil
		}
	}
}

func subjectKeyID(pub *ecdsa.PublicKey) ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, err
	}
	sum := sha1.Sum(der)
	return sum[:], nil
}

func cleanHost(host string) (string, error) {
	if host == "" {
		return "", errors.New("host is empty")
	}
	if strings.Contains(host, "*") {
		return "", fmt.Errorf("host %q uses a wildcard", host)
	}
	if strings.Contains(host, ":") {
		return "", fmt.Errorf("host %q includes a port or is an IP address", host)
	}
	host = strings.ToLower(host)
	if net.ParseIP(host) != nil {
		return "", fmt.Errorf("host %q is an IP address", host)
	}
	if len(host) > 253 {
		return "", fmt.Errorf("host %q is too long", host)
	}
	labels := strings.Split(host, ".")
	for _, label := range labels {
		if label == "" {
			return "", fmt.Errorf("host %q is not a valid hostname", host)
		}
		if len(label) > 63 {
			return "", fmt.Errorf("host %q has a label longer than 63 bytes", host)
		}
		for i, r := range label {
			switch {
			case r > unicode.MaxASCII:
				return "", fmt.Errorf("host %q is not an ASCII hostname", host)
			case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			case r == '-' && i > 0 && i < len(label)-1:
			default:
				return "", fmt.Errorf("host %q is not a valid hostname", host)
			}
		}
	}
	return host, nil
}
