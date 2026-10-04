//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/termsafe"
)

func TestImageAndFork(t *testing.T) {
	ctx := testContext(t, 25*time.Minute)
	ensureImage(t)
	token, _ := connectGitHub(t, ctx)
	const origin = "e2e-web"
	const fromImage = "e2e-web2"
	image := fmt.Sprintf("e2e-image-%08x", rand.Uint32())
	t.Cleanup(func() {
		fresh, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		for _, name := range []string{origin + "-1", origin + "-2", fromImage, origin} {
			runHib(fresh, "rm", name)
		}
		if _, stderr, code := runHib(fresh, "image", "rm", image); code != 0 {
			if _, err := vm.engine.Template(fresh, "hibimg-"+image); err == nil {
				t.Errorf("cleanup: image %s remains: %s", image, termsafe.String(stderr))
			}
		}
	})
	hib(t, ctx, "new", origin)
	setUpImageFixture(t, ctx, origin, token)
	sshMachine(t, ctx, origin, "true")
	key := laptopPinKey(t, origin)
	server := imageServer(t, ctx, origin)

	hib(t, ctx, "image", "save", origin, image)
	hib(t, ctx, "new", fromImage, "-image", image)
	requireImageCopy(t, ctx, fromImage, server, key)
	if _, stderr, code := runHib(ctx, "image", "rm", image); code != 1 ||
		!strings.Contains(stderr, "remove them first") || !strings.Contains(stderr, fromImage) {
		t.Fatalf("removing an in-use image exited %d: %s", code, termsafe.String(stderr))
	}
	scan := leakScanAllowingHits(t, ctx, "-image", image)
	if scan.SecretCount == 0 || scan.BuildID == "" {
		t.Fatal("saved image leak scan must cover real secrets and a snapshot build")
	}
	for _, hit := range scan.Hits {
		t.Errorf("saved image holds secret %s (%s) in %s at %d", termsafe.String(hit.Secret),
			termsafe.String(hit.Encoding), termsafe.String(hit.Where), hit.Offset)
	}
	hib(t, ctx, "rm", fromImage)
	hib(t, ctx, "image", "rm", image)

	hib(t, ctx, "fork", origin, "-count", "2")
	first, second := origin+"-1", origin+"-2"
	requireImageCopy(t, ctx, first, server, key)
	requireImageCopy(t, ctx, second, server, key)
	if laptopPinKey(t, first) == laptopPinKey(t, second) {
		t.Error("fork copies share an SSH host key")
	}
	ls := hib(t, ctx, "ls")
	if header := strings.SplitN(ls, "\n", 2)[0]; !slices.Contains(strings.Fields(header), "FROM") {
		t.Error("hib ls has no FROM column")
	}
	for _, copy := range []string{first, second} {
		row := strings.Fields(nameRow(ls, copy))
		if len(row) == 0 || row[len(row)-1] != origin {
			t.Errorf("hib ls doesn't report %s's origin", copy)
		}
	}
	requireImageOrigin(t, ctx, origin, engine.Running)
	if imageServer(t, ctx, origin) != server {
		t.Error("the origin's dev server restarted during the fork")
	}
	hib(t, ctx, "rm", first)
	hib(t, ctx, "rm", second)

	hib(t, ctx, "pause", origin)
	hib(t, ctx, "fork", origin)
	requireImageCopy(t, ctx, first, server, key)
	requireImageOrigin(t, ctx, origin, engine.Paused)
}

const imageFixtureDir = "/home/user/image-fixture"

func setUpImageFixture(t *testing.T, ctx context.Context, name, token string) {
	t.Helper()
	id := machineID(t, ctx, name)
	githubScript(t, ctx, name, token, `git clone https://github.com/solev/hibernaut-e2e image-fixture`)
	imageExec(t, ctx, id, engine.ExecRequest{
		Cmd: "npm", Args: []string{"init", "--yes"}, Dir: imageFixtureDir, User: "user",
	})
	imageExec(t, ctx, id, engine.ExecRequest{
		Cmd: "npm", Args: []string{"install", "--save-exact", "express@5.1.0"}, Dir: imageFixtureDir, User: "user",
	})
	imageExec(t, ctx, id, engine.ExecRequest{
		Cmd: "node", Args: []string{"-e", `
const fs = require("node:fs");
fs.writeFileSync("hib-image-marker", "image fixture");
fs.writeFileSync("server.cjs", [
  'const express = require("express");',
  'const boot = require("node:crypto").randomUUID();',
  'const app = express();',
  'app.get("/", (_, res) => res.json({boot, pid: process.pid}));',
  'app.listen(3000, "127.0.0.1");',
].join("\n"));`},
		Dir: imageFixtureDir, User: "user",
	})
	imageExec(t, ctx, id, engine.ExecRequest{
		Cmd: "tmux", Args: []string{"new-session", "-d", "-s", "image-dev", "node", "server.cjs"},
		Dir: imageFixtureDir, User: "user",
	})
	waitFor(t, ctx, "the fixture's dev server", time.Minute, func() bool {
		r, err := vm.engine.Exec(ctx, id, engine.ExecRequest{
			Cmd: "curl", Args: []string{"-fsS", "--max-time", "5", "http://127.0.0.1:3000/"}, User: "user",
		})
		return err == nil && r.ExitCode == 0
	})
}

func requireImageCopy(t *testing.T, ctx context.Context, name, server, originKey string) {
	t.Helper()
	id := machineID(t, ctx, name)
	for _, path := range []string{imageFixtureDir + "/README.md", imageFixtureDir + "/node_modules/express/package.json"} {
		imageExec(t, ctx, id, engine.ExecRequest{Cmd: "test", Args: []string{"-f", path}, User: "user"})
	}
	marker := imageExec(t, ctx, id, engine.ExecRequest{
		Cmd: "cat", Args: []string{imageFixtureDir + "/hib-image-marker"}, User: "user",
	})
	if marker != "image fixture" {
		t.Error("the copy lost the fixture's files")
	}
	if imageServer(t, ctx, name) != server {
		t.Error("the copy lost the running dev server's state")
	}
	sshMachine(t, ctx, name, "true")
	copyKey := laptopPinKey(t, name)
	if copyKey == originKey {
		t.Error("the copy inherited the origin's SSH host key")
	}
	if hostKey := hostPinKey(t, ctx, id); hostKey != copyKey {
		t.Error("the host and laptop pinned different copy keys")
	}
}

func imageServer(t *testing.T, ctx context.Context, name string) string {
	t.Helper()
	return imageExec(t, ctx, machineID(t, ctx, name), engine.ExecRequest{
		Cmd: "curl", Args: []string{"-fsS", "--max-time", "10", "http://127.0.0.1:3000/"}, User: "user",
	})
}

func imageExec(t *testing.T, ctx context.Context, id string, req engine.ExecRequest) string {
	t.Helper()
	r, err := vm.engine.Exec(ctx, id, req)
	if err != nil || r.ExitCode != 0 {
		t.Fatalf("image fixture command %s failed (exit %d). Check hib status.", req.Cmd, r.ExitCode)
	}
	return strings.TrimSpace(string(r.Stdout))
}

func requireImageOrigin(t *testing.T, ctx context.Context, name string, state engine.State) {
	t.Helper()
	m, err := vm.engine.Get(ctx, machineID(t, ctx, name))
	if err != nil || m.State != state {
		t.Fatalf("origin must be %s after the fork", state)
	}
}
