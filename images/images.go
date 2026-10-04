// Package images defines the machine images hibd builds into templates. An image is a directory
// of files embedded in hibd: scripts, systemd units and configs.
package images

import (
	"crypto/x509"
	"embed"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/hibapi"
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

// The template namespaces: built-in images, saved images, and forks' hidden templates.
const (
	builtPrefix = "hib-"
	savedPrefix = "hibimg-"
	forkPrefix  = "hibfork-"
)

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

// Build caches each step until it, or one before it, changes: put what is slow to build first.
type step struct {
	files []file
	ca    bool
	run   string
}

type file struct {
	name string
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
					{"gitconfig", "/etc/gitconfig", 0o644},
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
			{
				files: []file{{"trust-ca", lib + "/trust-ca", 0o755}},
				ca:    true,
				run:   lib + "/trust-ca",
			},
			{
				files: []file{
					{"versions", lib + "/versions", 0o644},
					{"tools", lib + "/tools", 0o755},
				},
				run: lib + "/tools",
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

// Template returns the template image name is in. A name in the catalog is a built-in image,
// built into hib-NAME. Any other name is a saved image, in hibimg-NAME, so a saved image never
// takes over a built-in one.
func Template(name string) string {
	if IsBuiltIn(name) {
		return builtPrefix + name
	}
	return SavedTemplate(name)
}

// IsBuiltIn says whether name is an image in the catalog.
func IsBuiltIn(name string) bool {
	_, ok := catalog[name]
	return ok
}

// SavedTemplate returns the template saved image name is in, even if a built-in image has
// that name.
func SavedTemplate(name string) string {
	return savedPrefix + name
}

// SavedName returns the name of the saved image in template alias, and whether alias holds
// one.
func SavedName(alias string) (string, bool) {
	name, ok := strings.CutPrefix(alias, savedPrefix)
	if !ok || hibapi.CheckName(name) != nil {
		return "", false
	}
	return name, true
}

// ForkTemplate returns the alias of the hidden template a fork of machine id, begun at at,
// saves the machine into.
func ForkTemplate(id string, at time.Time) string {
	return fmt.Sprintf("%s%s-%d", forkPrefix, id, at.UnixMilli())
}

// IsForkTemplate says whether alias is a fork's hidden template.
func IsForkTemplate(alias string) bool {
	return strings.HasPrefix(alias, forkPrefix)
}

// MemoryMB returns the memory image name needs on the host.
func MemoryMB(name string) (int, error) {
	img, err := lookup(name)
	if err != nil {
		return 0, err
	}
	return img.memoryMB, nil
}

// Spec returns how to build image name with the Hibernaut CA trusted inside it.
func Spec(name string, caPEM []byte) (engine.TemplateSpec, error) {
	img, err := lookup(name)
	if err != nil {
		return engine.TemplateSpec{}, err
	}
	if err := validateCA(caPEM); err != nil {
		return engine.TemplateSpec{}, err
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
		install, err := installStepScript(name, "", s, caPEM)
		if err != nil {
			return engine.TemplateSpec{}, err
		}
		spec.Steps = append(spec.Steps, engine.BuildStep{Cmd: install + s.run, User: "root"})
	}
	return spec, nil
}

func lookup(name string) (image, error) {
	img, ok := catalog[name]
	if !ok {
		return image{}, fmt.Errorf("no image %q (there is: %s)", name, strings.Join(Names(), ", "))
	}
	return img, nil
}

func validateCA(caPEM []byte) error {
	block, rest := pem.Decode(caPEM)
	if block == nil || block.Type != "CERTIFICATE" || len(strings.TrimSpace(string(rest))) != 0 {
		return fmt.Errorf("hibernaut CA must be exactly one PEM CERTIFICATE block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("parse hibernaut CA certificate: %w", err)
	}
	if !cert.IsCA {
		return fmt.Errorf("hibernaut CA certificate is not a CA certificate")
	}
	return nil
}

func installStepScript(name, root string, s step, caPEM []byte) (string, error) {
	install, err := installScript(name, root, s.files)
	if err != nil {
		return "", err
	}
	if s.ca {
		install += putDataScript(root+"/usr/local/share/ca-certificates/hibernaut.crt", 0o644, caPEM)
	}
	return install, nil
}

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

func putDataScript(dest string, mode fs.FileMode, data []byte) string {
	return fmt.Sprintf("put %s %o '%s'; ", quote(dest), mode, base64.StdEncoding.EncodeToString(data))
}

func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
