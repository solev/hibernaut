package github_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/solev/hibernaut/internal/github"
	"github.com/solev/hibernaut/internal/hibapi"
)

func TestProfileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "github.json")
	want := github.Profile{Login: "octocat", ID: 583231, Name: "The Octocat", Source: hibapi.GitHubFromToken}
	if err := github.SaveProfile(path, want); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("github.json mode = %v, want 0644", fi.Mode().Perm())
	}
	got, err := github.LoadProfile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || *got != want {
		t.Fatalf("LoadProfile = %+v, want %+v", got, want)
	}
	if email := got.Email(); email != "583231+octocat@users.noreply.github.com" {
		t.Errorf("Email() = %q", email)
	}
	want.Source = hibapi.GitHubFromGH
	if err := github.SaveProfile(path, want); err != nil {
		t.Fatal(err)
	}
	if got, err := github.LoadProfile(path); err != nil || *got != want {
		t.Fatalf("LoadProfile after overwriting = %+v, %v, want %+v", got, err, want)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("SaveProfile left files behind: %v", entries)
	}
}

func TestLoadProfileMissingMeansNotConnected(t *testing.T) {
	p, err := github.LoadProfile(filepath.Join(t.TempDir(), "github.json"))
	if p != nil || err != nil {
		t.Fatalf("LoadProfile = %+v, %v, want nil, nil", p, err)
	}
}

func TestProfileAccount(t *testing.T) {
	p := github.Profile{Login: "octocat", ID: 583231, Name: "The Octocat", Source: hibapi.GitHubFromToken}
	want := hibapi.GitHub{Login: "octocat", ID: 583231, Name: "The Octocat",
		Email: "583231+octocat@users.noreply.github.com", Source: hibapi.GitHubFromToken}
	if got := p.Account(nil); !reflect.DeepEqual(got, want) {
		t.Errorf("Account(nil) = %+v, want %+v", got, want)
	}
	user := &github.User{Login: "octocat", ID: 583231, Scopes: []string{"repo"}, ScopesKnown: true}
	want.Scopes, want.ScopesKnown = []string{"repo"}, true
	if got := p.Account(user); !reflect.DeepEqual(got, want) {
		t.Errorf("Account(user) = %+v, want %+v", got, want)
	}
}

func TestLoadProfileCleansTheName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "github.json")
	data := `{"v":1,"login":"octocat","id":1,"name":" -x\u001b[2J","source":"gh"}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := github.LoadProfile(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "octocat" {
		t.Errorf("Name = %q, want the login in place of a name that starts with -", p.Name)
	}
}

func TestLoadProfileRejectsCorruptFiles(t *testing.T) {
	for name, data := range map[string]string{
		"empty":       "",
		"not JSON":    "{",
		"no version":  `{"login":"octocat","id":1,"name":"x","source":"gh"}`,
		"new version": `{"v":2,"login":"octocat","id":1,"name":"x","source":"gh"}`,
		"bad login":   `{"v":1,"login":"-octocat","id":1,"name":"x","source":"gh"}`,
		"no ID":       `{"v":1,"login":"octocat","name":"x","source":"gh"}`,
		"bad source":  `{"v":1,"login":"octocat","id":1,"name":"x","source":"oauth"}`,
		"too big":     `{"v":1,"login":"octocat","id":1,"source":"gh","name":"` + strings.Repeat("x", 70<<10) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "github.json")
			if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
				t.Fatal(err)
			}
			p, err := github.LoadProfile(path)
			if err == nil {
				t.Fatalf("LoadProfile = %+v, want an error", p)
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("LoadProfile error = %q, want it to name the file", err)
			}
		})
	}
}

func TestSaveProfileRejectsBadProfiles(t *testing.T) {
	dir := t.TempDir()
	for name, p := range map[string]github.Profile{
		"bad login":  {Login: "octo cat", ID: 1, Name: "x", Source: hibapi.GitHubFromGH},
		"bad ID":     {Login: "octocat", Name: "x", Source: hibapi.GitHubFromGH},
		"bad source": {Login: "octocat", ID: 1, Name: "x", Source: "x"},
		"bad name":   {Login: "octocat", ID: 1, Name: "-x", Source: hibapi.GitHubFromGH},
	} {
		path := filepath.Join(dir, "github.json")
		if err := github.SaveProfile(path, p); err == nil {
			t.Errorf("SaveProfile with %s = nil, want an error", name)
		}
		if _, err := os.Stat(path); err == nil {
			t.Fatalf("SaveProfile with %s wrote the file", name)
		}
	}
}
