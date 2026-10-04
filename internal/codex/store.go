package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/solev/hibernaut/internal/egress/swap"
	"github.com/solev/hibernaut/internal/vault"
)

func (s *Store) path(name string) string { return filepath.Join(s.cfg.StateDir, name) }

func (s *Store) readProfile() (Profile, error) {
	b, err := os.ReadFile(s.path(ProfileFile))
	if errors.Is(err, os.ErrNotExist) {
		return Profile{}, ErrNotConnected
	}
	if err != nil {
		return Profile{}, errors.New("reading codex profile")
	}
	var p Profile
	if json.Unmarshal(b, &p) != nil || p.AccountID == "" {
		return Profile{}, errors.New("invalid codex profile: run hib connect codex")
	}
	return p, nil
}

func (s *Store) load() (Profile, error) {
	p, err := s.readProfile()
	if err != nil || p.Attempt == nil {
		return p, err
	}
	lock, err := os.OpenFile(s.path(LockFile), os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		return s.recoverProfile(p)
	}
	if err != nil {
		return Profile{}, errors.New("checking codex refresh lock")
	}
	defer lock.Close()
	err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return p, nil
	}
	if err != nil {
		return Profile{}, errors.New("checking codex refresh lock")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return s.recoverProfile(p)
}

func (s *Store) recoverProfile(p Profile) (Profile, error) {
	t, err := s.tokens()
	if err != nil {
		return Profile{}, err
	}
	if swap.Fingerprint(t.RefreshToken) == p.Attempt.Generation {
		p.Health = HealthStuck
		p.Problem = "a refresh was interrupted: run hib connect codex"
		return p, nil
	}
	updated, err := tokenProfile(t)
	if err != nil {
		return Profile{}, err
	}
	if p.Email != updated.Email || p.Plan != updated.Plan || p.AccountID != updated.AccountID {
		p.Published = p.Attempt.Started
	}
	p.Email, p.Plan, p.AccountID, p.Expires = updated.Email, updated.Plan, updated.AccountID, updated.Expires
	p.LastRefresh, p.Health, p.Problem = p.Attempt.Started, HealthOK, ""
	p.Attempt, p.Failures, p.RetryAt = nil, 0, time.Time{}
	return p, nil
}

func (s *Store) public() (PublicAuth, error) {
	p, err := s.readProfile()
	if err != nil {
		return PublicAuth{}, err
	}
	f, err := vault.Load(s.cfg.VaultPath)
	if err != nil {
		return PublicAuth{}, errors.New("reading codex placeholders")
	}
	out := PublicAuth{AccountID: p.AccountID, LastRefresh: p.Published}
	for _, secret := range f.Secrets() {
		switch secret.Name {
		case AccessName:
			out.AccessToken = secret.Placeholder
		case RefreshName:
			out.RefreshToken = secret.Placeholder
		case IDName:
			out.IDToken = secret.Placeholder
		}
	}
	if out.AccessToken == "" || out.RefreshToken == "" || out.IDToken == "" {
		return PublicAuth{}, ErrNotConnected
	}
	return out, nil
}

func (s *Store) tokens() (Tokens, error) {
	f, err := vault.Load(s.cfg.VaultPath)
	if err != nil {
		return Tokens{}, errors.New("reading codex vault")
	}
	if len(f.Secrets()) == 0 {
		return Tokens{}, ErrNotConnected
	}
	key, err := vault.LoadKey(s.cfg.KeyPath)
	if err != nil {
		return Tokens{}, errors.New("opening codex vault: restore the vault key")
	}
	values, err := f.Open(key)
	if err != nil {
		return Tokens{}, errors.New("opening codex tokens: restore the vault key")
	}
	var t Tokens
	for _, v := range values {
		switch v.Name {
		case AccessName:
			t.AccessToken = v.Value
		case RefreshName:
			t.RefreshToken = v.Value
		case IDName:
			t.IDToken = v.Value
		}
	}
	if t.AccessToken == "" || t.RefreshToken == "" || t.IDToken == "" {
		return Tokens{}, ErrNotConnected
	}
	c, err := decodeClaims(t.AccessToken)
	if err != nil {
		return Tokens{}, err
	}
	t.AccountID = c.Auth.Account
	return t, nil
}

