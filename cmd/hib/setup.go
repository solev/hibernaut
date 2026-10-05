package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/crypto/ssh"

	"github.com/solev/hibernaut/internal/atomicfile"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/version"
)

type config struct {
	// Host is the host's SSH destination, USER@HOST or a Host from ~/.ssh/config.
	Host string `json:"host"`
	// Identity is the private key for the host and machines. Empty means ssh's defaults.
	Identity string `json:"identity,omitempty"`
	// Azure is the host's VM, which hib starts when the host doesn't answer.
	Azure *azureVM `json:"azure,omitempty"`
	// SSHConfig is the ssh config that includes hib's, if it isn't ~/.ssh/config.
	SSHConfig string `json:"sshConfig,omitempty"`

	dir    string
	prompt bool
}

type azureVM struct {
	Group string `json:"group"`
	VM    string `json:"vm"`
}

var (
	hostRE  = regexp.MustCompile(`^([A-Za-z0-9_][A-Za-z0-9_.-]*@)?[A-Za-z0-9][A-Za-z0-9.-]*$`)
	azureRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._()-]{0,89}$`)
	pathRE  = regexp.MustCompile(`^/[A-Za-z0-9 ._/+,:@=~-]*$`)
)

func (c *config) validate() error {
	if !hostRE.MatchString(c.Host) {
		return fmt.Errorf("bad host %q: want USER@HOST", c.Host)
	}
	for _, p := range []string{c.Identity, c.SSHConfig} {
		if p != "" {
			if err := checkPath(p); err != nil {
				return err
			}
		}
	}
	if c.Azure != nil && (!azureRE.MatchString(c.Azure.Group) || !azureRE.MatchString(c.Azure.VM)) {
		return fmt.Errorf("bad Azure resource group %q or VM %q", c.Azure.Group, c.Azure.VM)
	}
	return nil
}

func checkPath(p string) error {
	if !pathRE.MatchString(p) {
		return fmt.Errorf("can't use %q: hib writes it into ssh's config, so it must be absolute "+
			"and use only letters, digits, spaces and . _ / + , : @ = ~ -", p)
	}
	return nil
}

func loadConfig(dir string) (*config, error) {
	path := filepath.Join(dir, "config.json")
	for _, p := range []string{dir, path} {
		if err := checkPrivate(p); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("no settings in %s: run hib init first", dir)
			}
			return nil, err
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c config
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c.dir = dir
	return &c, nil
}

func checkPrivate(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s: can't tell its owner", path)
	}
	if int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("%s belongs to uid %d, not you", path, st.Uid)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("others can change %s (%v): run chmod go-w on it", path, fi.Mode().Perm())
	}
	return nil
}

type initOptions struct {
	host, identity, group, vm, sshConfig string
}

func initFlags(fl *flag.FlagSet) runner {
	var o initOptions
	fl.StringVar(&o.host, "host", "", "the `USER@HOST` that SSH reaches the host as (required)")
	fl.StringVar(&o.identity, "identity", "", "the private key `file` for the host and machines (default ssh's)")
	fl.StringVar(&o.group, "azure-group", "", "the host VM's Azure resource `group`, so hib can start it")
	fl.StringVar(&o.vm, "azure-vm", "", "the host's Azure `VM` name")
	fl.StringVar(&o.sshConfig, "ssh-config", "", "the ssh config `file` to include hib's settings in (default ~/.ssh/config)")
	return func(ctx context.Context, a *app, _ []string) int {
		dir, err := a.configDir(a.configFlag)
		if err != nil {
			return a.fail(err)
		}
		return a.init(ctx, dir, o)
	}
}

func (a *app) init(ctx context.Context, dir string, o initOptions) int {
	if o.host == "" {
		saved, err := loadConfig(dir)
		if err != nil {
			return a.mistake("missing --host USER@HOST")
		}
		fmt.Fprintf(a.stderr, "hib init: this laptop is already set up for %s\n", saved.Host)
		fmt.Fprintln(a.stderr, "next: hib new NAME; ssh NAME.hib")
		fmt.Fprintf(a.stderr, "to set it up again: %s\n", a.command.synopsis())
		return 2
	}
	cfg := &config{Host: o.host, dir: dir, prompt: true}
	var err error
	if o.identity != "" {
		if cfg.Identity, err = filepath.Abs(o.identity); err != nil {
			return a.fail(err)
		}
		if _, err := os.Stat(cfg.Identity); err != nil {
			return a.fail(err)
		}
	}
	if (o.group == "") != (o.vm == "") {
		return a.mistake("--azure-group and --azure-vm go together")
	}
	if o.group != "" {
		cfg.Azure = &azureVM{Group: o.group, VM: o.vm}
	}
	includer := o.sshConfig
	if includer != "" {
		if cfg.SSHConfig, err = filepath.Abs(includer); err != nil {
			return a.fail(err)
		}
		includer = cfg.SSHConfig
	} else if home := a.getenv("HOME"); home != "" {
		includer = filepath.Join(home, ".ssh", "config")
	} else {
		return a.fail(errors.New("$HOME isn't set: use --ssh-config FILE"))
	}
	if err := cfg.validate(); err != nil {
		return a.mistake(err.Error())
	}
	hib, err := a.executable()
	if err == nil {
		hib, err = filepath.Abs(hib)
	}
	if err != nil {
		return a.fail(fmt.Errorf("finding hib's own path: %w", err))
	}
	for _, p := range []string{hib, dir} {
		if err := checkPath(p); err != nil {
			return a.fail(err)
		}
	}
	if err := a.checkOpenSSH(ctx); err != nil {
		return a.fail(err)
	}

	r, err := a.remote(ctx, cfg, shortTimeout, "version", "--json")
	if err != nil {
		return a.fail(fmt.Errorf("checking hibd on %s: %w", cfg.Host, err))
	}
	cfg.prompt = false
	if err := cfg.save(hib); err != nil {
		return a.fail(err)
	}
	added, err := addInclude(includer, filepath.Join(dir, "ssh_config"))
	if err != nil {
		return a.fail(err)
	}
	fmt.Fprintf(a.stdout, "%s runs hibd %s\n", cfg.Host, safe(r.Version))
	if r.Version != version.Version {
		fmt.Fprintf(a.stdout, "note: this is hib %s; deploy the same release to the host\n", version.Version)
	}
	if added {
		fmt.Fprintf(a.stdout, "added hib's settings to %s\n", includer)
	}
	fmt.Fprintln(a.stdout, "next: hib image build dev; hib new NAME; ssh NAME.hib")
	return 0
}

var opensshRE = regexp.MustCompile(`OpenSSH_(\d+)\.(\d+)`)

// checkOpenSSH makes sure ssh is OpenSSH 9.6 or later. Older versions pass a host name with
// shell metacharacters to the ProxyCommand (CVE-2023-51385), so a link such as a git URL for
// a crafted NAME.hib could run commands on this laptop.
func (a *app) checkOpenSSH(ctx context.Context) error {
	var out bytes.Buffer
	code, err := a.ssh(ctx, []string{"-V"}, nil, &out, &out)
	if err != nil {
		return fmt.Errorf("running ssh -V: %w", err)
	}
	m := opensshRE.FindStringSubmatch(out.String())
	if code != 0 || m == nil {
		return fmt.Errorf("hib needs OpenSSH, but ssh -V says %q", strings.TrimSpace(out.String()))
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major < 9 || major == 9 && minor < 6 {
		return fmt.Errorf("hib needs OpenSSH 9.6 or later, which keeps shell commands out of host names (CVE-2023-51385); this is %s.%s", m[1], m[2])
	}
	return nil
}

func (c *config) save(hib string) error {
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(c.dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicfile.WriteFile(filepath.Join(c.dir, "config.json"), append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err := atomicfile.WriteFile(filepath.Join(c.dir, "ssh_config"), []byte(c.sshConfig(hib)), 0o600); err != nil {
		return err
	}
	known := filepath.Join(c.dir, "known_hosts")
	if err := os.MkdirAll(known, 0o700); err != nil {
		return err
	}
	return os.Chmod(known, 0o700)
}

func (c *config) sshConfig(hib string) string {
	var b strings.Builder
	b.WriteString("# Written by hib init, which rewrites it. ssh NAME.hib reaches machine NAME through hib.\n")
	b.WriteString("Host *.hib\n")
	// ssh 9.6+ refuses host names with shell metacharacters, so '%h' stays one word.
	fmt.Fprintf(&b, "  ProxyCommand '%s' -config '%s' bridge '%%h'\n", hib, c.dir)
	b.WriteString("  User user\n")

	fmt.Fprintf(&b, "  UserKnownHostsFile \"%s/known_hosts/%%h\"\n", c.dir)
	b.WriteString("  GlobalKnownHostsFile /dev/null\n")
	b.WriteString("  StrictHostKeyChecking yes\n")
	b.WriteString("  UpdateHostKeys no\n")
	b.WriteString("  CheckHostIP no\n")
	b.WriteString("  # Waking the host takes a few minutes.\n")
	b.WriteString("  ConnectTimeout 600\n")
	b.WriteString("  # E2B's proxy drops connections that are quiet for 10 minutes.\n")
	b.WriteString("  ServerAliveInterval 60\n")
	b.WriteString("  ServerAliveCountMax 3\n")
	b.WriteString("  # Machines run code you don't control; don't lend them your keys.\n")
	b.WriteString("  ForwardAgent no\n")
	b.WriteString("  ForwardX11 no\n")
	if c.Identity != "" {
		fmt.Fprintf(&b, "  IdentityFile \"%s\"\n", c.Identity)
		b.WriteString("  IdentitiesOnly yes\n")
	}
	return b.String()
}

func addInclude(path, file string) (bool, error) {
	line := `Include "` + file + `"`
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	mode := fs.FileMode(0o600)
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		for _, l := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(l) == line {
				return false, nil
			}
		}
		fi, err := os.Stat(path)
		if err != nil {
			return false, err
		}
		mode = fi.Mode().Perm()
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return false, err
		}
	default:
		return false, err
	}
	head := "# Added by hib init: ssh NAME.hib reaches Hibernaut machine NAME.\n" + line + "\n"
	if len(data) > 0 {
		head += "\n"
	}
	return true, atomicfile.WriteFile(path, append([]byte(head), data...), mode)
}

func (c *config) pin(name string, key ssh.PublicKey) error {
	dir := filepath.Join(c.dir, "known_hosts")
	if err := checkPrivate(dir); err != nil {
		return err
	}
	line := name + ".hib " + hibapi.FormatHostKey(key) + "\n"
	return atomicfile.WriteFile(filepath.Join(dir, name+".hib"), []byte(line), 0o600)
}

func (c *config) unpin(name string) error {
	err := os.Remove(filepath.Join(c.dir, "known_hosts", name+".hib"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
