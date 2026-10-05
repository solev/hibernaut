package main

import (
	"context"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/hibapi"
)

func TestImageListAndRemove(t *testing.T) {
	h := imageHost(t)
	c := initClient(t, h)
	for _, args := range [][]string{
		{"new", "web"}, {"image", "save", "web", "myimg"}, {"new", "web2", "-image", "myimg"},
	} {
		if code := c.hib(args...); code != 0 {
			t.Fatalf("%v = %d; stderr %s", args, code, c.errOut)
		}
	}
	if code := c.hib("image", "ls"); code != 0 {
		t.Fatalf("list = %d; stderr %s", code, c.errOut)
	}
	lines := tableLines(c.out.String())
	if len(lines) != 3 || !slices.Equal(strings.Fields(lines[0]), []string{"NAME", "KIND", "MEMORY", "CREATED"}) ||
		!strings.Contains(lines[1], "built-in") || !strings.Contains(lines[2], "myimg") || !strings.Contains(lines[2], "saved") {
		t.Fatalf("image list = %q", c.out)
	}
	if code := c.hib("image", "rm", "myimg"); code != 1 || c.out.Len() != 0 ||
		c.errOut.String() != "hib: image myimg is used by machines web2: remove them first\n" {
		t.Fatalf("remove used image = %d; stdout %q; stderr %q", code, c.out, c.errOut)
	}
	if code := c.hib("rm", "web2", "--yes"); code != 0 {
		t.Fatalf("remove copy = %d; stderr %s", code, c.errOut)
	}
	if code := c.hib("image", "rm", "myimg"); code != 0 || c.out.String() != "removed image myimg\n" {
		t.Fatalf("remove image = %d; stdout %q; stderr %q", code, c.out, c.errOut)
	}
}

func TestImageSaveWaitsAndPrintsAnHonestFailure(t *testing.T) {
	h := imageHost(t)
	c := initClient(t, h)
	if code := c.hib("new", "web"); code != 0 {
		t.Fatalf("new = %d; stderr %s", code, c.errOut)
	}
	run := c.ssh
	c.ssh = func(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
		if _, ok := ctx.Deadline(); ok {
			t.Error("image save SSH must not time out before the Manager returns")
		}
		return run(ctx, args, stdin, stdout, stderr)
	}
	h.engine.FailNextSave(engine.ErrCheckpoint)
	if code := c.hib("image", "save", "web", "myimg"); code != 1 || c.out.Len() != 0 ||
		!strings.Contains(c.errOut.String(), "web is running again as it was when the save began. Nothing was saved. Try again.") {
		t.Fatalf("failed save = %d; stdout %q; stderr %q", code, c.out, c.errOut)
	}
	c.ssh = run
	if code := c.hib("image", "ls"); code != 0 || strings.Contains(c.out.String(), "myimg") {
		t.Fatalf("images after failure = %d; stdout %q; stderr %s", code, c.out, c.errOut)
	}
}

func TestImageAndForkBadArgumentsNeverReachHost(t *testing.T) {
	h := imageHost(t)
	c := initClient(t, h)
	for _, args := range [][]string{
		{"image"}, {"image", "other"}, {"image", "save"}, {"image", "save", "web"},
		{"image", "save", "web", "myimg", "extra"}, {"image", "save", "Web", "myimg"},
		{"image", "save", "web", "bad;name"}, {"image", "save", "-unknown"},
		{"image", "ls", "extra"}, {"image", "ls", "-unknown"},
		{"image", "rm"}, {"image", "rm", "myimg", "extra"}, {"image", "rm", "../myimg"},
		{"fork"}, {"fork", "web", "extra"}, {"fork", "Web"},
		{"fork", "web", "-count", "0"}, {"fork", "web", "-count", "11"},
		{"fork", "web", "-count", "many"}, {"fork", "web", "-unknown"},
	} {
		if code := c.hib(args...); code != 2 || c.errOut.Len() == 0 {
			t.Errorf("%v = %d; stderr %q", args, code, c.errOut)
		}
	}
	if calls := h.takeCalls(); len(calls) != 0 {
		t.Fatalf("bad arguments reached host: %q", calls)
	}
}

func TestImageListMarksShadowedAndUnbuiltImages(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	h.override = func(words []string, stdout, _ io.Writer) (int, bool) {
		if !slices.Equal(words, []string{"image", "ls", "-json"}) {
			return 0, false
		}
		return writeTestResponse(stdout, hibapi.Response{Images: []hibapi.Image{
			{Name: "dev", Kind: hibapi.KindBuiltIn, MemoryMB: 2048},
			{Name: "dev", Kind: hibapi.KindSaved, MemoryMB: 512, Built: true, Shadowed: true,
				Created: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)},
		}}), true
	}
	if code := c.hib("image", "ls"); code != 0 {
		t.Fatalf("list = %d; stderr %s", code, c.errOut)
	}
	lines := tableLines(c.out.String())
	if len(lines) != 3 || !strings.Contains(lines[1], "2 GiB") || !strings.HasSuffix(lines[1], "-") ||
		!strings.Contains(lines[2], "dev (shadowed)") || !strings.Contains(lines[2], "512 MiB") ||
		!strings.HasSuffix(lines[2], "2026-10-04T12:00:00Z") {
		t.Fatalf("shadowed image list = %q", c.out)
	}
}
