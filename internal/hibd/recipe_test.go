package hibd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/statefile"
)

const recipeBody = `{"command":["sh","-c","echo 'from recipe'"],"image":"custom","size":"small","timeoutSeconds":3600,"publish":"none"}`

func callRecipe(t *testing.T, a *App, body string, args ...string) (int, string) {
	t.Helper()
	cmd := *a
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr, cmd.Stdin = &out, &stderr, strings.NewReader(body)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	code := cmd.Run(ctx, append(append([]string{"recipe"}, args...), "-json"))
	if stderr.Len() != 0 {
		t.Fatalf("recipe stderr %q", &stderr)
	}
	return code, out.String()
}

func keepRecipe(t *testing.T, a *App, name, body string) {
	t.Helper()
	if code, out := callRecipe(t, a, body, "put", name); code != 0 {
		t.Fatalf("put %s = exit %d, %s; want saved recipe", name, code, out)
	}
}

func TestRecipeCRUDAndDefault(t *testing.T) {
	a, _, _ := newApp(t, fake.New())
	keepRecipe(t, a, "check", recipeBody)
	for _, args := range [][]string{{"ls"}, {"show", "check"}, {"default", "check"}, {"default"}} {
		code, out := callRecipe(t, a, "", args...)
		if code != 0 || !strings.Contains(out, "check") ||
			(args[0] == "show" && !strings.Contains(out, "from recipe")) {
			t.Fatalf("recipe %v = exit %d, %s; want saved recipe", args, code, out)
		}
	}
	if code, out := callRecipe(t, a, "", "rm", "check"); code != 0 {
		t.Fatalf("remove = %d, %s", code, out)
	}
	for _, args := range [][]string{{"ls"}, {"default"}} {
		if code, out := callRecipe(t, a, "", args...); code != 0 || strings.Contains(out, "check") {
			t.Fatalf("after removal %v = %d, %s; want no recipe or default", args, code, out)
		}
	}
	code, r := submitTaskJSON(t, a, hibapi.TaskStart{RequestID: "no-default"}, &taskUnreadInput{})
	requireTaskError(t, code, r, "no-default", hibapi.CodeBadRequest, false)
	for _, want := range []string{"-- CMD", "--recipe NAME", "hib recipe default NAME"} {
		if !strings.Contains(r.Error, want) {
			t.Errorf("no default = %q; want %q", r.Error, want)
		}
	}
}

func TestRecipeResolutionAndSnapshot(t *testing.T) {
	for _, field := range []string{"none", "command", "image", "size", "timeout", "default"} {
		t.Run(field, func(t *testing.T) {
			a, _, _ := newApp(t, fake.New())
			keepRecipe(t, a, "check", recipeBody)
			req := hibapi.TaskStart{RequestID: "resolve", Recipe: "check"}
			command, image, size, seconds := []string{"sh", "-c", "echo 'from recipe'"}, "custom", "small", int64(3600)
			switch field {
			case "command":
				req.Command, command = []string{"true"}, []string{"true"}
			case "image":
				req.Image, image = "other", "other"
			case "size":
				req.Size, size = "large", "large"
			case "timeout":
				req.TimeoutSeconds, seconds = 86400, 86400
			case "default":
				if code, out := callRecipe(t, a, "", "default", "check"); code != 0 {
					t.Fatalf("default = %d, %s", code, out)
				}
				req.Recipe = ""
			}
			code, r := submitTaskJSON(t, a, req, strings.NewReader(""))
			got := requireTaskReply(t, code, r, req.RequestID, 1)
			saved, err := savedTasks(t, a).Task(t.Context(), got.ID)
			if err != nil {
				t.Fatal(err)
			}
			run := saved.Runs[0].Spec
			if !reflect.DeepEqual(run.Command, command) || run.Image != image || run.Size != size ||
				run.Timeout != time.Duration(seconds)*time.Second || run.Recipe != "check" {
				t.Fatalf("resolved run %+v; want %q %s %s %ds recipe check", run, command, image, size, seconds)
			}
			keepRecipe(t, a, "check", `{"command":["false"]}`)
			callRecipe(t, a, "", "rm", "check")
			cmd := *a
			var out bytes.Buffer
			cmd.Stdout = &out
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if code := cmd.Run(ctx, []string{"task", "show", string(got.ID)}); code != 0 ||
				!strings.Contains(out.String(), "recipe check") || !strings.Contains(out.String(), command[0]) {
				t.Fatalf("snapshot = exit %d, %s; want old command and recipe check", code, &out)
			}
		})
	}
}

