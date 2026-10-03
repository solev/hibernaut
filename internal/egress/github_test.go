package egress

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/cgi"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/egress/swap"
)

func TestRealGitGitHubClonePush(t *testing.T) {
	t.Parallel()
	requireBinary(t, "git")
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + home,
		"TMPDIR=" + root,
		"LANG=C",
		"LC_ALL=C",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_ALLOW_PROTOCOL=file:https",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
	}
	version := runGitHubGit(t, root, env, "--version")
	var major, minor int
	if _, err := fmt.Sscanf(version, "git version %d.%d", &major, &minor); err != nil {
		t.Fatalf("parse git version: %v", err)
	}
	if major < 2 || major == 2 && minor < 37 {
		t.Skip("git 2.37 or later is required for http.curloptResolve")
	}
	backendPath := filepath.Join(runGitHubGit(t, root, env, "--exec-path"), "git-http-backend")
	if info, err := os.Stat(backendPath); err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		t.Skip("git-http-backend is not installed or executable in git's exec path")
	}

	repos := filepath.Join(root, "repos")
	bare := filepath.Join(repos, "repo.git")
	runGitHubGit(t, root, env, "init", "--bare", "--initial-branch=main", bare)
	runGitHubGit(t, bare, env, "config", "http.receivepack", "true")
	seed := filepath.Join(root, "seed")
	runGitHubGit(t, root, env, "init", "--initial-branch=main", seed)
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("Hibernaut GitHub fixture.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitHubGit(t, seed, env, "add", "README.md")
	runGitHubGit(t, seed, env, "commit", "-m", "Initial commit")
	initial := runGitHubGit(t, seed, env, "rev-parse", "HEAD")
	runGitHubGit(t, seed, env, "push", bare, "main")

	backend := &cgi.Handler{
		Path: backendPath,
		Dir:  repos,
		Env: append(append([]string{}, env...),
			"GIT_PROJECT_ROOT="+repos,
			"GIT_HTTP_EXPORT_ALL=1",
			"REMOTE_USER=x-access-token",
		),
		Logger: log.New(io.Discard, "", 0),
		Stderr: io.Discard,
	}
	type request struct {
		method, path, host, auth string
		contentLength, bodySize  int64
	}
	var mu sync.Mutex
	var requests []request
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := &gitHubBodyCounter{ReadCloser: r.Body}
		r.Body = body
		defer func() {
			mu.Lock()
			defer mu.Unlock()
			requests = append(requests, request{
				method: r.Method, path: r.URL.Path, host: r.Host,
				auth: r.Header.Get("Authorization"), contentLength: r.ContentLength,
				bodySize: body.size,
			})
		}()
		user, password, ok := r.BasicAuth()
		if !ok || user != "x-access-token" || password != realValue {
			w.Header().Set("WWW-Authenticate", `Basic realm="GitHub"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		backend.ServeHTTP(w, r)
	})
	sw, err := swap.New([]swap.Secret{{
		Name: secretName, Placeholder: placeholder, Value: realValue,
		Sites: []string{"github.com"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	// CGI rejects chunked HTTP/1.1. HTTP/2 carries the streamed body without that encoding.
	fx := newFixtureProtocols(t, "github.com", handler, func(s *Server) {
		s.Swapper = func() *swap.Swapper { return sw }
	}, true)
	defer fx.close()
	_, port, err := net.SplitHostPort(fx.proxyAddr())
	if err != nil {
		t.Fatal(err)
	}
	base := "https://github.com:" + port + "/"
	helper := filepath.Join(home, "credential-helper")
	helperLog := filepath.Join(home, "credential-helper.log")
	const helperScript = `#!/bin/sh
set -eu
{
	printf 'operation=%s\nGH_TOKEN=%s\n' "$1" "$GH_TOKEN"
	cat
	printf '\n'
} >> "$HIB_GIT_CREDENTIAL_LOG"
if [ "$1" = get ]; then
	printf 'username=x-access-token\npassword=%s\n\n' "$GH_TOKEN"
fi
`
	if err := os.WriteFile(helper, []byte(helperScript), 0o700); err != nil {
		t.Fatal(err)
	}
	clientEnv := append(append([]string{}, env...),
		"GH_TOKEN="+placeholder,
		"HIB_GIT_CREDENTIAL_HELPER="+helper,
		"HIB_GIT_CREDENTIAL_LOG="+helperLog,
	)
	clientArgs := []string{
		"-c", "http.curloptResolve=github.com:" + port + ":127.0.0.1",
		"-c", "http.sslCAInfo=" + fx.hibCertPath,
		"-c", "http.proxy=",
		"-c", "http.version=HTTP/1.1",
		"-c", "http.postBuffer=1048576",
		"-c", "credential.helper=",
		"-c", `credential.helper=!exec "$HIB_GIT_CREDENTIAL_HELPER"`,
	}
	clientGit := func(dir string, args ...string) string {
		t.Helper()
		return runGitHubGit(t, dir, clientEnv, append(append([]string{}, clientArgs...), args...)...)
	}

	clone := filepath.Join(root, "https-clone")
	clientGit(root, "clone", base+"repo.git", clone)
	if got := clientGit(clone, "rev-parse", "HEAD"); got != initial {
		t.Fatal("HTTPS clone does not contain the initial commit")
	}
	if got := clientGit(clone, "config", "remote.origin.url"); got != base+"repo.git" {
		t.Fatal("HTTPS clone stored an unexpected remote URL")
	}
	data := make([]byte, 4<<20)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, "random.bin"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	clientGit(clone, "add", "random.bin")
	clientGit(clone, "commit", "-m", "Push an incompressible git pack")
	pushed := clientGit(clone, "rev-parse", "HEAD")
	const branch = "hibernaut-test"
	clientGit(clone, "push", "origin", "HEAD:refs/heads/"+branch)
	if got := runGitHubGit(t, bare, env, "rev-parse", "refs/heads/"+branch); got != pushed {
		t.Fatal("bare repository branch does not point at the pushed commit")
	}

	rewriteClone := filepath.Join(root, "ssh-rewrite-clone")
	clientGit(root, "-c", "url."+base+".insteadOf=git@github.com:", "clone", "git@github.com:repo.git", rewriteClone)
	if got := clientGit(rewriteClone, "rev-parse", "HEAD"); got != initial {
		t.Fatal("SSH URL rewrite clone does not contain the initial commit")
	}
	if got := clientGit(rewriteClone, "rev-parse", "refs/remotes/origin/"+branch); got != pushed {
		t.Fatal("SSH URL rewrite clone does not contain the pushed branch")
	}
	for _, dir := range []string{clone, rewriteClone} {
		assertGitHubNoRealTokenInFiles(t, filepath.Join(dir, ".git"))
	}
	input, err := os.ReadFile(helperLog)
	if err != nil {
		t.Fatal(err)
	}
	assertGitHubNoRealToken(t, "credential helper input and environment", input)
	for _, want := range []string{"operation=get\n", "GH_TOKEN=" + placeholder + "\n", "password=" + placeholder + "\n"} {
		if !bytes.Contains(input, []byte(want)) {
			t.Fatal("credential helper did not receive the expected placeholder credentials")
		}
	}

	mu.Lock()
	seen := append([]request{}, requests...)
	mu.Unlock()
	var challenges, authenticated int
	var streamedPush bool
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+realValue))
	for _, r := range seen {
		if r.host != "github.com" {
			t.Error("backend received a request for a site other than github.com")
		}
		if r.auth == "" {
			challenges++
			continue
		}
		authenticated++
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(r.auth, "Basic "))
		if err == nil && bytes.Contains(decoded, []byte(placeholder)) {
			t.Error("placeholder reached the backend Authorization header")
		}
		if r.auth != wantAuth {
			t.Error("backend Authorization was not Basic auth with the real token")
		}
		if r.method == http.MethodPost && r.path == "/repo.git/git-receive-pack" && r.contentLength == -1 && r.bodySize >= 4<<20 {
			streamedPush = true
		}
	}
	if challenges == 0 || authenticated == 0 {
		t.Errorf("backend saw %d authentication challenges and %d authenticated requests, want both", challenges, authenticated)
	}
	if !streamedPush {
		t.Error("backend did not receive an unknown-length git-receive-pack body of at least 4 MiB")
	}
}

func runGitHubGit(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	config := []string{
		"-c", "user.name=Hibernaut Test",
		"-c", "user.email=1+hibernaut-test@users.noreply.github.com",
		"-c", "commit.gpgSign=false",
		"-c", "init.templateDir=",
	}
	cmd := exec.CommandContext(ctx, "git", append(config, args...)...)
	cmd.Dir = dir
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	assertGitHubNoRealToken(t, "git stdout", stdout.Bytes())
	assertGitHubNoRealToken(t, "git stderr", stderr.Bytes())
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return strings.TrimSpace(stdout.String())
}

func assertGitHubNoRealToken(t *testing.T, location string, data []byte) {
	t.Helper()
	for _, value := range []string{
		realValue,
		base64.StdEncoding.EncodeToString([]byte(realValue)),
		base64.StdEncoding.EncodeToString([]byte("x-access-token:" + realValue)),
	} {
		if bytes.Contains(data, []byte(value)) {
			t.Fatalf("the real token appeared in %s", location)
		}
	}
}

func assertGitHubNoRealTokenInFiles(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		assertGitHubNoRealToken(t, "clone's .git files", data)
		return nil
	})
	if err != nil {
		t.Fatalf("scan clone's .git files: %v", err)
	}
}

type gitHubBodyCounter struct {
	io.ReadCloser
	size int64
}

func (r *gitHubBodyCounter) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.size += int64(n)
	return n, err
}
