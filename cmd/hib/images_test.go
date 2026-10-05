package main

import (
	"context"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/hibd"
	"github.com/solev/hibernaut/internal/leakscan/leakscantest"
)

const imageBuild = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"

func imageHost(t *testing.T) *host {
	t.Helper()
	h := newHost(t)
	h.env["HIBD_E2B_STORAGE"] = t.TempDir()
	leakscantest.WriteBuild(t, h.env["HIBD_E2B_STORAGE"], imageBuild, []byte("memory"), []byte("disk"))
	h.engine = fake.New(
		fake.WithTemplates(images.Template("dev")),
		fake.WithExec(func(_ context.Context, _ engine.Machine, _ string, req engine.ExecRequest) (engine.ExecResult, error) {
			if req.Cmd == images.MachineInit {
				return engine.ExecResult{Stdout: []byte(hibapi.FormatHostKey(h.machineKey) + "\n")}, nil
			}
			return engine.ExecResult{}, nil
		}),
	)
	h.override = func(words []string, stdout, stderr io.Writer) (int, bool) {
		if len(words) < 3 || !slices.Equal(words[:3], []string{"sudo", "-n", "hibd"}) {
			return 0, false
		}
		a := &hibd.App{
			Stdin: strings.NewReader(""), Stdout: stdout, Stderr: stderr,
			Getenv:    func(k string) string { return h.env[k] },
			NewEngine: func() (engine.Engine, error) { return h.engine, nil },
			ReadRoom:  h.readRoom, EUID: func() int { return 0 },
			Query: func(_ context.Context, sql string) ([][]string, error) {
				if strings.HasPrefix(sql, "SELECT status_group") {
					return [][]string{{"ready"}}, nil
				}
				return [][]string{{imageBuild}}, nil
			},
		}
		return a.Run(t.Context(), words[3:]), true
	}
	return h
}

func TestImageSaveAndNewWithSavedImage(t *testing.T) {
	h := imageHost(t)
	c := initClient(t, h)
	if code := c.hib("new", "web"); code != 0 {
		t.Fatalf("new = %d; stderr %s", code, c.errOut)
	}
	if code := c.hib("image", "save", "web", "myimg"); code != 0 {
		t.Fatalf("save = %d; stderr %s", code, c.errOut)
	}
	if c.out.String() != "saved image myimg (512 MiB). use it with: hib new NAME --image myimg\n" {
		t.Fatalf("save output = %q", c.out)
	}
	if code := c.hib("new", "web2", "-image", "myimg"); code != 0 {
		t.Fatalf("new saved image = %d; stderr %s", code, c.errOut)
	}
	if c.out.String() != "made web2; connect with: ssh web2.hib\n" {
		t.Fatalf("new output = %q", c.out)
	}
	calls := remoteCommands(h.takeCalls())
	if !slices.Contains(calls, "sudo -n hibd 'image' 'save' 'web' 'myimg' '-json'") {
		t.Fatalf("save must use root: calls %q", calls)
	}
}
