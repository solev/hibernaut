package hibd

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/statefile"
	"github.com/solev/hibernaut/internal/task"
)

const maxRecipes = 256
const maxRecipeState = 5 << 20

var recipeLockWait = 10 * time.Second

var (
	errRecipeRequest = errors.New("recipe request failed")
	errRecipeBusy    = errors.New("recipe lock busy")
	errRecipeRecheck = errors.New("saved task cannot be rechecked")
)

type recipeErrorKind int

const (
	recipeIO recipeErrorKind = iota
	recipeDamage
	recipeNewer
	recipeMode
	recipeOwner
	recipeNotRegular
	recipeBusy
)

type recipeError struct {
	path  string
	kind  recipeErrorKind
	cause error
	owner statefile.Owner
}

func (e *recipeError) Error() string {
	switch e.kind {
	case recipeMode:
		return fmt.Sprintf("recipe file needs mode 0600; run chmod 600 %s", e.path)
	case recipeOwner:
		return fmt.Sprintf("recipe file has the wrong owner; run sudo chown %d:%d %s", e.owner.UID, e.owner.GID, e.path)
	case recipeNotRegular:
		return fmt.Sprintf("%s: restore a regular file owned by uid %d and gid %d with mode 0600",
			e.path, e.owner.UID, e.owner.GID)
	case recipeDamage:
		return fmt.Sprintf("%s: damaged recipe file: %v; move it aside to recover recipes", e.path, e.cause)
	case recipeNewer:
		return fmt.Sprintf("%s: %v; update hibd, or move it aside to start over", e.path, e.cause)
	case recipeBusy:
		return fmt.Sprintf("%s: %v; retry the recipe change", e.path, e.cause)
	default:
		return fmt.Sprintf("%s: %v; retry after checking the I/O failure", e.path, e.cause)
	}
}

func (e *recipeError) Unwrap() error { return e.cause }

func recipeFileError(root *os.Root, name string, kind recipeErrorKind, err error) *recipeError {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		err = fmt.Errorf("%s: %w", pathErr.Op, pathErr.Err)
	}
	return &recipeError{path: filepath.Join(root.Name(), name), kind: kind, cause: err}
}

type missingRecipeError struct{ name string }

func (e missingRecipeError) Error() string {
	if e.name == "" {
		return "no default recipe"
	}
	return fmt.Sprintf("no recipe %q", e.name)
}

type recipeDoc struct {
	V       int                      `json:"v"`
	Recipes map[string]hibapi.Recipe `json:"recipes"`
	Default string                   `json:"default,omitempty"`
}

type recipeStore struct{ owned statefile.Dir }

func checkRecipe(r hibapi.Recipe) error {
	if err := r.Check(); err != nil {
		return err
	}
	if r.Size != "" {
		_, err := images.SizeNamed(r.Size)
		return err
	}
	return nil
}

func (s recipeStore) read() (recipeDoc, error) {
	root, owner, err := s.owned.Root()
	if errors.Is(err, fs.ErrNotExist) {
		return recipeDoc{V: 1, Recipes: map[string]hibapi.Recipe{}}, nil
	}
	if err != nil {
		return recipeDoc{}, fmt.Errorf("%w; retry after checking the state directory", err)
	}
	defer root.Close()
	return readRecipes(root, owner)
}

