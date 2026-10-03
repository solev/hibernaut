package leakscan

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var errIncomplete = errors.New("snapshot files are incomplete")

type logicalFile struct {
	data, where string
	header      header
}

func readBuildSet(storageDir, buildID string) (map[string][]logicalFile, error) {
	builds := map[string][]logicalFile{}
	pending := []string{buildID}
	for len(pending) > 0 {
		id := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if _, ok := builds[id]; ok {
			continue
		}
		files, err := readBuild(storageDir, id)
		if err != nil {
			return nil, err
		}
		builds[id] = files
		for _, file := range files {
			pending = append(pending, file.header.refs...)
		}
	}
	return builds, nil
}

func readBuild(storageDir, buildID string) ([]logicalFile, error) {
	dir := filepath.Join(storageDir, buildID)
	metaPath := filepath.Join(dir, "metadata.json")
	b, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return nil, fmt.Errorf("%s: %w: empty file", metaPath, errIncomplete)
	}
	var meta struct {
		Template struct {
			BuildID string `json:"build_id"`
		} `json:"template"`
		FilesystemOnly bool `json:"filesystem_only"`
	}
	if err := json.Unmarshal(b, &meta); err != nil {
		return nil, fmt.Errorf("%s: %w", metaPath, err)
	}
	if meta.FilesystemOnly {
		return nil, fmt.Errorf("%s: filesystem-only snapshot cannot prove memory is clean", metaPath)
	}
	if meta.Template.BuildID != "" && meta.Template.BuildID != buildID {
		return nil, fmt.Errorf("%s: metadata build ID %q does not match %s", metaPath, meta.Template.BuildID, buildID)
	}
	if err := requireFile(filepath.Join(dir, "snapfile")); err != nil {
		return nil, err
	}
	var files []logicalFile
	for _, spec := range []struct {
		data, where string
	}{
		{"memfile", "memory"},
		{"rootfs.ext4", "disk"},
	} {
		path := filepath.Join(dir, spec.data+".header")
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		h, err := parseHeader(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if h.build != buildID {
			return nil, fmt.Errorf("%s: header belongs to build %s, want %s", path, h.build, buildID)
		}
		for _, m := range h.maps {
			if m.build == nilUUID || m.length == 0 {
				continue
			}
			path := filepath.Join(storageDir, m.build, spec.data)
			f, err := os.Open(path)
			if err != nil {
				return nil, err
			}
			err = checkRange(f, m.storageOffset, m.length)
			cerr := f.Close()
			if err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			if cerr != nil {
				return nil, fmt.Errorf("%s: %w", path, cerr)
			}
		}
		files = append(files, logicalFile{data: spec.data, where: spec.where, header: h})
	}
	return files, nil
}

func requireFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s: not a regular file", path)
	}
	if info.Size() == 0 {
		return fmt.Errorf("%s: %w: empty file", path, errIncomplete)
	}
	return nil
}