func TestRecipeRetryAfterEdit(t *testing.T) {
	for _, mode := range []string{"recipe", "default", "overrides", "rm", "default-rm", "damage"} {
		t.Run(mode, func(t *testing.T) {
			a, out, _ := newApp(t, fake.New())
			keepRecipe(t, a, "check", recipeBody)
			req := hibapi.TaskStart{RequestID: "recipe-retry", Recipe: "check"}
			if mode == "default" || mode == "default-rm" {
				callRecipe(t, a, "", "default", "check")
				req.Recipe = ""
			} else if mode == "overrides" {
				req.Command, req.Image, req.Size, req.TimeoutSeconds = []string{"true"}, "dev", "medium", 86400
			}
			code, r := submitTaskJSON(t, a, req, strings.NewReader(""))
			first := requireTaskReply(t, code, r, req.RequestID, 1)
			switch mode {
			case "default":
				keepRecipe(t, a, "next", recipeBody)
				callRecipe(t, a, "", "default", "next")
			case "rm", "default-rm":
				if code, out := callRecipe(t, a, "", "rm", "check"); code != 0 {
					t.Fatalf("remove before retry = %d, %s", code, out)
				}
			case "damage":
				if err := os.WriteFile(filepath.Join(a.Getenv("HIBD_STATE_DIR"), "recipes.json"), []byte("{"), 0o600); err != nil {
					t.Fatal(err)
				}
			default:
				keepRecipe(t, a, "check", `{"command":["false"]}`)
			}
			code, r = submitTaskJSON(t, a, req, strings.NewReader(""))
			if mode == "overrides" {
				if got := requireTaskReply(t, code, r, req.RequestID, 1); got.ID != first.ID {
					t.Fatalf("overridden edit returned %s; want %s", got.ID, first.ID)
				}
				req.Image = "changed"
				code, r = submitTaskJSON(t, a, req, strings.NewReader(""))
			}
			requireTaskError(t, code, r, req.RequestID, hibapi.CodeConflict, false)
			code, r = taskReadReply(t, a, out, "task", "show", "-request", req.RequestID)
			if code != 0 || r.Task == nil || r.Task.ID != first.ID ||
				len(r.Task.Runs) != 1 || r.Task.Runs[0].Recipe != "check" {
				t.Fatalf("request lookup = %d, %+v; want original recipe task %s", code, r, first.ID)
			}
			all, err := savedTasks(t, a).List(t.Context())
			if err != nil || len(all) != 1 || all[0].ID != first.ID || len(all[0].Runs) != 1 {
				t.Fatalf("retry saved %+v, %v; want first task only", all, err)
			}
			if mode == "rm" || mode == "default-rm" || mode == "damage" {
				for _, id := range []string{"new-request", ""} {
					req.RequestID = id
					code, r = submitTaskJSON(t, a, req, &taskUnreadInput{})
					requireTaskError(t, code, r, id, hibapi.CodeBadRequest, false)
				}
			}
		})
	}
}

