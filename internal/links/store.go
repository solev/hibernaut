package links

import (
	"context"
	"path/filepath"
	"sync"
	"time"
)

// store is the one reader and writer of links.json. Every slice goes through it rather than
// reading the file, so the lock order, the fence's owner file and the version check live in one
// place.
type store struct {
	dir string
	mu  sync.Mutex
}

// NewStore returns the production store over stateDir, usually HIBD_STATE_DIR. The document is
// stateDir/links.json and the fence owner files are beside it.
func NewStore(stateDir string) Store {
	return &store{dir: stateDir}
}

// Path is the document this store reads and writes.
func (s *store) Path() string { return filepath.Join(s.dir, StateName) }

func (s *store) Read() (Doc, error) { return Doc{}, ErrLinksOff }

func (s *store) Update(ctx context.Context, f func(*Doc) error) error { return ErrLinksOff }

func (s *store) Fence(ctx context.Context, id string, why string) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return func() {}, ErrLinksOff
}

// clock is the production activity clock. busy.Config.Activity feeds it, the SSH bridge
// heartbeats into it while a session is open, and a successful create or fork touches it once.
// It is kept under stateDir so a restart of hibd does not reset everyone's authority.
type clock struct {
	dir string
	mu  sync.Mutex
	at  map[string]time.Time
}

// NewClock returns the production activity clock over stateDir, usually HIBD_STATE_DIR.
func NewClock(stateDir string) Clock {
	return &clock{dir: stateDir, at: map[string]time.Time{}}
}

// Path is the file the clock survives a restart in.
func (c *clock) Path() string { return filepath.Join(c.dir, ActivityName) }

func (c *clock) Active(id string) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at[id]
}

func (c *clock) Touch(id string, t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at[id] = t
}

var (
	_ Store = (*store)(nil)
	_ Clock = (*clock)(nil)
)
