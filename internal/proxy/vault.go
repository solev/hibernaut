// Package proxy wires the host egress proxy's reload, identity and lifecycle boundaries.
package proxy

import (
	"errors"
	"os"
	"sync"
	"time"

	"github.com/solev/hibernaut/internal/codex"
	"github.com/solev/hibernaut/internal/egress/swap"
	"github.com/solev/hibernaut/internal/vault"
)

// VaultSource reloads host secrets when the vault or key changes. The secrets codex.ScrubOnly
// names are scrub-only.
// Its configuration must not change after the first call to Swapper.
type VaultSource struct {
	Path, KeyPath string
	Now           func() time.Time
	Logf          func(string, ...any)

	mu        sync.Mutex
	loaded    bool
	file, key os.FileInfo
	swapper   *swap.Swapper
	nextLog   time.Time
}

// Swapper returns the current immutable swapper, or nil when secrets cannot be loaded.
// Calls may run concurrently. Errors never log file contents or secret metadata.
func (s *VaultSource) Swapper() *swap.Swapper {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, fileErr := os.Stat(s.Path)
	key, keyErr := os.Stat(s.KeyPath)
	if errors.Is(fileErr, os.ErrNotExist) || errors.Is(keyErr, os.ErrNotExist) {
		s.loaded, s.file, s.key, s.swapper = true, file, key, nil
		return nil
	}
	if fileErr != nil || keyErr != nil {
		s.swapper = nil
		s.logFailure("stat")
		return nil
	}
	if s.loaded && sameFile(s.file, file) && sameFile(s.key, key) {
		return s.swapper
	}
	s.swapper = nil
	vf, err := os.Open(s.Path)
	if err != nil {
		s.logFailure("open vault")
		return nil
	}
	defer vf.Close()
	kf, err := os.Open(s.KeyPath)
	if err != nil {
		s.logFailure("open key")
		return nil
	}
	defer kf.Close()
	file, fileErr = vf.Stat()
	key, keyErr = kf.Stat()
	if fileErr != nil || keyErr != nil {
		s.logFailure("stat opened files")
		return nil
	}
	s.loaded, s.file, s.key = true, file, key
	v, err := vault.LoadFile(vf)
	if err != nil {
		s.logFailure("load vault")
		return nil
	}
	k, err := vault.LoadKeyFile(kf)
	if err != nil {
		s.logFailure("load key")
		return nil
	}
	values, err := v.Open(k)
	if err != nil {
		s.logFailure("decrypt")
		return nil
	}
	secrets := make([]swap.Secret, len(values))
	for i, value := range values {
		secrets[i] = swap.Secret{
			Name: value.Name, Placeholder: value.Placeholder, Value: value.Value, Sites: value.Sites,
			ScrubOnly: codex.ScrubOnly(value.Name),
		}
	}
	sw, err := swap.New(secrets)
	if err != nil {
		s.logFailure("compile swapper")
		return nil
	}
	afterFile, fileErr := vf.Stat()
	afterKey, keyErr := kf.Stat()
	if fileErr != nil || keyErr != nil || !sameFile(file, afterFile) || !sameFile(key, afterKey) {
		s.loaded = false
		s.logFailure("files changed while reading")
		return nil
	}
	if len(secrets) > 0 {
		s.swapper = sw
	}
	return s.swapper
}

func sameFile(a, b os.FileInfo) bool {
	return a != nil && b != nil && os.SameFile(a, b) && a.Size() == b.Size() &&
		a.ModTime().Equal(b.ModTime()) && a.Mode() == b.Mode()
}

func (s *VaultSource) logFailure(stage string) {
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	if s.Logf != nil && !now.Before(s.nextLog) {
		s.Logf("proxy: cannot load secrets (%s). Passing placeholders through until the files change", stage)
		s.nextLog = now.Add(time.Minute)
	}
}