func TestRecipeNamesAndLimits(t *testing.T) {
	a, _, _ := newApp(t, nil)
	keepRecipe(t, a, "check", recipeBody)
	path := filepath.Join(a.Getenv("HIBD_STATE_DIR"), "recipes.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../x", "a/b", ".", "X", strings.Repeat("a", 64)} {
		for _, leaf := range []string{"put", "show", "rm", "default"} {
			code, out := callRecipe(t, a, recipeBody, leaf, name)
			if code != 1 || !strings.Contains(out, `"errorCode":"BAD_REQUEST"`) {
				t.Errorf("%s %q = %d, %s; want BAD_REQUEST", leaf, name, code, out)
			}
		}
		data := &taskUnreadInput{}
		code, r := submitTaskJSON(t, a, hibapi.TaskStart{RequestID: "bad-name", Recipe: name}, data)
		requireTaskError(t, code, r, "bad-name", hibapi.CodeBadRequest, false)
		if data.reads != 0 {
			t.Fatal("invalid recipe read task input")
		}
	}
	for _, body := range []string{
		`{"command":[]}`, `{"command":[""]}`, `{"command":["a\u0000b"]}`,
		`{"command":["true"],"publish":"draft-pr"}`, `{"command":["true"],"timeoutSeconds":-1}`,
		`{"command":["true"],"timeoutSeconds":9223372037}`,
		`{"command":["true"],"timeoutSeconds":0.5}`,
		`{"command":["true"],"image":"../x"}`, `{"command":["true"],"size":"huge"}`,
		`{"command":["` + strings.Repeat("a", 16385) + `"]}`, `{"command":["true"],"unknown":1}`,
	} {
		if code, out := callRecipe(t, a, body, "put", "check"); code != 1 ||
			!strings.Contains(out, `"errorCode":"BAD_REQUEST"`) {
			t.Fatalf("invalid put = %d, %s; want BAD_REQUEST", code, out)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("invalid request changed recipes: %v", err)
	}
}

func TestRecipeDamagedStoreDoesNotBlockExplicitCommand(t *testing.T) {
	for _, damage := range []string{"json", "version", "missing-version", "missing-recipes", "null-recipes", "oversized", "symlink", "directory", "mode"} {
		t.Run(damage, func(t *testing.T) {
			a, _, _ := newApp(t, nil)
			keepRecipe(t, a, "check", recipeBody)
			dir := a.Getenv("HIBD_STATE_DIR")
			for _, name := range []string{"recipes.json", "recipes.lock"} {
				fi, err := os.Lstat(filepath.Join(dir, name))
				owner, ownerErr := os.Stat(dir)
				if err != nil || ownerErr != nil || fi.Mode().Perm() != 0o600 ||
					fi.Sys().(*syscall.Stat_t).Uid != owner.Sys().(*syscall.Stat_t).Uid ||
					fi.Sys().(*syscall.Stat_t).Gid != owner.Sys().(*syscall.Stat_t).Gid {
					t.Fatalf("%s mode/owner = %v, %v, %v", name, fi, err, ownerErr)
				}
			}
			path := filepath.Join(dir, "recipes.json")
			var err error
			switch damage {
			case "json":
				err = os.WriteFile(path, []byte("{"), 0o600)
			case "version":
				err = os.WriteFile(path, []byte(`{"v":2,"recipes":{}}`), 0o600)
			case "missing-version":
				err = os.WriteFile(path, []byte(`{"recipes":{}}`), 0o600)
			case "missing-recipes":
				err = os.WriteFile(path, []byte(`{"v":1}`), 0o600)
			case "null-recipes":
				err = os.WriteFile(path, []byte(`{"v":1,"recipes":null}`), 0o600)
			case "oversized":
				err = os.Truncate(path, 5<<20+1)
			case "mode":
				err = os.Chmod(path, 0o644)
			default:
				if err = os.Remove(path); err == nil {
					if damage == "symlink" {
						err = os.Symlink(filepath.Join(dir, "recipes.lock"), path)
					} else {
						err = os.Mkdir(path, 0o700)
					}
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{{"ls"}, {"show", "check"}, {"put", "new"}, {"rm", "check"}, {"default"}} {
				if code, out := callRecipe(t, a, recipeBody, args...); code != 1 || !strings.Contains(out, "recipes.json") {
					t.Fatalf("damaged %v = %d, %s; want file-specific failure", args, code, out)
				}
			}
			code, r := submitTaskJSON(t, a, hibapi.TaskStart{RequestID: "damaged"}, &taskUnreadInput{})
			requireTaskError(t, code, r, "damaged", hibapi.CodeBadRequest, false)
			code, r = submitTaskJSON(t, a, hibapi.TaskStart{RequestID: "explicit", Command: []string{"true"}}, strings.NewReader(""))
			requireTaskReply(t, code, r, "explicit", 1)
		})
	}
}

func TestRecipeConcurrentWritersAndCap(t *testing.T) {
	a, _, _ := newApp(t, nil)
	keepRecipe(t, a, "seed", recipeBody)
	dir := a.Getenv("HIBD_STATE_DIR")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	unlock, err := (statefile.Dir{Path: dir}).WaitLock(ctx, "recipes.lock", "recipes")
	if err != nil {
		t.Fatal(err)
	}

	defer unlock()
	results := make(chan string, 2)
	for _, name := range []string{"first", "second"} {
		go func() {
			code, out := callRecipe(t, a, recipeBody, "put", name)
			results <- fmt.Sprintf("%d %s", code, out)
		}()
	}
	select {
	case out := <-results:
		t.Fatalf("put bypassed held recipe lock: %s", out)
	case <-time.After(100 * time.Millisecond):
	}
	unlock()
	for range 2 {
		select {
		case out := <-results:
			if !strings.HasPrefix(out, "0 ") {
				t.Fatal(out)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	_, out := callRecipe(t, a, "", "ls")
	if !strings.Contains(out, "first") || !strings.Contains(out, "second") {
		t.Fatalf("concurrent puts lost a recipe: %s", out)
	}
	for i := 3; i < 255; i++ {
		keepRecipe(t, a, fmt.Sprintf("r%d", i), `{"command":["true"]}`)
	}
	results = make(chan string, 2)
	for _, name := range []string{"last-a", "last-b"} {
		go func() {
			code, out := callRecipe(t, a, recipeBody, "put", name)
			results <- fmt.Sprintf("%d %s", code, out)
		}()
	}
	success := 0
	for range 2 {
		select {
		case out := <-results:
			if strings.HasPrefix(out, "0 ") {
				success++
			} else if !strings.Contains(out, "256") {
				t.Fatal(out)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if success != 1 {
		t.Fatalf("writers at 255: %d succeeded; want one", success)
	}
	path := filepath.Join(dir, "recipes.json")
	before, _ := os.ReadFile(path)
	if code, out := callRecipe(t, a, recipeBody, "put", "overflow"); code != 1 {
		t.Fatalf("257th recipe = %d, %s; want refusal", code, out)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("257th recipe changed the store")
	}
}

func TestRecipeCrashBeforeRename(t *testing.T) {
	if dir := os.Getenv("HIB_RECIPE_CRASH_DIR"); dir != "" {
		s := recipeStore{owned: statefile.Dir{Path: dir, Chown: func(f *os.File, _, _ int) error {
			if strings.Contains(f.Name(), ".recipes.json.tmp-") {
				if _, err := f.Write([]byte(`{"v":1,"recipes":`)); err != nil {
					return err
				}
				p, err := os.FindProcess(os.Getpid())
				if err != nil {
					return err
				}
				return p.Kill()
			}
			return nil
		}}}
		_, err := s.change(t.Context(), "put", "new", hibapi.Recipe{Name: "new", Command: []string{"false"}})
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	a, _, _ := newApp(t, nil)
	keepRecipe(t, a, "old", recipeBody)
	dir := a.Getenv("HIBD_STATE_DIR")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRecipeCrashBeforeRename$")
	cmd.Env = append(os.Environ(), "HIB_RECIPE_CRASH_DIR="+dir)
	if err := cmd.Run(); err == nil || ctx.Err() != nil {
		t.Fatalf("crashed writer = %v, context %v; want killed process", err, ctx.Err())
	}
	stale, _ := filepath.Glob(filepath.Join(dir, ".recipes.json.tmp-*"))
	if len(stale) != 1 {
		t.Fatalf("crash left %v; want one unpublished temporary file", stale)
	}
	if data, err := os.ReadFile(stale[0]); err != nil || len(data) == 0 {
		t.Fatalf("crashed write left %q, %v; want unpublished bytes", data, err)
	}
	if code, out := callRecipe(t, a, "", "ls"); code != 0 || !strings.Contains(out, "old") || strings.Contains(out, `"name":"new"`) {
		t.Fatalf("crash changed published recipes = %d, %s", code, out)
	}
	keepRecipe(t, a, "next", recipeBody)
	stale, _ = filepath.Glob(filepath.Join(dir, ".recipes.json.tmp-*"))
	if len(stale) != 0 {
		t.Fatalf("next put left stale temporary files %v", stale)
	}
}
