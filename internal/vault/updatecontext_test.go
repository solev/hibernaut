package vault_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/solev/hibernaut/internal/vault"
)

func TestUpdateContextCanCancelBeforeTheVaultLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.json")
	lock, err := os.OpenFile(path+".lock", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	err = vault.UpdateContext(ctx, path, filepath.Join(dir, "vault.key"), func(*vault.File, vault.Key) error {
		called = true
		return nil
	})
	if !errors.Is(err, context.Canceled) || called {
		t.Fatal("a canceled update must not acquire the vault lock or run the callback")
	}
	if _, err := os.Stat(filepath.Join(dir, "vault.key")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a canceled update must not create a key")
	}
}
