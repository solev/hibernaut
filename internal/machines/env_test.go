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
)

type envFileGuest struct {
	content           []byte
	casMismatchesLeft int
	writes            int
	requests          []engine.ExecRequest
}

func (g *envFileGuest) exec(_ context.Context, _ engine.Machine, _ string, req engine.ExecRequest) (engine.ExecResult, error) {
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
		if len(req.Args) == 5 && req.Args[0] == "-c" {
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
			g.writes++
			return engine.ExecResult{}, nil
		}
	}
	return engine.ExecResult{ExitCode: 127, Stderr: []byte(fmt.Sprintf("unexpected %+v", req))}, nil
}

func TestWriteBlockRetriesCASAndNeverSendsValues(t *testing.T) {
	guest := &envFileGuest{content: []byte("PATH=/usr/bin\n"), casMismatchesLeft: 1}
	e := fake.New(fake.WithExec(guest.exec))
	e.Add(engine.Machine{ID: "app1", State: engine.Running, StartedAt: time.Now()})
	block, err := envblock.Render([]envblock.Var{{Name: "GH_TOKEN", Value: "gho_hibplaceholder1234567890"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := machines.WriteBlock(t.Context(), e, "app1", block); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(guest.content, []byte("GH_TOKEN=gho_hibplaceholder1234567890\n")) {
		t.Fatalf("/etc/environment = %q", guest.content)
	}
	if guest.writes != 1 {
		t.Fatalf("writes = %d, want 1", guest.writes)
	}
	for _, req := range guest.requests {
		if strings.Contains(strings.Join(req.Args, " "), "real-secret-value") {
			t.Fatalf("real value reached argv: %+v", req)
		}
	}
	if err := machines.WriteBlock(t.Context(), e, "app1", block); err != nil {
		t.Fatal(err)
	}
	if guest.writes != 1 {
		t.Fatalf("unchanged block wrote again; writes = %d", guest.writes)
	}
}

func TestWriteBlockReportsMergeErrorWithoutOverwriting(t *testing.T) {
	block, err := envblock.Render([]envblock.Var{{Name: "GH_TOKEN", Value: "gho_hibplaceholder1234567890"}})
	if err != nil {
		t.Fatal(err)
	}
	corrupt := []byte(strings.ReplaceAll(string(block), "# END hibernaut secrets\n", ""))
	guest := &envFileGuest{content: append([]byte("PATH=/usr/bin\n"), corrupt...)}
	e := fake.New(fake.WithExec(guest.exec))
	e.Add(engine.Machine{ID: "app1", State: engine.Running, StartedAt: time.Now()})
	err = machines.WriteBlock(t.Context(), e, "app1", nil)
	if err == nil || !strings.Contains(err.Error(), "managed block begin without end") {
		t.Fatalf("WriteBlock error = %v, want merge error", err)
	}
	if guest.writes != 0 {
		t.Fatalf("corrupt block was overwritten %d times", guest.writes)
	}
}