func recipeFile(root *os.Root, name string, owner statefile.Owner) (fs.FileInfo, error) {
	fi, err := root.Lstat(name)
	if err != nil {
		return nil, recipeFileError(root, name, recipeIO, err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	kind := recipeNotRegular
	switch {
	case !fi.Mode().IsRegular() || !ok:
	case int(st.Uid) != owner.UID || int(st.Gid) != owner.GID:
		kind = recipeOwner
	case fi.Mode().Perm() != 0o600:
		kind = recipeMode
	default:
		return fi, nil
	}
	return nil, &recipeError{path: filepath.Join(root.Name(), name), kind: kind, owner: owner}
}

func readRecipes(root *os.Root, owner statefile.Owner) (doc recipeDoc, err error) {
	defer func() {
		var classified *recipeError
		if err != nil && !errors.As(err, &classified) {
			err = recipeFileError(root, "recipes.json", recipeDamage, err)
		}
	}()
	doc = recipeDoc{V: 1, Recipes: map[string]hibapi.Recipe{}}
	named, err := recipeFile(root, "recipes.json", owner)
	if errors.Is(err, fs.ErrNotExist) {
		return doc, nil
	}
	if err != nil {
		return doc, err
	}
	f, err := root.OpenFile("recipes.json", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return doc, recipeFileError(root, "recipes.json", recipeIO, err)
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return doc, recipeFileError(root, "recipes.json", recipeIO, err)
	}
	if !os.SameFile(named, opened) {
		return doc, recipeFileError(root, "recipes.json", recipeIO, errors.New("changed while opening"))
	}
	data, err := io.ReadAll(io.LimitReader(f, maxRecipeState+1))
	if err != nil {
		return doc, recipeFileError(root, "recipes.json", recipeIO, err)
	}
	if len(data) > maxRecipeState {
		return doc, fmt.Errorf("exceeds %d bytes", maxRecipeState)
	}
	var probe struct {
		V int `json:"v"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return doc, err
	}
	if probe.V < 1 {
		return doc, fmt.Errorf("missing or invalid version %d", probe.V)
	}
	if probe.V > 1 {
		return doc, recipeFileError(root, "recipes.json", recipeNewer, fmt.Errorf("version %d is unsupported", probe.V))
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	doc = recipeDoc{}
	if err := dec.Decode(&doc); err != nil {
		return doc, err
	}
	if doc.Recipes == nil {
		return doc, errors.New("needs a recipes object")
	}
	if len(doc.Recipes) > maxRecipes {
		return doc, fmt.Errorf("exceeds %d recipes", maxRecipes)
	}
	for name, r := range doc.Recipes {
		if err := hibapi.CheckName(name); err != nil {
			return doc, err
		}
		if r.Name != name {
			return doc, fmt.Errorf("recipe key %q differs from name %q", name, r.Name)
		}
		if err := checkRecipe(r); err != nil {
			return doc, err
		}
	}
	if doc.Default != "" {
		if err := hibapi.CheckName(doc.Default); err != nil {
			return doc, err
		}
		if _, ok := doc.Recipes[doc.Default]; !ok {
			return doc, errors.New("default recipe is missing")
		}
	}
	return doc, nil
}

var errRecipePublish = errors.New("recipe publish failed")

var publishRecipe = func(s recipeStore, root *os.Root, data []byte, owner statefile.Owner) (*os.File, error) {
	return s.owned.PublishOwned(root, "recipes.json", data, owner, true)
}

func (s recipeStore) change(ctx context.Context, leaf, name string, r hibapi.Recipe) (recipeDoc, error) {
	root, owner, err := s.owned.Root()
	if err != nil {
		return recipeDoc{}, fmt.Errorf("%w; retry after checking the state directory", err)
	}
	defer root.Close()
	if _, err := recipeFile(root, "recipes.lock", owner); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return recipeDoc{}, err
	}
	wait, cancel := context.WithTimeout(ctx, recipeLockWait)
	defer cancel()
	lockFailure := func(err error) error {
		if wait.Err() != nil {
			return recipeFileError(root, "recipes.lock", recipeBusy, fmt.Errorf("%w: %w", errRecipeBusy, wait.Err()))
		}
		return recipeFileError(root, "recipes.lock", recipeIO, err)
	}
	unlock, err := s.owned.WaitLock(wait, "recipes.lock", "recipes")
	if err != nil {
		return recipeDoc{}, lockFailure(err)
	}
	defer unlock()
	if err := wait.Err(); err != nil {
		return recipeDoc{}, lockFailure(err)
	}
	dir, err := root.Open(".")
	if err != nil {
		return recipeDoc{}, err
	}
	names, err := dir.Readdirnames(-1)
	err = errors.Join(err, dir.Close())
	if err != nil {
		return recipeDoc{}, err
	}
	for _, file := range names {
		token, ok := strings.CutPrefix(file, ".recipes.json.tmp-")
		if _, err := hex.DecodeString(token); !ok || len(token) != 32 || err != nil {
			continue
		}
		if fi, err := root.Lstat(file); err == nil && (fi.Mode().IsRegular() || fi.Mode()&fs.ModeSymlink != 0) {
			_ = root.Remove(file)
		}
	}
	doc, err := readRecipes(root, owner)
	if err != nil {
		return doc, err
	}
	switch leaf {
	case "put":
		if _, exists := doc.Recipes[name]; !exists && len(doc.Recipes) >= maxRecipes {
			return doc, fmt.Errorf("at most %d recipes can be saved", maxRecipes)
		}
		doc.Recipes[name] = r
	case "rm", "default":
		if _, exists := doc.Recipes[name]; !exists {
			return doc, missingRecipeError{name: name}
		}
		if leaf == "default" {
			doc.Default = name
		} else {
			delete(doc.Recipes, name)
			if doc.Default == name {
				doc.Default = ""
			}
		}
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return doc, err
	}
	if len(data)+1 > maxRecipeState {
		return doc, fmt.Errorf("saved recipes would exceed %d bytes", maxRecipeState)
	}
	if err := wait.Err(); err != nil {
		return doc, lockFailure(err)
	}
	f, err := publishRecipe(s, root, append(data, '\n'), owner)
	if err != nil {
		return doc, fmt.Errorf("%w: %w", errRecipePublish, recipeFileError(root, "recipes.json", recipeIO, err))
	}
	if err := f.Close(); err != nil {
		return doc, fmt.Errorf("%w: %w", errRecipePublish, recipeFileError(root, "recipes.json", recipeIO, err))
	}
	return doc, nil
}

func (a *App) recipe(ctx context.Context, c taskCommand, args []string) int {
	fl := a.flags(c.Path)
	asJSON := fl.Bool("json", false, "print JSON")
	rest, ok := a.parseAny(fl, args)
	if !ok {
		return 2
	}
	leaf := strings.TrimPrefix(c.Path, "recipe ")
	fail := func(err error) int {
		if !errors.Is(err, errRecipePublish) {
			err = fmt.Errorf("%w: %w", errRecipeRequest, err)
		}
		return a.taskFail(*asJSON, c.Path, "", err)
	}
	n := 1
	if leaf == "ls" || (leaf == "default" && len(rest) == 0) {
		n = 0
	}
	if len(rest) != n {
		if n == 1 {
			return fail(errors.New("expected one recipe name"))
		}
		return fail(errors.New("expected no recipe names"))
	}
	name := ""
	if n == 1 {
		name = rest[0]
		if err := hibapi.CheckName(name); err != nil {
			return fail(err)
		}
	}
	var r hibapi.Recipe
	if leaf == "put" {
		data, err := io.ReadAll(io.LimitReader(a.Stdin, 1<<20+1))
		if err != nil {
			return fail(err)
		}
		if len(data) > 1<<20 {
			return fail(errors.New("recipe JSON exceeds 1 MiB"))
		}
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&r); err != nil {
			return fail(err)
		}
		if _, err := dec.Token(); err != io.EOF {
			return fail(errors.New("data after recipe JSON"))
		}
		if r.Name != "" && r.Name != name {
			return fail(errors.New("recipe name differs from NAME"))
		}
		r.Name = name
		if err := checkRecipe(r); err != nil {
			return fail(err)
		}
		r.Publish = task.PublishNone
	}
	cfg, err := loadSettings(a.Getenv)
	if err != nil {
		return fail(err)
	}
	s := recipeStore{owned: statefile.Dir{Path: cfg.stateDir}}
	var doc recipeDoc
	if leaf == "put" || leaf == "rm" || (leaf == "default" && name != "") {
		doc, err = s.change(ctx, leaf, name, r)
	} else {
		doc, err = s.read()
	}
	if err != nil {
		return fail(err)
	}
	reply := hibapi.RecipeReply{DefaultRecipe: doc.Default}
	switch leaf {
	case "put":
		reply.Recipe = &r
	case "show":
		found, ok := doc.Recipes[name]
		if !ok {
			return fail(missingRecipeError{name: name})
		}
		reply.Recipe = &found
	case "ls":
		reply.Recipes = []hibapi.Recipe{}
		for _, r := range doc.Recipes {
			reply.Recipes = append(reply.Recipes, r)
		}
		slices.SortFunc(reply.Recipes, func(a, b hibapi.Recipe) int { return strings.Compare(a.Name, b.Name) })
	}
	if *asJSON {
		return a.reply(hibapi.Response{RecipeReply: reply})
	}
	var out bytes.Buffer
	if reply.Recipe != nil {
		_ = hibapi.PrintRecipe(&out, *reply.Recipe)
	} else if leaf == "ls" {
		for _, r := range reply.Recipes {
			fmt.Fprintln(&out, r.Name)
		}
	} else if leaf == "default" {
		if reply.DefaultRecipe == "" {
			out.WriteString("no default recipe\n")
		} else {
			fmt.Fprintln(&out, reply.DefaultRecipe)
		}
	}
	return a.writeReply(out.Bytes(), nil, false)
}

func resolveRecipe(req hibapi.TaskStart, stateDir string) (hibapi.TaskStart, error) {
	if req.Recipe != "" {
		if err := hibapi.CheckName(req.Recipe); err != nil {
			return req, err
		}
	} else if len(req.Command) != 0 {
		return req, nil
	}
	doc, err := (recipeStore{owned: statefile.Dir{Path: stateDir}}).read()
	if err != nil {
		return req, err
	}
	if req.Recipe == "" {
		req.Recipe = doc.Default
	}
	r, ok := doc.Recipes[req.Recipe]
	if !ok {
		return req, missingRecipeError{name: req.Recipe}
	}
	if len(req.Command) == 0 {
		req.Command = r.Command
	}
	if req.Image == "" {
		req.Image = r.Image
	}
	if req.Size == "" {
		req.Size = r.Size
	}
	if req.TimeoutSeconds == 0 {
		req.TimeoutSeconds = r.TimeoutSeconds
	}
	return req, nil
}
