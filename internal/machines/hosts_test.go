package machines_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/hostsblock"
	"github.com/solev/hibernaut/internal/machines"
)

const userHosts = "127.0.0.1 localhost\n::1 localhost ip6-localhost ip6-loopback\n127.0.1.1 e2b.local\n"

// hostsGuest is a machine's /etc/hosts behind envd. A mismatch is someone else editing the file
// between hibd's read and its write.
type hostsGuest struct {
	content    []byte
	mismatches int
	requests   []engine.ExecRequest
}

func (g *hostsGuest) exec(_ context.Context, _ engine.Machine, _ string, req engine.ExecRequest) (engine.ExecResult, error) {
	g.requests = append(g.requests, req)
	if req.User != "root" {
		return engine.ExecResult{ExitCode: 1, Stderr: []byte("not root")}, nil
	}
	switch {
	case req.Cmd == "cat" && len(req.Args) == 1 && req.Args[0] == "/etc/hosts":
		return engine.ExecResult{Stdout: bytes.Clone(g.content)}, nil
	case req.Cmd == "sh" && len(req.Args) == 5 && req.Args[0] == "-c" && req.Args[2] == "hibd-hosts":
		if g.mismatches > 0 {
			g.mismatches--
			g.content = append(g.content, "10.0.0.9 edited\n"...)
			return engine.ExecResult{ExitCode: 75}, nil
		}
		if fmt.Sprintf("%x", sha256.Sum256(g.content)) != req.Args[3] {
			return engine.ExecResult{ExitCode: 75}, nil
		}
		next, err := base64.StdEncoding.DecodeString(req.Args[4])
		if err != nil {
			return engine.ExecResult{ExitCode: 64, Stderr: []byte(err.Error())}, nil
		}
		g.content = next
		return engine.ExecResult{}, nil
	}
	return engine.ExecResult{ExitCode: 127, Stderr: fmt.Appendf(nil, "unexpected %+v", req)}, nil
}

func (g *hostsGuest) writes() int {
	n := 0
	for _, req := range g.requests {
		if req.Cmd == "sh" {
			n++
		}
	}
	return n
}

func hostsMachine(g *hostsGuest) *fake.Engine {
	e := fake.New(fake.WithExec(g.exec))
	e.Add(engine.Machine{ID: "app1", State: engine.Running, StartedAt: time.Now()})
	return e
}

func renderHosts(t *testing.T, hosts ...hostsblock.Host) []byte {
	t.Helper()
	block, err := hostsblock.Render(hosts)
	if err != nil {
		t.Fatal(err)
	}
	return block
}

func TestWriteHostsRetriesCASAndKeepsUserLines(t *testing.T) {
	old := renderHosts(t, hostsblock.Host{IP: "198.18.0.1", Name: "api.hib"})
	guest := &hostsGuest{content: []byte(userHosts + string(old) + "10.0.0.5 db\n"), mismatches: 1}
	e := hostsMachine(guest)
	block := renderHosts(t, hostsblock.Host{IP: "198.18.0.1", Name: "api.hib"}, hostsblock.Host{IP: "198.18.0.2", Name: "web.hib"})
	if err := machines.WriteHosts(t.Context(), e, "app1", block); err != nil {
		t.Fatal(err)
	}
	want := userHosts + string(block) + "10.0.0.5 db\n10.0.0.9 edited\n"
	if got := string(guest.content); got != want {
		t.Fatalf("/etc/hosts = %q, want %q", got, want)
	}
	if n := guest.writes(); n != 2 {
		t.Errorf("WriteHosts wrote %d times, want a retry after the mismatch", n)
	}

	before := len(guest.requests)
	if err := machines.WriteHosts(t.Context(), e, "app1", block); err != nil {
		t.Fatal(err)
	}
	for _, req := range guest.requests[before:] {
		if req.Cmd == "sh" {
			t.Fatalf("WriteHosts rewrote an unchanged /etc/hosts: %+v", req)
		}
	}
}

func TestWriteHostsRemovesTheBlock(t *testing.T) {
	block := renderHosts(t, hostsblock.Host{IP: "198.18.0.1", Name: "api.hib"})
	for name, empty := range map[string][]byte{"nil": nil, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			guest := &hostsGuest{content: []byte(userHosts + string(block) + "10.0.0.5 db\n")}
			if err := machines.WriteHosts(t.Context(), hostsMachine(guest), "app1", empty); err != nil {
				t.Fatal(err)
			}
			if got, want := string(guest.content), userHosts+"10.0.0.5 db\n"; got != want {
				t.Errorf("/etc/hosts = %q, want %q", got, want)
			}
		})
	}
}

func TestWriteHostsReportsMergeErrorWithoutOverwriting(t *testing.T) {
	block := renderHosts(t, hostsblock.Host{IP: "198.18.0.1", Name: "api.hib"})
	original := userHosts + strings.TrimSuffix(string(block), "# END hibernaut machines\n")
	guest := &hostsGuest{content: []byte(original)}
	err := machines.WriteHosts(t.Context(), hostsMachine(guest), "app1", block)
	if !errors.Is(err, hostsblock.ErrUnterminated) {
		t.Fatalf("WriteHosts = %v, want %v", err, hostsblock.ErrUnterminated)
	}
	if string(guest.content) != original || guest.writes() != 0 {
		t.Errorf("WriteHosts wrote %q over an unterminated block", guest.content)
	}
}

func TestWriteHostsGivesUpAfterFourMismatches(t *testing.T) {
	guest := &hostsGuest{content: []byte(userHosts), mismatches: 4}
	block := renderHosts(t, hostsblock.Host{IP: "198.18.0.1", Name: "api.hib"})
	err := machines.WriteHosts(t.Context(), hostsMachine(guest), "app1", block)
	if err == nil || !strings.Contains(err.Error(), "/etc/hosts") {
		t.Fatalf("WriteHosts = %v, want an error about /etc/hosts", err)
	}
	if guest.writes() != 4 || bytes.Contains(guest.content, block) {
		t.Errorf("WriteHosts tried %d times and left %q, want 4 tries and no block", guest.writes(), guest.content)
	}
}
