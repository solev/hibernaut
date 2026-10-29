package e2b

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"sync"
	"syscall"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/statefile"
)

var tokenID = regexp.MustCompile(`^[a-z0-9]{1,64}$`)

type trafficTokens struct {
	dir    string
	memory sync.Map
}

func tokenName(id string) (string, error) {
	if !tokenID.MatchString(id) {
		return "", fmt.Errorf("traffic token: unexpected machine ID %q", id)
	}
	return "traffic-token-" + id, nil
}

func (s *trafficTokens) store(id string, token *string) error {
	value := ""
	if token != nil {
		value = *token
	}
	if s.dir == "" {
		s.memory.Store(id, value)
		return nil
	}
	name, err := tokenName(id)
	if err != nil {
		return err
	}
	d := statefile.Dir{Path: s.dir}
	root, owner, err := d.Root()
	if err != nil {
		return fmt.Errorf("storing traffic token: %w", err)
	}
	defer root.Close()
	f, err := d.PublishOwned(root, name, []byte(value), owner, true)
	if err != nil {
		return fmt.Errorf("storing traffic token: %w", err)
	}
	return f.Close()
}

func (s *trafficTokens) load(id string) (string, error) {
	if s.dir == "" {
		if value, ok := s.memory.Load(id); ok {
			return value.(string), nil
		}
		return "", engine.ErrNoTrafficToken
	}
	name, err := tokenName(id)
	if err != nil {
		return "", err
	}
	root, owner, err := (statefile.Dir{Path: s.dir}).Root()
	if err != nil {
		return "", err
	}
	defer root.Close()
	fi, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return "", engine.ErrNoTrafficToken
	}
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0o077 != 0 {
		return "", errors.New("traffic token needs a private regular file")
	}
	f, err := root.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !os.SameFile(fi, opened) {
		return "", errors.New("traffic token changed while opening it")
	}
	st, ok := opened.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != owner.UID {
		return "", errors.New("traffic token must belong to the state directory's owner")
	}
	data, err := io.ReadAll(io.LimitReader(f, 64<<10+1))
	if err != nil {
		return "", err
	}
	if len(data) > 64<<10 {
		return "", errors.New("traffic token exceeds 64 KiB")
	}
	return string(data), nil
}

func (s *trafficTokens) remove(id string) error {
	if s.dir == "" {
		s.memory.Delete(id)
		return nil
	}
	name, err := tokenName(id)
	if err != nil {
		return err
	}
	root, _, err := (statefile.Dir{Path: s.dir}).Root()
	if err != nil {
		return err
	}
	defer root.Close()
	err = root.Remove(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
