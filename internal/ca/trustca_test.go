package ca_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTrustCAKeepsEnvironmentAfterInterruptedPublication(t *testing.T) {
	c, _, _ := initCA(t)
	root := t.TempDir()
	for _, dir := range []string{"etc/ssl/certs", "usr/local/share/ca-certificates", "bin"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{
		"etc/ssl/certs/ca-certificates.crt", "usr/local/share/ca-certificates/hibernaut.crt",
	} {
		if err := os.WriteFile(filepath.Join(root, path), c.CertPEM(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	envPath := filepath.Join(root, "etc/environment")
	original := "KEEP=this-value\nSSL_CERT_FILE=old-bundle\nNODE_EXTRA_CA_CERTS=old-ca\n"
	if err := os.WriteFile(envPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	writeCommand := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, "bin", name), []byte("#!/bin/bash\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeCommand("update-ca-certificates", "exit 0\n")
	writeCommand("cat", "if [[ $# -gt 0 ]]; then printf 'interrupted copy\\n'; exit 1; fi\nexec /bin/cat\n")
	writeCommand("mv", "exit 1\n")
	run := func() ([]byte, error) {
		cmd := exec.Command("bash", "../../images/dev/trust-ca", root)
		cmd.Env = append(os.Environ(), "PATH="+filepath.Join(root, "bin")+":"+os.Getenv("PATH"))
		return cmd.CombinedOutput()
	}
	if out, err := run(); err == nil {
		t.Fatalf("trust-ca succeeded during an interrupted publication: %s", out)
	}
	if got, err := os.ReadFile(envPath); err != nil || string(got) != original {
		t.Fatalf("environment after interrupted publication = %q, %v, want %q", got, err, original)
	}
	for _, name := range []string{"cat", "mv"} {
		if err := os.Remove(filepath.Join(root, "bin", name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(envPath+".hibernaut-interrupted", []byte("partial environment"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := `KEEP=this-value
NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/hibernaut.crt
SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt
REQUESTS_CA_BUNDLE=/etc/ssl/certs/ca-certificates.crt
CODEX_CA_CERTIFICATE=/etc/ssl/certs/ca-certificates.crt
`
	for range 2 {
		if out, err := run(); err != nil {
			t.Fatalf("trust-ca retry: %v, %s", err, out)
		}
		got, err := os.ReadFile(envPath)
		if err != nil || string(got) != want {
			t.Fatalf("environment after retry = %q, %v, want %q", got, err, want)
		}
		info, err := os.Stat(envPath)
		if err != nil || info.Mode().Perm() != 0o644 {
			t.Fatalf("environment mode = %v, %v, want 0644", info, err)
		}
	}
	if err := os.Remove(envPath); err != nil {
		t.Fatal(err)
	}
	if out, err := run(); err != nil {
		t.Fatalf("trust-ca with no environment: %v, %s", err, out)
	}
	want = strings.TrimPrefix(want, "KEEP=this-value\n")
	if got, err := os.ReadFile(envPath); err != nil || string(got) != want {
		t.Fatalf("initial environment = %q, %v, want %q", got, err, want)
	}
}
