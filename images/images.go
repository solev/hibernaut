// Package images defines the machine images hibd builds into templates. An image is a directory
// of files embedded in hibd: scripts, systemd units and configs that build steps install into
// the template. The paths and port the guest side shares with hibd are constants here.
package images

import (
	"embed"
	"encoding/base64"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"

	"github.com/solev/hibernaut/internal/engine"
)

// Default is the image machines are created from when no other is asked for.
const Default = "dev"

const (
	// MachineInit is run by hibd as root in each new machine, with the host's authorized_keys
	// base64-encoded as its only argument. It prints the machine's SSH host public key.
	MachineInit = lib + "/machine-init"
	// SSHPort is where a machine's sshd is reached, over a WebSocket through the engine.
	SSHPort = 8081
)

const lib = "/usr/lib/hibernaut"

//go:embed dev
var files embed.FS

type image struct {
	from          string
	cpus          int
	memoryMB      int
	minFreeDiskMB int
	steps         []step
	startCmd      string
	readyCmd      string
}

// A step installs files from the image's directory and then runs a command, as root. Build
// caches each step until it, or one before it, changes: put what is slow to build first.
type step struct {
	files []file
	run   string
}

type file struct {
	name string // in the image's directory
	dest string
	mode fs.FileMode
}

var catalog = map[string]image{
	"dev": {
		from:          "ubuntu:24.04",
		cpus:          2,
		memoryMB:      2048,
		minFreeDiskMB: 10240,
		steps: []step{
			{
				files: []file{{"packages", lib + "/packages", 0o755}},
				run:   lib + "/packages",
			},
			{
				files: []file{
					{"sshd_config", "/etc/hibernaut/sshd_config", 0o644},
					{"hib-sshd.socket", "/etc/systemd/system/hib-sshd.socket", 0o644},
					{"hib-sshd@.service", "/etc/systemd/system/hib-sshd@.service", 0o644},
					{"hib-websocat.service", "/etc/systemd/system/hib-websocat.service", 0o644},
					{"ghostty.terminfo", lib + "/ghostty.terminfo", 0o644},
					{"hib-term.sh", "/etc/profile.d/hib-term.sh", 0o644},
					{"configure", lib + "/configure", 0o755},
					{"start", lib + "/start", 0o755},
					{"ready", lib + "/ready", 0o755},
					{"machine-init", MachineInit, 0o755},
				},
				run: lib + "/configure",
			},
		},
		// The start command runs as the template's default user, which has passwordless sudo.
		startCmd: "sudo -n " + lib + "/start",
		readyCmd: lib + "/ready",
	},
}

// Names returns the images there are, sorted.
func Names() []string {
	names := make([]string, 0, len(catalog))
	for name := range catalog {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// Template returns the name of the template image name is built into.
func Template(name string) string {
	return "hib-" + name
}

// Spec returns how to build image name.
func Spec(name string) (engine.TemplateSpec, error) {
	img, ok := catalog[name]
	if !ok {
		return engine.TemplateSpec{}, fmt.Errorf("no image %q (there is: %s)", name, strings.Join(Names(), ", "))
	}
	spec := engine.TemplateSpec{
		Name:          Template(name),
		FromImage:     img.from,
		StartCmd:      img.startCmd,
		ReadyCmd:      img.readyCmd,
		CPUs:          img.cpus,
		MemoryMB:      img.memoryMB,
		MinFreeDiskMB: img.minFreeDiskMB,
	}
	for _, s := range img.steps {
		install, err := installScript(name, "", s.files)
		if err != nil {
			return engine.TemplateSpec{}, err
		}
		spec.Steps = append(spec.Steps, engine.BuildStep{Cmd: install + s.run, User: "root"})
	}
	return spec, nil
}

// installScript returns a shell command that writes files, from image name's directory, under
// root. Each file is replaced in one rename. The command is a single line, the file contents
// base64 in single quotes, so that nothing in the files can be read as shell.
func installScript(name, root string, list []file) (string, error) {
	var b strings.Builder
	b.WriteString(`set -euo pipefail; umask 022; put() { install -d -m 755 "$(dirname "$1")"; printf %s "$3" | base64 -d >"$1.tmp"; chmod "$2" "$1.tmp"; mv -f "$1.tmp" "$1"; }; `)
	for _, f := range list {
		data, err := files.ReadFile(path.Join(name, f.name))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "put %s %o '%s'; ", quote(root+f.dest), f.mode, base64.StdEncoding.EncodeToString(data))
	}
	return b.String(), nil
}

// quote quotes s for the shell.
func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
