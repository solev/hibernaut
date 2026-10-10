package audit

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// jsonlFile is an append-only file of JSON lines, one T to a line, that keeps one older file at
// path.1. It is not safe for concurrent use: each log holds its own lock around it.
type jsonlFile[T any] struct {
	path string
	// maxBytes is the size the file rotates at, if above 0.
	maxBytes int64
	// name is what errors call the log.
	name string
	file *os.File
	size int64
}

// openJSONL opens path for appending and creates it with mode 0644 if needed.
func openJSONL[T any](path string, maxBytes int64, name string) (jsonlFile[T], error) {
	file, size, err := openFile(path)
	if err != nil {
		return jsonlFile[T]{}, err
	}
	return jsonlFile[T]{path: path, maxBytes: maxBytes, name: name, file: file, size: size}, nil
}

// closed reports whether f has no open file. A failed rotation leaves it closed too.
func (f *jsonlFile[T]) closed() bool {
	return f.file == nil
}

// write appends v as one line. When the line would take a file that isn't empty past maxBytes,
// it first moves the file to path.1, replacing the older file there.
func (f *jsonlFile[T]) write(v T) error {
	line, err := json.Marshal(v)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	if f.maxBytes > 0 && f.size > 0 && f.size+int64(len(line)) > f.maxBytes {
		if err := f.rotate(); err != nil {
			return err
		}
	}
	n, err := f.file.Write(line)
	f.size += int64(n)
	if err != nil {
		return err
	}
	if n != len(line) {
		return fmt.Errorf("short %s write: wrote %d of %d bytes", f.name, n, len(line))
	}
	return nil
}

func (f *jsonlFile[T]) close() error {
	err := f.file.Close()
	f.file = nil
	return err
}

func (f *jsonlFile[T]) rotate() error {
	if err := f.file.Close(); err != nil {
		return err
	}
	f.file = nil
	if err := os.Rename(f.path, f.path+".1"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, size, err := openFile(f.path)
	if err != nil {
		return err
	}
	f.file, f.size = file, size
	return nil
}

// readJSONL returns the last n values in path.1 and path, oldest first, or all of them when n
// is 0 or less. It also returns how many lines across both files were corrupt and skipped.
func readJSONL[T any](path string, n int) ([]T, int, error) {
	var out []T
	corrupt := 0
	for _, p := range []string{path + ".1", path} {
		values, c, err := readJSONLFile[T](p)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, values...)
		corrupt += c
	}
	if n > 0 && len(out) > n {
		out = out[len(out)-n:]
	}
	return out, corrupt, nil
}

func openFile(path string) (*os.File, int64, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, 0, err
	}
	if err := os.Chmod(path, 0o644); err != nil {
		file.Close()
		return nil, 0, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, 0, err
	}
	return file, info.Size(), nil
}

func readJSONLFile[T any](path string) ([]T, int, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer file.Close()
	var values []T
	corrupt := 0
	reader := bufio.NewReader(file)
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			var v T
			if len(line) > maxAuditLineBytes {
				corrupt++
			} else if err := json.Unmarshal(line, &v); err != nil {
				corrupt++
			} else {
				values = append(values, v)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, 0, readErr
		}
	}
	return values, corrupt, nil
}
