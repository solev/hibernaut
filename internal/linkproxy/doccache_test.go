package linkproxy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/links"
)

// countingStore reads a fixed document, or fails with err, and counts its reads.
type countingStore struct {
	doc   links.Doc
	err   error
	reads int
}

func (s *countingStore) Read() (links.Doc, error) {
	s.reads++
	if s.err != nil {
		return links.Doc{}, s.err
	}
	return s.doc, nil
}

func (s *countingStore) Update(context.Context, func(*links.Doc) error) error { return nil }

func (s *countingStore) Fence(context.Context, string, string) (func(), error) {
	return func() {}, nil
}

func TestDocCacheReadsAgainOnlyWhenTheFileChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), links.StateName)
	store := &countingStore{doc: links.Doc{V: links.Version, Next: srcAddr}}
	get := NewDocCache(store, path)

	read := func(wantReads int) *links.Doc {
		t.Helper()
		doc, err := get()
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if store.reads != wantReads {
			t.Fatalf("reads = %d, want %d", store.reads, wantReads)
		}
		return doc
	}

	missing := read(1)
	if again := read(1); again != missing {
		t.Error("a missing file was read again")
	}

	write := func(body string, mtime time.Time) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	write("one", at)
	first := read(2)
	if again := read(2); again != first {
		t.Error("an unchanged file did not share its document")
	}

	write("three", at)
	read(3)
	write("three", at.Add(time.Second))
	read(4)

	moved := path + ".new"
	if err := os.WriteFile(moved, []byte("three"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(moved, at.Add(time.Second), at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(moved, path); err != nil {
		t.Fatal(err)
	}
	read(5)

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	read(6)
}

func TestDocCacheNeverKeepsAFailedRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), links.StateName)
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &countingStore{doc: links.Doc{V: links.Version}, err: links.ErrStateUnreadable}
	get := NewDocCache(store, path)
	for range 2 {
		if _, err := get(); !errors.Is(err, links.ErrStateUnreadable) {
			t.Fatalf("err = %v, want ErrStateUnreadable", err)
		}
	}
	if store.reads != 2 {
		t.Errorf("reads = %d, want 2", store.reads)
	}
	store.err = nil
	if doc, err := get(); err != nil || doc.V != links.Version {
		t.Errorf("after the store recovered: %+v, %v", doc, err)
	}
	if store.reads != 3 {
		t.Errorf("reads = %d, want 3", store.reads)
	}
}