func (s *Store) lock(ctx context.Context) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(s.path(LockFile), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, errors.New("opening codex lock")
	}
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			f.Close()
			return nil, errors.New("locking codex login")
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-tick.C:
		}
	}
}

func unlock(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = f.Close()
}

func (s *Store) saveProfile(p Profile) error {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return errors.New("encoding codex profile")
	}
	f, err := os.CreateTemp(s.cfg.StateDir, ".codex-*")
	if err != nil {
		return errors.New("creating codex profile")
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chmod(0o644); err != nil {
		return errors.New("setting codex profile mode")
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		return errors.New("writing codex profile")
	}
	if err := f.Sync(); err != nil {
		return errors.New("syncing codex profile")
	}
	if err := f.Close(); err != nil {
		return errors.New("closing codex profile")
	}
	if err := os.Rename(f.Name(), s.path(ProfileFile)); err != nil {
		return errors.New("publishing codex profile")
	}
	dir, err := os.Open(s.cfg.StateDir)
	if err != nil {
		return errors.New("opening codex state directory")
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return errors.New("syncing codex state directory")
	}
	return nil
}

func (s *Store) commitLogin(ctx context.Context, t Tokens) (Profile, error) {
	p, err := tokenProfile(t)
	if err != nil {
		return Profile{}, err
	}
	lock, err := s.lock(ctx)
	if err != nil {
		return Profile{}, err
	}
	defer unlock(lock)
	now := s.cfg.Now().UTC()
	p.ConnectedAt, p.LastRefresh, p.Published = now, now, now
	previous, err := s.public()
	if err != nil && !errors.Is(err, ErrNotConnected) {
		return Profile{}, err
	}
	changed, err := s.publishTokens(t, now)
	if err != nil {
		return Profile{}, err
	}
	if !changed && err == nil && !previous.LastRefresh.IsZero() {
		p.Published = previous.LastRefresh
	}
	if err := s.saveProfile(p); err != nil {
		return Profile{}, err
	}
	return p, nil
}

func (s *Store) publishTokens(t Tokens, now time.Time) (bool, error) {
	old, err := s.tokens()
	if err != nil && !errors.Is(err, ErrNotConnected) {
		return false, err
	}
	if err := s.retire(old, t, now); err != nil {
		return false, err
	}
	changed := false
	err = vault.Update(s.cfg.VaultPath, s.cfg.KeyPath, func(f *vault.File, key vault.Key) error {
		previous := map[string]string{}
		for _, secret := range f.Secrets() {
			previous[secret.Name] = secret.Placeholder
		}
		for _, v := range []struct {
			name, value string
			sites       []string
		}{
			{AccessName, t.AccessToken, AccessSites()},
			{RefreshName, t.RefreshToken, AuthSites()},
			{IDName, t.IDToken, AuthSites()},
		} {
			secret, err := f.Put(key, v.name, "", v.sites, v.value)
			if err != nil {
				return errors.New("sealing codex tokens")
			}
			changed = changed || previous[v.name] != secret.Placeholder
		}
		return nil
	})
	if err != nil {
		return false, errors.New("publishing codex tokens: check the vault and its key")
	}
	return changed, nil
}

type retiredRecord struct {
	vault.Revealed
	original string
	dropped  int64
	ordinal  int
}

func retirementName(name string) (string, int64, int) {
	for _, original := range []string{AccessName, RefreshName, IDName} {
		if suffix, ok := strings.CutPrefix(name, original+"-"); ok {
			parts := strings.Split(suffix, "-")
			dropped, err := strconv.ParseInt(parts[0], 10, 64)
			if err != nil {
				return "", 0, 0
			}
			ordinal := 1
			if len(parts) == 2 {
				ordinal, _ = strconv.Atoi(parts[1])
			}
			return original, dropped, ordinal
		}
	}
	return "", 0, 0
}

