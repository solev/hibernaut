package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/hibapi"
)

func completions(t *testing.T, c *client, words ...string) []string {
	t.Helper()
	code := c.hib(append([]string{"__complete"}, words...)...)
	if code != 0 || c.errOut.Len() != 0 {
		t.Fatalf("hib __complete %q = %d, stderr %q", words, code, c.errOut)
	}
	if c.out.Len() == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(c.out.String(), "\n"), "\n")
}

func noHost(t *testing.T, c *client) {
	t.Helper()
	c.ssh = func(_ context.Context, args []string, _ io.Reader, _, _ io.Writer) (int, error) {
		t.Errorf("completing ran ssh %q", args)
		return 255, nil
	}
	c.az = func(_ context.Context, args ...string) ([]byte, error) {
		t.Errorf("completing ran az %q", args)
		return nil, nil
	}
	c.execSSH = func(args []string) error {
		t.Errorf("completing ran ssh %q", args)
		return nil
	}
}

func writeNames(t *testing.T, dir string, names any) {
	t.Helper()
	data, err := json.Marshal(names)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "names.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCompleteCommandsFlagsAndSizes(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	noHost(t, c)
	writeNames(t, c.dir, map[string][]string{"machines": {"app", "api"}, "images": {"myimg"}})
	commands := commandTable().names()
	for _, tc := range []struct {
		words, want []string
	}{
		{nil, commands},
		{[]string{""}, commands},
		{[]string{"s"}, []string{"ssh", "status", "secret"}},
		{[]string{"im"}, []string{"image"}},
		{[]string{"__"}, nil},
		{[]string{"nosuch", ""}, nil},
		{[]string{"image", ""}, []string{"build", "save", "ls", "rm"}},
		{[]string{"image", "b"}, []string{"build"}},
		{[]string{"completion", ""}, []string{"bash", "zsh", "fish"}},
		{[]string{"connect", "github", "--"}, []string{"--from-gh", "--token"}},
		{[]string{"new", "app", "--"}, []string{"--idle", "--image", "--json", "--size"}},
		{[]string{"new", "app", "--s"}, []string{"--size"}},
		{[]string{"new", "app", "-s"}, []string{"-size"}},
		{[]string{"new", "app", "-"}, []string{"--idle", "--image", "--json", "--size"}},
		{[]string{"new", "app", "--size", ""}, images.SizeNames()},
		{[]string{"new", "app", "--size", "l"}, []string{"large"}},
		{[]string{"new", "app", "-size", "m"}, []string{"medium"}},
		{[]string{"new", "--size", "small", ""}, nil},
		{[]string{"new", "app", "--size=m"}, []string{"--size=medium"}},
		{[]string{"new", "app", "--size", "=", ""}, images.SizeNames()},
		{[]string{"new", "app", "--size", "=", "s"}, []string{"small"}},
		{[]string{"new", "app", "--size", "="}, images.SizeNames()},
		{[]string{"new", "app", "--image", ""}, []string{"dev", "myimg"}},
		{[]string{"new", "app", "--idle", ""}, nil},
		{[]string{"new", ""}, nil},
		{[]string{"rm", "--"}, []string{"--yes"}},
		{[]string{"rm", "--yes", ""}, []string{"api", "app"}},
		{[]string{"rm", "app", ""}, nil},
		{[]string{"ls", "--json", ""}, nil},
		{[]string{"ssh", "ap"}, []string{"api", "app"}},
		{[]string{"ssh", "app", "--", ""}, nil},
		{[]string{"exec", "app", "--", "--"}, nil},
		{[]string{"fork", "app", "--count", ""}, nil},
		{[]string{"image", "save", ""}, []string{"api", "app"}},
		{[]string{"image", "save", "app", ""}, nil},
		{[]string{"image", "rm", ""}, []string{"myimg"}},
		{[]string{"image", "build", ""}, images.Names()},
		{[]string{"secret", "rm", ""}, nil},
		{[]string{"port", ""}, []string{"add", "rm", "ls"}},
		{[]string{"port", "add", ""}, []string{"api", "app"}},
		{[]string{"port", "rm", "ap"}, []string{"api", "app"}},
		{[]string{"port", "add", "app", ""}, nil},
		{[]string{"port", "rm", "app", "8080", "--"}, []string{"--json"}},
		{[]string{"port", "ls", ""}, nil},
		{[]string{"help", ""}, commands},
		{[]string{"help", "image", "s"}, []string{"save"}},
		{[]string{"help", "ls", ""}, nil},
		{[]string{"--c"}, []string{"--config"}},
		{[]string{"--config", c.dir, "rm", "app", "--"}, []string{"--yes"}},
	} {
		if got := completions(t, c, tc.words...); !slices.Equal(got, tc.want) {
			t.Errorf("completing %q = %q, want %q", tc.words, got, tc.want)
		}
	}
	if calls := h.takeCalls(); len(calls) != 0 {
		t.Errorf("completing reached the host: %q", calls)
	}
}

