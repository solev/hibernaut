package audit_test

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/audit"
)

func TestWriteReadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	log := openLog(t, path, 1<<20)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("log mode = %v, want 0644", got)
	}
	entries := []audit.Entry{
		{
			Time:      time.Date(2026, 10, 3, 7, 0, 0, 0, time.UTC),
			Machine:   "web",
			MachineID: "sbx-1",
			Site:      "api.github.com",
			Method:    "GET",
			Path:      "/user?token=placeholder",
			Secrets:   []string{"GH_TOKEN"},
		},
		{
			Time:    time.Date(2026, 10, 3, 7, 1, 0, 0, time.UTC),
			Site:    "api.openai.com",
			Method:  "POST",
			Path:    "/v1/chat/completions",
			Secrets: []string{"OPENAI_API_KEY"},
		},
	}
	for _, entry := range entries {
		if err := log.Write(entry); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := audit.Read(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	entries[0].Path = "/user"
	if got.Corrupt != 0 || !reflect.DeepEqual(got.Entries, entries) {
		t.Errorf("Read = %#v, want entries %#v and no corrupt lines", got, entries)
	}
}

func TestRotationReadsOldAndCurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	log := openLog(t, path, 320)
	entries := []audit.Entry{
		entry("one", "/one"),
		entry("two", "/two"),
		entry("three", "/three"),
	}
	for _, entry := range entries {
		if err := log.Write(entry); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("rotated log: %v", err)
	}
	got, err := audit.Read(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Entries, entries) {
		t.Errorf("Read across rotation = %#v, want %#v", got.Entries, entries)
	}
}

func TestConcurrentWritersProduceWholeLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	log := openLog(t, path, 1<<20)
	const writers = 8
	const perWriter = 50
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := range perWriter {
				if err := log.Write(entry("web", "/"+strings.Repeat("x", i+j+1))); err != nil {
					t.Errorf("Write: %v", err)
				}
			}
		}(i)
	}
	wg.Wait()
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	lines := 0
	for scanner.Scan() {
		lines++
		var e audit.Entry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			t.Fatalf("line %d is not whole JSON: %q: %v", lines, scanner.Text(), err)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if want := writers * perWriter; lines != want {
		t.Errorf("line count = %d, want %d", lines, want)
	}
}

func TestReadSkipsCorruptLinesAndLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	first := entry("one", "/one")
	second := entry("two", "/two")
	writeJSONLines(t, path+".1", first)
	if err := os.WriteFile(path, []byte(`{"bad":`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	if err := enc.Encode(second); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"truncated"`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := audit.Read(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Corrupt != 2 {
		t.Errorf("Corrupt = %d, want 2", got.Corrupt)
	}
	if !reflect.DeepEqual(got.Entries, []audit.Entry{second}) {
		t.Errorf("limited entries = %#v, want %#v", got.Entries, []audit.Entry{second})
	}
}

func TestReadMissingFiles(t *testing.T) {
	got, err := audit.Read(filepath.Join(t.TempDir(), "missing.jsonl"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.Corrupt != 0 || len(got.Entries) != 0 {
		t.Errorf("Read missing = %#v, want empty result", got)
	}
}

func openLog(t *testing.T, path string, maxBytes int64) *audit.Log {
	t.Helper()
	log, err := audit.Open(path, maxBytes)
	if err != nil {
		t.Fatal(err)
	}
	return log
}

func entry(machine, path string) audit.Entry {
	return audit.Entry{
		Time:      time.Date(2026, 10, 3, 7, 0, 0, 0, time.UTC),
		Machine:   machine,
		MachineID: "sbx-" + machine,
		Site:      "api.github.com",
		Method:    "GET",
		Path:      path,
		Secrets:   []string{"GH_TOKEN"},
	}
}

func writeJSONLines(t *testing.T, path string, entries ...audit.Entry) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	for _, entry := range entries {
		if err := enc.Encode(entry); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
