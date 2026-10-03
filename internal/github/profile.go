package github

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/solev/hibernaut/internal/hibapi"
)

// ProfileFile is the profile's file name in hibd's state directory.
const ProfileFile = "github.json"

const (
	profileVersion = 1
	maxProfile     = 64 << 10
)

// Profile is the connected account, as ProfileFile keeps it. It never holds the token.
type Profile struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
	// Name is the profile's name, cleaned by CleanName.
	Name string `json:"name"`
	// Source is how the token reached hibd: hibapi.GitHubFromGH or hibapi.GitHubFromToken.
	Source string `json:"source"`
}

type profileFile struct {
	Version int `json:"v"`
	Profile
}

// Email returns the account's noreply address, which machines commit as.
func (p Profile) Email() string {
	return Noreply(p.ID, p.Login)
}

func (p Profile) check() error {
	if err := CheckLogin(p.Login); err != nil {
		return err
	}
	if p.ID <= 0 {
		return fmt.Errorf("bad GitHub ID %d", p.ID)
	}
	if p.Source != hibapi.GitHubFromGH && p.Source != hibapi.GitHubFromToken {
		return fmt.Errorf("bad source %q: want %s or %s", p.Source, hibapi.GitHubFromGH, hibapi.GitHubFromToken)
	}
	return nil
}

// LoadProfile loads the profile at path. It returns nil, and no error, if there is no file:
// GitHub isn't connected.
func LoadProfile(path string) (*Profile, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxProfile+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxProfile {
		return nil, fmt.Errorf("%s is bigger than %d bytes", path, maxProfile)
	}
	var pf profileFile
	if err := json.Unmarshal(data, &pf); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if pf.Version != profileVersion {
		return nil, fmt.Errorf("%s: format version %d isn't supported", path, pf.Version)
	}
	p := pf.Profile
	if err := p.check(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	p.Name = CleanName(p.Name, p.Login)
	return &p, nil
}

// SaveProfile writes p to path atomically, with mode 0644: it holds nothing secret.
func SaveProfile(path string, p Profile) error {
	if err := p.check(); err != nil {
		return err
	}
	if p.Name != CleanName(p.Name, p.Login) {
		return errors.New("the profile's name isn't cleaned")
	}
	data, err := json.MarshalIndent(profileFile{Version: profileVersion, Profile: p}, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".github-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