func activeRetired(v vault.Revealed, now time.Time) (retiredRecord, bool) {
	name, dropped, ordinal := retirementName(v.Name)
	if name == "" {
		return retiredRecord{}, false
	}
	expires := time.Unix(dropped, 0).Add(30 * 24 * time.Hour)
	if name != RefreshName {
		c, err := decodeClaims(v.Value)
		if err != nil {
			return retiredRecord{}, false
		}
		expires = time.Unix(c.Expires, 0)
	}
	return retiredRecord{v, name, dropped, ordinal}, now.Before(expires)
}

func (s *Store) retiredRecords(f *vault.File, now time.Time) ([]retiredRecord, error) {
	if len(f.Secrets()) == 0 {
		return nil, nil
	}
	key, err := vault.LoadKey(s.cfg.KeyPath)
	if err != nil {
		return nil, errors.New("opening retired codex values: restore the vault key")
	}
	values, err := f.Open(key)
	if err != nil {
		return nil, errors.New("opening retired codex values")
	}
	var out []retiredRecord
	for _, value := range values {
		if v, active := activeRetired(value, now); active {
			out = append(out, v)
		}
	}
	slices.SortFunc(out, func(a, b retiredRecord) int {
		if a.dropped != b.dropped {
			return intCompare(a.dropped, b.dropped)
		}
		if a.ordinal != b.ordinal {
			return a.ordinal - b.ordinal
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out, nil
}

func intCompare(a, b int64) int {
	if a < b {
		return -1
	}
	return 1
}

func (s *Store) retire(old, next Tokens, now time.Time) error {
	f, err := vault.Load(s.path(RetiredFile))
	if err != nil {
		return errors.New("reading retired codex values")
	}
	records, err := s.retiredRecords(f, now)
	if err != nil {
		return err
	}
	active := map[string]bool{}
	for _, r := range records {
		active[r.Name] = true
	}
	for _, v := range f.Secrets() {
		if !active[v.Name] {
			_ = f.Remove(v.Name)
		}
	}
	replaced := []struct{ name, old, next string }{
		{AccessName, old.AccessToken, next.AccessToken},
		{IDName, old.IDToken, next.IDToken},
		{RefreshName, old.RefreshToken, next.RefreshToken},
	}
	for _, v := range replaced {
		if v.old == "" || v.old == v.next || slices.ContainsFunc(records, func(r retiredRecord) bool { return r.original == v.name && r.Value == v.old }) {
			continue
		}
		name := fmt.Sprintf("%s-%d", v.name, now.Unix())
		for n := 2; active[name]; n++ {
			name = fmt.Sprintf("%s-%d-%d", v.name, now.Unix(), n)
		}
		value := vault.Revealed{Secret: vault.Secret{Name: name}, Value: v.old}
		if _, keep := activeRetired(value, now); !keep {
			continue
		}
		// Retirement never creates a key. A replaced value already has one.
		key, err := vault.LoadKey(s.cfg.KeyPath)
		if err != nil {
			return errors.New("retiring codex values: restore the vault key")
		}
		if _, err := f.Put(key, name, "", AuthSites(), v.old); err != nil {
			return errors.New("sealing retired codex values")
		}
		active[name] = true
	}
	records, err = s.retiredRecords(f, now)
	if err != nil {
		return err
	}
	for len(records) > 100 {
		_ = f.Remove(records[0].Name)
		records = records[1:]
	}
	if len(f.Secrets()) == 0 {
		if _, err := os.Stat(s.path(RetiredFile)); errors.Is(err, os.ErrNotExist) {
			return nil
		}
	}
	if err := f.Save(s.path(RetiredFile)); err != nil {
		return errors.New("saving retired codex values")
	}
	return nil
}

func (s *Store) retired() ([]OldValue, error) {
	f, err := vault.Load(s.path(RetiredFile))
	if err != nil {
		return nil, errors.New("reading retired codex values")
	}
	records, err := s.retiredRecords(f, s.cfg.Now())
	if err != nil {
		return nil, err
	}
	out := make([]OldValue, len(records))
	for i, r := range records {
		out[i] = OldValue{Name: r.original, Value: r.Value}
	}
	return out, nil
}
