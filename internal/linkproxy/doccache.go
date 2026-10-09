package linkproxy

import (
	"errors"
	"io/fs"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/solev/hibernaut/internal/links"
)

// NewDocCache returns a Config.Doc that reads store, whose file is at path, again only when the
// file's modification time, size or inode has changed since the last read. A failed read is
// never kept, so the next connection tries again.
//
// The Doc it returns is shared between every caller until the file changes, and must not be
// modified.
func NewDocCache(store links.Store, path string) func() (*links.Doc, error) {
	c := &docCache{store: store, path: path}
	return c.get
}

type docCache struct {
	store links.Store
	path  string

	mu  sync.Mutex
	key fileKey
	doc *links.Doc
}

type fileKey struct {
	exists bool
	mtime  time.Time
	size   int64
	inode  uint64
}

func (c *docCache) get() (*links.Doc, error) {
	key, err := statKey(c.path)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.doc != nil && c.key == key {
		return c.doc, nil
	}
	doc, err := c.store.Read()
	if err != nil {
		c.doc = nil
		return nil, err
	}
	c.key, c.doc = key, &doc
	return c.doc, nil
}

func statKey(path string) (fileKey, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return fileKey{}, nil
	}
	if err != nil {
		return fileKey{}, err
	}
	key := fileKey{exists: true, mtime: info.ModTime(), size: info.Size()}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		key.inode = uint64(st.Ino)
	}
	return key, nil
}
