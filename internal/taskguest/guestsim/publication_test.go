package guestsim_test

import (
	"bytes"
	"context"
	"io"
	"path"
	"testing"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/taskguest"
)

func TestPublicationReadAndReset(t *testing.T) {
	for _, mode := range []string{"absent", "present", "unreadable"} {
		t.Run(mode, func(t *testing.T) {
			s, id, r := machine(t)
			name := path.Join(taskguest.RunDir(r.ID), "publish")
			data := []byte("changes=no\n")
			if mode != "absent" {
				file := name
				if mode == "unreadable" {
					file = path.Join(file, "child")
				}
				if err := s.Upload(context.Background(), id, "root", []engine.File{{
					Path: file, Size: int64(len(data)),
					Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil },
				}}); err != nil {
					t.Fatal(err)
				}
			}
			res := execGuest(t, s, id, taskguest.PublicationOf(r.ID))
			switch mode {
			case "absent":
				if res.ExitCode != taskguest.ExitNoPublication {
					t.Fatalf("absent publication exit=%d, want distinct absent code", res.ExitCode)
				}
			case "present":
				if res.ExitCode != 0 || !bytes.Equal(res.Stdout, data) {
					t.Fatalf("present publication read=%+v", res)
				}
			case "unreadable":
				if res.ExitCode == 0 || res.ExitCode == taskguest.ExitNoPublication || len(res.Stderr) == 0 {
					t.Fatalf("unreadable publication was treated as absent: %+v", res)
				}
			}
			if mode != "absent" {
				file := name
				if mode == "unreadable" {
					file = path.Join(file, "child")
				}
				if _, exists := s.File(id, file); !exists {
					t.Fatal("publication read reset the guest")
				}
			}
			if res := execGuest(t, s, id, []string{"sh", "-c", "rm -rf -- " + taskguest.Root}); res.ExitCode != 0 {
				t.Fatalf("reset failed: %+v", res)
			}
			file := name
			if mode == "unreadable" {
				file = path.Join(file, "child")
			}
			if _, exists := s.File(id, file); exists {
				t.Fatal("reset left publication behind")
			}
		})
	}
}