func TestCompleteHonoursConfigOnTheLine(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	noHost(t, c)
	writeNames(t, c.dir, map[string][]string{"machines": {"mine"}})
	other := filepath.Join(t.TempDir(), "other")
	writeNames(t, other, map[string][]string{"machines": {"theirs"}})
	home := c.getenv("HOME")
	writeNames(t, filepath.Join(home, "hib2"), map[string][]string{"machines": {"homed"}})
	for _, tc := range []struct {
		words, want []string
	}{
		{[]string{"rm", ""}, []string{"mine"}},
		{[]string{"--config", other, "rm", ""}, []string{"theirs"}},
		{[]string{"-config", other, "rm", ""}, []string{"theirs"}},
		{[]string{"--config=" + other, "rm", ""}, []string{"theirs"}},
		{[]string{"--config", "=", other, "rm", ""}, []string{"theirs"}},
		{[]string{"--config", "~/hib2", "rm", ""}, []string{"homed"}},
		{[]string{"--config", filepath.Join(other, "missing"), "rm", ""}, nil},
	} {
		if got := completions(t, c, tc.words...); !slices.Equal(got, tc.want) {
			t.Errorf("completing %q = %q, want %q", tc.words, got, tc.want)
		}
	}
}

func TestCompleteNeverPrintsBadCachedNames(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	noHost(t, c)
	many := make([]string, 0, 1100)
	for i := range 1100 {
		many = append(many, fmt.Sprintf("m%04d", i))
	}
	bad := []string{"Bad", "a;b", "$(touch pwned)", "\x1b[2J", "a\nb", "", "-rf", strings.Repeat("a", 64)}
	writeNames(t, c.dir, map[string][]string{"machines": append(slices.Clone(bad), "good"), "images": append(bad, many...)})
	if got := completions(t, c, "rm", ""); !slices.Equal(got, []string{"good"}) {
		t.Errorf("machines from a cache with bad names = %q", got)
	}
	got := completions(t, c, "image", "rm", "")
	if len(got) != 1000 || slices.ContainsFunc(got, func(s string) bool { return hibapi.CheckName(s) != nil }) {
		t.Errorf("completing images from a cache of 1,100 gave %d names: %q", len(got), got[:min(len(got), 5)])
	}

	for _, content := range []string{"not json", `{"machines":"app"}`, `[]`, `{"machines":[1,2]}`} {
		if err := os.WriteFile(filepath.Join(c.dir, "names.json"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := completions(t, c, "rm", ""); len(got) != 0 {
			t.Errorf("cache %q gave %q", content, got)
		}
		if got := completions(t, c, "r"); !slices.Equal(got, []string{"rm", "recipe"}) {
			t.Errorf("cache %q broke command completion: %q", content, got)
		}
	}
}

func cachedNames(t *testing.T, dir string) (machines, images []string) {
	t.Helper()
	path := filepath.Join(dir, "names.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("names.json has mode %v, want 0600", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var names struct{ Machines, Images []string }
	if err := json.Unmarshal(data, &names); err != nil {
		t.Fatalf("names.json %q: %v", data, err)
	}
	return names.Machines, names.Images
}

func TestLsAndImageLsFillTheCache(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	for _, name := range []string{"app", "api"} {
		if code := c.hib("new", name); code != 0 {
			t.Fatalf("new %s exit %d: %s", name, code, c.errOut)
		}
	}
	if _, err := os.Stat(filepath.Join(c.dir, "names.json")); !os.IsNotExist(err) {
		t.Errorf("names.json exists before ls: %v", err)
	}
	if code := c.hib("ls"); code != 0 {
		t.Fatalf("ls exit %d: %s", code, c.errOut)
	}
	if machines, imgs := cachedNames(t, c.dir); !slices.Equal(machines, []string{"api", "app"}) || len(imgs) != 0 {
		t.Errorf("after ls the cache has machines %q and images %q", machines, imgs)
	}
	if code := c.hib("image", "ls", "--json"); code != 0 {
		t.Fatalf("image ls exit %d: %s", code, c.errOut)
	}
	if machines, imgs := cachedNames(t, c.dir); !slices.Equal(machines, []string{"api", "app"}) || !slices.Contains(imgs, images.Default) {
		t.Errorf("after image ls the cache has machines %q and images %q", machines, imgs)
	}

	if code := c.hib("rm", "api", "--yes"); code != 0 {
		t.Fatalf("rm exit %d: %s", code, c.errOut)
	}
	if code := c.hib("ls", "--json"); code != 0 {
		t.Fatalf("ls --json exit %d: %s", code, c.errOut)
	}
	if machines, _ := cachedNames(t, c.dir); !slices.Equal(machines, []string{"app"}) {
		t.Errorf("ls --json didn't replace the machines: %q", machines)
	}

	h.override = func(_ []string, stdout, _ io.Writer) (int, bool) {
		return writeTestResponse(stdout, hibapi.Response{Error: "E2B is down"}), true
	}
	if code := c.hib("ls"); code != 1 {
		t.Errorf("failing ls exit %d", code)
	}
	if machines, _ := cachedNames(t, c.dir); !slices.Equal(machines, []string{"app"}) {
		t.Errorf("a failing ls changed the cache: %q", machines)
	}
}

func TestTheCacheKeepsOnlyGoodNames(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	var listed []hibapi.Machine
	for _, name := range []string{"Bad", "a;b", "$(touch pwned)", "\x1b[2J", ""} {
		listed = append(listed, hibapi.Machine{Name: name, ID: "x", State: "running"})
	}
	for i := range 1100 {
		listed = append(listed, hibapi.Machine{Name: fmt.Sprintf("m%04d", i), ID: "x", State: "running"})
	}
	h.override = func(_ []string, stdout, _ io.Writer) (int, bool) {
		return writeTestResponse(stdout, hibapi.Response{Machines: listed}), true
	}
	if code := c.hib("ls"); code != 0 {
		t.Fatalf("ls exit %d: %s", code, c.errOut)
	}
	machines, _ := cachedNames(t, c.dir)
	if len(machines) != 1000 || slices.ContainsFunc(machines, func(s string) bool { return hibapi.CheckName(s) != nil }) {
		t.Errorf("the cache has %d machines, starting %q", len(machines), machines[:min(len(machines), 5)])
	}
}

func TestFailingToWriteTheCacheIsSilent(t *testing.T) {
	h := newHost(t)
	c := initClient(t, h)
	blocker := filepath.Join(c.dir, "names.json")
	if err := os.MkdirAll(filepath.Join(blocker, "full"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"ls"}, {"ls", "--json"}, {"image", "ls"}} {
		if code := c.hib(args...); code != 0 || c.errOut.Len() != 0 {
			t.Errorf("hib %q with an unwritable cache: exit %d, stderr %q", args, code, c.errOut)
		}
	}
	noHost(t, c)
	if got := completions(t, c, "rm", ""); len(got) != 0 {
		t.Errorf("an unreadable cache gave %q", got)
	}
}

func TestCompletionScripts(t *testing.T) {
	c := newClient(t, newHost(t))
	for _, tc := range []struct{ shell, check string }{
		{"bash", "-n"}, {"zsh", "-n"}, {"fish", "--no-execute"},
	} {
		t.Run(tc.shell, func(t *testing.T) {
			if code := c.hib("completion", tc.shell); code != 0 || !strings.Contains(c.out.String(), "hib __complete") {
				t.Fatalf("completion %s = %d, %q", tc.shell, code, c.out)
			}
			script := filepath.Join(t.TempDir(), "hib."+tc.shell)
			if err := os.WriteFile(script, c.out.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := exec.LookPath(tc.shell); err != nil {
				t.Skipf("no %s here", tc.shell)
			}
			if out, err := exec.Command(tc.shell, tc.check, script).CombinedOutput(); err != nil {
				t.Errorf("%s %s: %v: %s", tc.shell, tc.check, err, out)
			}
		})
	}
}

var hostileCandidates = []string{"$(touch pwned)", "`touch pwned`", "*", "two words", "--size=small", "a'b\"c"}

func fakeHib(t *testing.T) (dir, argsFile string) {
	t.Helper()
	dir = t.TempDir()
	argsFile = filepath.Join(dir, "args")
	var quoted []string
	for _, s := range hostileCandidates {
		quoted = append(quoted, "'"+strings.ReplaceAll(s, "'", `'\''`)+"'")
	}
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$HIB_ARGS\"\nprintf '%s\\n' " + strings.Join(quoted, " ") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "hib"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return dir, argsFile
}

func runCompletion(t *testing.T, shell, driver string) {
	t.Helper()
	if _, err := exec.LookPath(shell); err != nil {
		t.Skipf("no %s here", shell)
	}
	c := newClient(t, newHost(t))
	if code := c.hib("completion", shell); code != 0 {
		t.Fatalf("completion %s exit %d", shell, code)
	}
	bin, argsFile := fakeHib(t)
	work := t.TempDir()
	script := filepath.Join(bin, "hib."+shell)
	if err := os.WriteFile(script, c.out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(shell, "-c", driver, "driver", script)
	cmd.Dir = work
	cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "HIB_ARGS=" + argsFile, "HOME=" + work}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v: %s", shell, err, out)
	}
	if got := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n"); !slices.Equal(got, hostileCandidates) {
		t.Errorf("%s completed %q, want %q", shell, got, hostileCandidates)
	}
	if args, err := os.ReadFile(argsFile); err != nil || string(args) != "__complete\nrm\n\n" {
		t.Errorf("%s ran hib with %q (%v), want __complete rm ''", shell, args, err)
	}
	if entries, _ := os.ReadDir(work); len(entries) != 1 {
		t.Errorf("%s completing changed the directory: %v", shell, entries)
	}
}

func TestBashCompletionNeverEvaluatesCandidates(t *testing.T) {
	runCompletion(t, "bash", `source "$1"
COMP_WORDS=(hib rm "")
COMP_CWORD=2
_hib
printf '%s\n' "${COMPREPLY[@]}"`)
}

func TestZshCompletionNeverEvaluatesCandidates(t *testing.T) {
	runCompletion(t, "zsh", `compdef() { :; }
compadd() {
	while [[ $1 == -* && $1 != -- ]]; do shift; done
	[[ $1 == -- ]] && shift
	printf '%s\n' "$@"
}
source "$1"
words=(hib rm "")
CURRENT=3
_hib`)
}
