package machines_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/envblock"
	"github.com/solev/hibernaut/internal/machines"
	"github.com/solev/hibernaut/internal/vault"
)

type envFileGuest struct {
	content           []byte
	casMismatchesLeft int
	requests          []engine.ExecRequest
	// git runs the script that sets git's identity. Nil answers as if sh were missing.
	git func(ctx context.Context, req engine.ExecRequest) (engine.ExecResult, error)
	// codex runs the script that writes Codex's login file. Nil answers as if sh were missing.
	codex func(ctx context.Context, req engine.ExecRequest) (engine.ExecResult, error)
}

func (g *envFileGuest) exec(ctx context.Context, _ engine.Machine, _ string, req engine.ExecRequest) (engine.ExecResult, error) {
	g.requests = append(g.requests, req)
	if req.User != "root" {
		return engine.ExecResult{ExitCode: 127, Stderr: []byte("not root")}, nil
	}
	switch req.Cmd {
	case "cat":
		if len(req.Args) == 1 && req.Args[0] == "/etc/environment" {
			return engine.ExecResult{Stdout: append([]byte(nil), g.content...)}, nil
		}
	case "sh":
		if len(req.Args) == 5 && req.Args[0] == "-c" && req.Args[2] == "hibd-git" && g.git != nil {
			return g.git(ctx, req)
		}
		if len(req.Args) == 5 && req.Args[0] == "-c" && req.Args[2] == "hibd-codex" && g.codex != nil {
			return g.codex(ctx, req)
		}
		if len(req.Args) == 5 && req.Args[0] == "-c" && req.Args[2] == "hibd-env" {
			if g.casMismatchesLeft > 0 {
				g.casMismatchesLeft--
				g.content = append(g.content, []byte("# concurrent edit\n")...)
				return engine.ExecResult{ExitCode: 75, Stderr: []byte("changed")}, nil
			}
			got := fmt.Sprintf("%x", sha256.Sum256(g.content))
			if got != req.Args[3] {
				return engine.ExecResult{ExitCode: 75, Stderr: []byte("changed")}, nil
			}
			next, err := base64.StdEncoding.DecodeString(req.Args[4])
			if err != nil {
				return engine.ExecResult{ExitCode: 64, Stderr: []byte(err.Error())}, nil
			}
			g.content = next
			return engine.ExecResult{}, nil
		}
	}
	return engine.ExecResult{ExitCode: 127, Stderr: []byte(fmt.Sprintf("unexpected %+v", req))}, nil
}

func TestWriteBlockRetriesCASAndReplacesStaleSecretValue(t *testing.T) {
	const value = "real-secret-value"
	guest := &envFileGuest{
		content: []byte("PATH=/usr/bin\n# BEGIN hibernaut secrets: hibd manages this block and replaces edits\n" +
			"GH_TOKEN=" + value + "\n# END hibernaut secrets\n"),
		casMismatchesLeft: 1,
	}
	e := fake.New(fake.WithExec(guest.exec))
	e.Add(engine.Machine{ID: "app1", State: engine.Running, StartedAt: time.Now()})
	block, err := envblock.Render([]envblock.Var{{Name: "GH_TOKEN", Value: "gho_hibplaceholder1234567890"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := machines.WriteBlock(t.Context(), e, "app1", block); err != nil {
		t.Fatal(err)
	}
	const want = "PATH=/usr/bin\n# BEGIN hibernaut secrets: hibd manages this block and replaces edits\n" +
		"GH_TOKEN=gho_hibplaceholder1234567890\n# END hibernaut secrets\n# concurrent edit\n"
	if got := string(guest.content); got != want {
		t.Fatalf("/etc/environment = %q, want %q", got, want)
	}
	for _, req := range guest.requests {
		if strings.Contains(strings.Join(req.Args, " "), value) {
			t.Fatalf("real value reached argv: %+v", req)
		}
		if req.Cmd == "sh" {
			payload, err := base64.StdEncoding.DecodeString(req.Args[4])
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(payload, []byte(value)) {
				t.Fatalf("real value reached encoded argv: %q", payload)
			}
		}
	}
	before := len(guest.requests)
	if err := machines.WriteBlock(t.Context(), e, "app1", block); err != nil {
		t.Fatal(err)
	}
	if got := string(guest.content); got != want {
		t.Fatalf("repeated WriteBlock changed /etc/environment to %q, want %q", got, want)
	}
	for _, req := range guest.requests[before:] {
		if req.Cmd == "sh" {
			t.Fatalf("repeated WriteBlock rewrote an unchanged /etc/environment: %+v", req)
		}
	}
}

func TestWriteBlockReportsMergeErrorWithoutOverwriting(t *testing.T) {
	block, err := envblock.Render([]envblock.Var{{Name: "GH_TOKEN", Value: "gho_hibplaceholder1234567890"}})
	if err != nil {
		t.Fatal(err)
	}
	corrupt := []byte(strings.ReplaceAll(string(block), "# END hibernaut secrets\n", ""))
	original := append([]byte("PATH=/usr/bin\n"), corrupt...)
	guest := &envFileGuest{content: append([]byte(nil), original...)}
	e := fake.New(fake.WithExec(guest.exec))
	e.Add(engine.Machine{ID: "app1", State: engine.Running, StartedAt: time.Now()})
	err = machines.WriteBlock(t.Context(), e, "app1", nil)
	if err == nil || !strings.Contains(err.Error(), "managed block begin without end") {
		t.Fatalf("WriteBlock error = %v, want merge error", err)
	}
	if !bytes.Equal(guest.content, original) {
		t.Fatalf("corrupt environment = %q, want original %q", guest.content, original)
	}
}

func TestBlockFromSecretsSkipsSecretsWithoutAVariable(t *testing.T) {
	block, err := machines.BlockFromSecrets([]vault.Secret{
		{Name: "github", Env: "GH_TOKEN", Placeholder: "gho_hibplaceholder1234567890"},
		{Name: "codex-refresh", Placeholder: "rt.hibplaceholder1234567890"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want, err := envblock.Render([]envblock.Var{{Name: "GH_TOKEN", Value: "gho_hibplaceholder1234567890"}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(block, want) {
		t.Errorf("block = %q, want %q", block, want)
	}
	if block, err := machines.BlockFromSecrets([]vault.Secret{{Name: "codex-id", Placeholder: "x"}}); err != nil || block != nil {
		t.Errorf("block of secrets without variables = %q, %v; want none, as with no secrets", block, err)
	}
}
