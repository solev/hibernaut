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

type diskProfile struct {
	Profile
	// This fingerprint recovers Published after the vault saved but the profile did not.
	Placeholders string `json:"placeholders,omitempty"`
}

func (s *Store) readDiskProfile() (diskProfile, error) {
	b, err := os.ReadFile(s.path(ProfileFile))
	if errors.Is(err, os.ErrNotExist) {
		return diskProfile{}, ErrNotConnected
	}
	if err != nil {
		return diskProfile{}, errors.New("reading codex profile")
	}
	var p diskProfile
	if json.Unmarshal(b, &p) != nil || p.AccountID == "" {
		return diskProfile{}, errors.New("invalid codex profile: run hib connect codex")
	}
	return p, nil
}

func (s *Store) readProfile() (Profile, error) {
	p, err := s.readDiskProfile()
	return p.Profile, err
}

func (s *Store) load() (Profile, error) {
	record, err := s.readDiskProfile()
	p := record.Profile
	if err != nil || p.Attempt == nil {
		return p, err
	}
	lock, err := os.OpenFile(s.path(LockFile), os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		return s.recoverProfile(record)
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
	record, err = s.readDiskProfile()
	p = record.Profile
	if err != nil || p.Attempt == nil {
		return p, err
	}
	return s.recoverProfile(record)
}

func (s *Store) recoverProfile(record diskProfile) (Profile, error) {
	f, err := vault.Load(s.cfg.VaultPath)
	if err != nil {
		return Profile{}, errors.New("reading codex vault")
	}
	return s.recoverProfileFrom(record, f)
}

func (s *Store) recoverProfileFrom(record diskProfile, f *vault.File) (Profile, error) {
	p := record.Profile
	t, err := s.openTokens(f)
	if err != nil && !(p.Attempt.Login && errors.Is(err, ErrNotConnected)) {
		return Profile{}, err
	}
	generation := ""
	if err == nil {
		generation = swap.Fingerprint(t.RefreshToken)
	}
	if generation == p.Attempt.Generation {
		if p.Attempt.Login {
			p.Attempt = nil
			return p, nil
		}
		p.Health = HealthStuck
		if p.Problem == "" {
			p.Problem = "a refresh was interrupted: run hib connect codex"
		}
		return p, nil
	}
	updated, err := tokenProfile(t)
	if err != nil {
		return Profile{}, err
	}
	generation = placeholderGeneration(f)
	changed := record.Placeholders != generation && (record.Placeholders != "" || p.Attempt.Login)
	if changed || p.Email != updated.Email || p.Plan != updated.Plan || p.AccountID != updated.AccountID {
		p.Published = p.Attempt.Started
	}
	if p.Attempt.Login {
		updated.ConnectedAt, updated.LastRefresh = p.Attempt.Started, p.Attempt.Started
		updated.Published = p.Published
		return updated, nil
	}
	p.Email, p.Plan, p.AccountID, p.Expires = updated.Email, updated.Plan, updated.AccountID, updated.Expires
	p.LastRefresh, p.Health, p.Problem = p.Attempt.Started, HealthOK, ""
	p.Attempt, p.Failures, p.RetryAt = nil, 0, time.Time{}
	return p, nil
}

func (s *Store) public() (PublicAuth, error) {
	for range 3 {
		record, err := s.readDiskProfile()
		if err != nil {
			return PublicAuth{}, err
		}
		f, err := vault.Load(s.cfg.VaultPath)
		if err != nil {
			return PublicAuth{}, errors.New("reading codex placeholders")
		}
		current, err := s.readDiskProfile()
		if err != nil {
			return PublicAuth{}, err
		}
		if !sameDiskProfile(record, current) {
			continue
		}
		p := record.Profile
		if p.Attempt != nil {
			p, err = s.recoverProfileFrom(record, f)
			if err != nil {
				return PublicAuth{}, err
			}
		}
		out := placeholderTokens(f)
		out.AccountID, out.LastRefresh = p.AccountID, p.Published
		if out.AccessToken == "" || out.RefreshToken == "" || out.IDToken == "" {
			return PublicAuth{}, ErrNotConnected
		}
		return out, nil
	}
	return PublicAuth{}, errors.New("codex login changed while reading: try again")
}

func sameDiskProfile(a, b diskProfile) bool {
	if (a.Attempt == nil) != (b.Attempt == nil) {
		return false
	}
	if a.Attempt != nil && *a.Attempt != *b.Attempt {
		return false
	}
	a.Attempt, b.Attempt = nil, nil
	return a == b
}

func tokenValues(byName map[string]string) (access, id, refresh string) {
	return byName[AccessName], byName[IDName], byName[RefreshName]
}

func placeholderTokens(f *vault.File) PublicAuth {
	byName := make(map[string]string)
	for _, secret := range f.Secrets() {
		byName[secret.Name] = secret.Placeholder
	}
	var out PublicAuth
	out.AccessToken, out.IDToken, out.RefreshToken = tokenValues(byName)
	return out
}

func (s *Store) tokens() (Tokens, error) {
	f, err := vault.Load(s.cfg.VaultPath)
	if err != nil {
		return Tokens{}, errors.New("reading codex vault")
	}
	return s.openTokens(f)
}

func (s *Store) openTokens(f *vault.File) (Tokens, error) {
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
	byName := make(map[string]string)
	for _, v := range values {
		byName[v.Name] = v.Value
	}
	var t Tokens
	t.AccessToken, t.IDToken, t.RefreshToken = tokenValues(byName)
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
		if err := ctx.Err(); err != nil {
			f.Close()
			return nil, err
		}
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
	generation, err := s.placeholderGeneration()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(diskProfile{p, generation}, "", "  ")
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

func (s *Store) placeholderGeneration() (string, error) {
	f, err := vault.Load(s.cfg.VaultPath)
	if err != nil {
		return "", errors.New("reading codex placeholder generation")
	}
	return placeholderGeneration(f), nil
}

func placeholderGeneration(f *vault.File) string {
	t := placeholderTokens(f)
	values := []string{t.AccessToken, t.IDToken, t.RefreshToken}
	if slices.Contains(values, "") {
		return ""
	}
	return swap.Fingerprint(strings.Join(values, "\x00"))
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
	previous, err := s.readDiskProfile()
	if err != nil && !errors.Is(err, ErrNotConnected) {
		return Profile{}, err
	}
	if err == nil {
		if previous.Attempt != nil {
			previous.Profile, err = s.recoverProfile(previous)
			if err != nil {
				return Profile{}, err
			}
			if err := s.saveProfile(previous.Profile); err != nil {
				return Profile{}, err
			}
		}
		old, err := s.tokens()
		if err != nil && !errors.Is(err, ErrNotConnected) {
			return Profile{}, err
		}
		generation := ""
		if err == nil {
			generation = swap.Fingerprint(old.RefreshToken)
		}
		previous.Attempt = &Attempt{Generation: generation, Started: now, Login: true}
		if err := s.saveProfile(previous.Profile); err != nil {
			return Profile{}, err
		}
	}
	publishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshTimeout)
	defer cancel()
	changed, err := s.publishTokens(publishCtx, t, now)
	if err != nil {
		return Profile{}, err
	}
	if !changed && !previous.Published.IsZero() {
		p.Published = previous.Published
	}
	if err := s.saveProfile(p); err != nil {
		return Profile{}, err
	}
	return p, nil
}

func (s *Store) publishTokens(ctx context.Context, t Tokens, now time.Time) (bool, error) {
	old, err := s.tokens()
	if err != nil && !errors.Is(err, ErrNotConnected) {
		return false, err
	}
	if err := s.retire(old, t, now); err != nil {
		return false, err
	}
	changed := false
	err = vault.UpdateContext(ctx, s.cfg.VaultPath, s.cfg.KeyPath, func(f *vault.File, key vault.Key) error {
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
