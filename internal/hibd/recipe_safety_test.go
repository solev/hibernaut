package hibd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/statefile"
)

func TestRecipeLockWaitBound(t *testing.T) {
	old := recipeLockWait
	recipeLockWait = 40 * time.Millisecond
	t.Cleanup(func() { recipeLockWait = old })
	a, _, _ := newApp(t, nil)
	keepRecipe(t, a, "old", recipeBody)
	dir := a.Getenv("HIBD_STATE_DIR")
	before, err := os.ReadFile(filepath.Join(dir, "recipes.json"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	unlock, err := (statefile.Dir{Path: dir}).WaitLock(ctx, "recipes.lock", "recipes")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	cmd := *a
	var out bytes.Buffer
	cmd.Stdout, cmd.Stdin = &out, strings.NewReader(recipeBody)
	code := cmd.Run(ctx, []string{"recipe", "put", "new", "-json"})
	var reply hibapi.Response
	if err := json.Unmarshal(out.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if code != 1 || reply.Code != hibapi.CodeBusy || !reply.Retryable ||
		strings.Count(reply.Error, "recipes.lock") != 1 ||
		!strings.Contains(reply.Error, filepath.Join(dir, "recipes.lock")) ||
		!strings.Contains(reply.Error, "retry") || ctx.Err() != nil {
		t.Errorf("held lock = exit %d, %+v, context %v; want bounded retryable BUSY naming only the lock",
			code, reply, ctx.Err())
	}
	unlock()
	after, err := os.ReadFile(filepath.Join(dir, "recipes.json"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("timed-out change landed: %q, %v", after, err)
	}
	if code, out := callRecipe(t, a, "", "ls"); code != 0 ||
		!strings.Contains(out, `"name":"old"`) || strings.Contains(out, `"name":"new"`) {
		t.Fatalf("read after releasing lock = %d, %s; want original recipe only", code, out)
	}
}

func TestRecipeOwnerAndIOAdvice(t *testing.T) {
	for _, mode := range []string{"owner", "io"} {
		t.Run(mode, func(t *testing.T) {
			a, _, _ := newApp(t, nil)
			keepRecipe(t, a, "check", recipeBody)
			root, owner, err := (statefile.Dir{Path: a.Getenv("HIBD_STATE_DIR")}).Root()
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			want := "retry"
			if mode == "owner" {
				owner.UID++
				want = fmt.Sprintf("sudo chown %d:%d ", owner.UID, owner.GID)
			} else if err := root.Close(); err != nil {
				t.Fatal(err)
			}
			_, err = readRecipes(root, owner)
			if err == nil || !strings.Contains(err.Error(), want) ||
				!strings.Contains(err.Error(), filepath.Join(a.Getenv("HIBD_STATE_DIR"), "recipes.json")) ||
				strings.Count(err.Error(), "recipes.json") != 1 || strings.Contains(err.Error(), "move it aside") {
				t.Fatalf("%s failure = %v; want file-specific %q without move-aside advice", mode, err, want)
			}
		})
	}
}

func TestRecipeWrongGroupThroughRun(t *testing.T) {
	a, _, _ := newApp(t, nil)
	keepRecipe(t, a, "check", recipeBody)
	path := filepath.Join(a.Getenv("HIBD_STATE_DIR"), "recipes.json")
	root, owner, err := (statefile.Dir{Path: a.Getenv("HIBD_STATE_DIR")}).Root()
	if err != nil {
		t.Fatal(err)
	}
	root.Close()
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	group := owner.GID
	for _, gid := range groups {
		if gid != owner.GID {
			group = gid
			break
		}
	}
	if group == owner.GID {
		t.Skip("user has no second group for a rootless wrong-group fixture")
	}
	if err := os.Chown(path, -1, group); err != nil {
		t.Fatal(err)
	}
	code, out := callRecipe(t, a, "", "show", "check")
	want := fmt.Sprintf("sudo chown %d:%d %s", owner.UID, owner.GID, path)
	if code != 1 || !strings.Contains(out, want) || strings.Count(out, "recipes.json") != 1 ||
		strings.Contains(out, "move it aside") {
		t.Fatalf("wrong group = exit %d, %s; want %q without move-aside advice", code, out, want)
	}
}

func TestRecipePublishFailureAfterRename(t *testing.T) {
	a, _, _ := newApp(t, nil)
	keepRecipe(t, a, "check", recipeBody)
	publish := publishRecipe
	publishRecipe = func(s recipeStore, root *os.Root, data []byte, owner statefile.Owner) (*os.File, error) {
		f, err := publish(s, root, data, owner)
		if err != nil {
			return f, err
		}
		return nil, errors.Join(errors.New("injected post-rename sync failure"), f.Close())
	}
	t.Cleanup(func() { publishRecipe = publish })
	code, out := callRecipe(t, a, `{"command":["false"]}`, "put", "check")
	var reply hibapi.Response
	if err := json.Unmarshal([]byte(out), &reply); err != nil {
		t.Fatal(err)
	}
	if code != 1 || reply.Code != "" || !strings.Contains(reply.Error, "injected post-rename sync failure") ||
		!strings.Contains(reply.Error, "retry after checking the I/O failure") ||
		strings.Count(reply.Error, "recipes.json") != 1 {
		t.Errorf("published failure = exit %d, %s; want an uncoded I/O failure with file-specific advice", code, out)
	}
	if code, out := callRecipe(t, a, "", "show", "check"); code != 0 ||
		!strings.Contains(out, `"command":["false"]`) {
		t.Fatalf("read after failed publish = exit %d, %s; want the landed change", code, out)
	}
}

func TestRecipeErrorsNameTheirCause(t *testing.T) {
	for _, mode := range []string{"lock-mode", "lock-directory", "name", "missing", "count", "settings"} {
		t.Run(mode, func(t *testing.T) {
			a, _, _ := newApp(t, nil)
			keepRecipe(t, a, "check", recipeBody)
			args := []string{"put", "next"}
			want := ""
			switch mode {
			case "lock-mode":
				path := filepath.Join(a.Getenv("HIBD_STATE_DIR"), "recipes.lock")
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
				want = "chmod 600 " + path
			case "lock-directory":
				path := filepath.Join(a.Getenv("HIBD_STATE_DIR"), "recipes.lock")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
				want = "regular file"
			case "name":
				args, want = []string{"show", "X"}, "bad name"
			case "missing":
				for _, leaf := range []string{"show", "rm", "default"} {
					code, out := callRecipe(t, a, "", leaf, "gone")
					var r hibapi.Response
					if err := json.Unmarshal([]byte(out), &r); err != nil {
						t.Fatal(err)
					}
					if code != 1 || r.Code != hibapi.CodeBadRequest || !strings.HasSuffix(r.Error, `no recipe "gone"`) ||
						strings.Contains(r.Error, "recipes.json") || strings.Contains(r.Error, "bad task request") {
						t.Errorf("%s missing = %d, %s; want same missing-name error without file blame", leaf, code, out)
					}
				}
				return
			case "count":
				args, want = []string{"show"}, "expected one recipe name"
			case "settings":
				getenv := a.Getenv
				a.Getenv = func(key string) string {
					if key == "HIBD_LINK_PORT" {
						return "no-port"
					}
					return getenv(key)
				}
				want = "HIBD_LINK_PORT"
			}
			code, out := callRecipe(t, a, recipeBody, args...)
			if code != 1 || !strings.Contains(out, want) ||
				strings.Contains(out, "recipes.json") || strings.Contains(out, "bad task request") ||
				(strings.HasPrefix(mode, "lock-") && strings.Count(out, "recipes.lock") != 1) {
				t.Fatalf("%s = %d, %s; want %q without unrelated file or task blame", mode, code, out, want)
			}
		})
	}
}
