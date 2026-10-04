package github

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/solev/hibernaut/internal/atomicfile"
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

// Account is the account as hib shows it. Its scopes come from u, GitHub's answer for the token,
// unless u is nil.
func (p Profile) Account(u *User) hibapi.GitHub {
	g := hibapi.GitHub{Login: p.Login, ID: p.ID, Name: p.Name, Email: p.Email(), Source: p.Source}
	if u != nil {
		g.Scopes, g.ScopesKnown = u.Scopes, u.ScopesKnown
	}
	return g
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

// SaveProfile writes p to path atomically, with mode 0644.
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
	return atomicfile.WriteFile(path, append(data, '\n'), 0o644)
}
