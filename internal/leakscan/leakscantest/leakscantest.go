// Package leakscantest writes small E2B build fixtures for leak scan tests.
package leakscantest

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Mapping is one logical range in an E2B snapshot header.
type Mapping struct {
	Offset        uint64
	Length        uint64
	Build         string
	StorageOffset uint64
}

// WriteBuild writes a complete memory snapshot with version 3 headers.
func WriteBuild(t testing.TB, storage, build string, mem, disk []byte) {
	t.Helper()
	WriteBuildFile(t, storage, build, "memfile", mem)
	WriteBuildFile(t, storage, build, "rootfs.ext4", disk)
	WriteHeader(t, storage, build, "memfile.header", uint64(len(mem)),
		Mapping{Length: uint64(len(mem)), Build: build})
	WriteHeader(t, storage, build, "rootfs.ext4.header", uint64(len(disk)),
		Mapping{Length: uint64(len(disk)), Build: build})
	WriteBuildFile(t, storage, build, "snapfile", []byte("VM state"))
	meta, err := json.Marshal(map[string]any{
		"version":  2,
		"template": map[string]string{"build_id": build},
	})
	if err != nil {
		t.Fatal(err)
	}
	WriteBuildFile(t, storage, build, "metadata.json", meta)
}

// WriteBuildFile writes name in storage/build.
func WriteBuildFile(t testing.TB, storage, build, name string, data []byte) {
	t.Helper()
	dir := filepath.Join(storage, build)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// WriteHeader writes an E2B version 3 snapshot header in storage/build.
func WriteHeader(t testing.TB, storage, build, name string, size uint64, maps ...Mapping) {
	t.Helper()
	WriteHeaderVersion(t, storage, build, name, 3, size, maps...)
}

// WriteHeaderVersion writes an E2B snapshot header in storage/build.
func WriteHeaderVersion(t testing.TB, storage, build, name string, version, size uint64, maps ...Mapping) {
	t.Helper()
	dir := filepath.Join(storage, build)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 64+40*len(maps))
	binary.LittleEndian.PutUint64(b[0:8], version)
	binary.LittleEndian.PutUint64(b[8:16], 4096)
	binary.LittleEndian.PutUint64(b[16:24], size)
	copyUUID(t, b[32:48], build)
	copyUUID(t, b[48:64], build)
	for i, m := range maps {
		p := b[64+i*40:]
		binary.LittleEndian.PutUint64(p[0:8], m.Offset)
		binary.LittleEndian.PutUint64(p[8:16], m.Length)
		copyUUID(t, p[16:32], m.Build)
		binary.LittleEndian.PutUint64(p[32:40], m.StorageOffset)
	}
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func copyUUID(t testing.TB, dst []byte, id string) {
	t.Helper()
	raw, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
	if err != nil {
		t.Fatal(err)
	}
	copy(dst, raw)
}
