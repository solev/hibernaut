package vault_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/solev/hibernaut/internal/vault"
)

func TestLoadOpenedFilesSurvivesReplacement(t *testing.T) {
	dir := t.TempDir()
	keyPath, path := filepath.Join(dir, "key"), filepath.Join(dir, "vault.json")
	key, err := vault.CreateKey(keyPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	v, err := vault.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Put(key, "token", "TOKEN", []string{"example.com"}, "sk-original-value"); err != nil {
		t.Fatal(err)
	}
	if err := v.Save(path); err != nil {
		t.Fatal(err)
	}
	vf, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer vf.Close()
	kf, err := os.Open(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer kf.Close()
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.CreateKey(keyPath, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("corrupt replacement"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldVault, err := vault.LoadFile(vf)
	if err != nil {
		t.Fatal(err)
	}
	oldKey, err := vault.LoadKeyFile(kf)
	if err != nil {
		t.Fatal(err)
	}
	values, err := oldVault.Open(oldKey)
	if err != nil || len(values) != 1 || values[0].Value != "sk-original-value" {
		t.Fatalf("opened files changed after replacement: %v", err)
	}
	if err := kf.Chmod(0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := kf.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.LoadKeyFile(kf); err == nil {
		t.Fatal("read a key accessible by others")
	}
}
