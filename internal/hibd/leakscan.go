package hibd

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/cleanup"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/hibapi"
	scanpkg "github.com/solev/hibernaut/internal/leakscan"
	"github.com/solev/hibernaut/internal/machines"
	"github.com/solev/hibernaut/internal/termsafe"
	"github.com/solev/hibernaut/internal/vault"
)

type leakscanTarget struct {
	kind string
	name string
	id   string
}

const leakscanUsage = "usage: hibd leakscan [-json] NAME | leakscan [-json] -image NAME | leakscan [-json] -canary"

func (a *App) leakscan(ctx context.Context, args []string) int {
	fl := a.flags("leakscan")
	image := fl.String("image", "", "scan the latest build of image NAME")
	canary := fl.Bool("canary", false, "prove leak scan finds a fresh test value")
	asJSON := fl.Bool("json", false, "print JSON")
	rest, ok := a.parseAny(fl, args)
	if !ok {
		return 2
	}
	if *canary {
		if *image != "" || len(rest) != 0 {
			fmt.Fprintln(a.Stderr, leakscanUsage)
			return 2
		}
		if err := a.wantRoot("leakscan"); err != nil {
			return a.fail(*asJSON, "leakscan", err)
		}
		return a.leakscanCanary(ctx, *asJSON)
	}
	if *image != "" && len(rest) != 0 || *image == "" && len(rest) != 1 {
		fmt.Fprintln(a.Stderr, leakscanUsage)
		return 2
	}
	var target leakscanTarget
	if *image != "" {
		if !slices.Contains(images.Names(), *image) {
			fmt.Fprintf(a.Stderr, "hibd leakscan: no image %q (there is: %s)\n", *image, strings.Join(images.Names(), ", "))
			return 2
		}
		target = leakscanTarget{kind: "image", name: *image}
	} else {
		target = leakscanTarget{kind: "machine", name: rest[0]}
	}
	if err := a.wantRoot("leakscan"); err != nil {
		return a.fail(*asJSON, "leakscan", err)
	}
	cfg, err := loadConfig(a.Getenv)
	if err != nil {
		return a.fail(*asJSON, "leakscan", err)
	}
	secrets, err := loadLeakscanSecrets(cfg)
	if err != nil {
		return a.fail(*asJSON, "leakscan", err)
	}
	if len(secrets) == 0 {
		r := hibapi.LeakScan{Target: apiLeakscanTarget(target), Hits: []hibapi.LeakScanHit{}}
		if *asJSON {
			return a.reply(hibapi.Response{LeakScan: &r})
		}
		fmt.Fprintf(a.Stdout, "no vault secrets in %s; nothing to scan\n", termsafe.String(cfg.vaultPath))
		return 0
	}
	result, err := a.runLeakscan(ctx, cfg, target, secrets)
	if err != nil {
		return a.fail(*asJSON, "leakscan", err)
	}
	return a.printLeakscanResult(*asJSON, result)
}

func (a *App) runLeakscan(ctx context.Context, cfg config, target leakscanTarget, secrets []scanpkg.Secret) (hibapi.LeakScan, error) {
	e, err := a.engine()
	if err != nil {
		return hibapi.LeakScan{}, err
	}
	query := a.query()
	switch target.kind {
	case "machine":
		m, err := findLeakscanMachine(ctx, e, target.name)
		if err != nil {
			return hibapi.LeakScan{}, err
		}
		target.id = m.ID
		if m.State == engine.Running {
			if err := e.Pause(ctx, m.ID); err != nil {
				return hibapi.LeakScan{}, err
			}
		}
		build, err := scanpkg.SnapshotBuild(ctx, query, m.ID)
		if err != nil {
			return hibapi.LeakScan{}, err
		}
		return scanBuild(cfg, target, build, secrets)
	case "image":
		build, err := scanpkg.TemplateBuild(ctx, query, images.Template(target.name))
		if err != nil {
			return hibapi.LeakScan{}, err
		}
		return scanBuild(cfg, target, build, secrets)
	}
	return hibapi.LeakScan{}, fmt.Errorf("unknown leakscan target %q", target.kind)
}

func scanBuild(cfg config, target leakscanTarget, build string, secrets []scanpkg.Secret) (hibapi.LeakScan, error) {
	needles, err := scanpkg.Needles(secrets)
	if err != nil {
		return hibapi.LeakScan{}, err
	}
	hits, err := scanpkg.ScanBuild(cfg.e2bStorage, build, needles)
	if err != nil {
		return hibapi.LeakScan{}, err
	}
	return hibapi.LeakScan{
		Target:      apiLeakscanTarget(target),
		BuildID:     build,
		SecretCount: len(secrets),
		Hits:        apiLeakscanHits(hits),
	}, nil
}

func (a *App) leakscanCanary(ctx context.Context, asJSON bool) int {
	cfg, err := loadConfig(a.Getenv)
	if err != nil {
		return a.fail(asJSON, "leakscan", err)
	}
	e, err := a.engine()
	if err != nil {
		return a.fail(asJSON, "leakscan", err)
	}
	canary, err := a.newCanary()
	if err != nil {
		return a.fail(asJSON, "leakscan", err)
	}
	name := "leakscan-canary-" + canary[len("hibcanary_"):len("hibcanary_")+12]
	m, err := e.Create(ctx, engine.CreateOptions{
		Template: images.Template(images.Default),
		Metadata: map[string]string{
			"hib.name":  name,
			"hib.image": images.Default,
		},
	})
	if err != nil {
		return a.fail(asJSON, "leakscan", err)
	}
	result, runErr := a.runCanaryScan(ctx, cfg, e, m, name, canary)
	killCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	killErr := e.Kill(killCtx, m.ID)
	if runErr != nil {
		if killErr != nil {
			runErr = fmt.Errorf("%w; also failed to kill canary %s: %v", runErr, m.ID, killErr)
		}
		return a.fail(asJSON, "leakscan", runErr)
	}
	if killErr != nil {
		return a.fail(asJSON, "leakscan", fmt.Errorf("killing canary %s: %w", m.ID, killErr))
	}
	if asJSON {
		a.reply(hibapi.Response{LeakScan: &result})
		if !result.MemoryFound || !result.DiskFound {
			return 1
		}
		return 0
	}
	if result.MemoryFound && result.DiskFound {
		fmt.Fprintf(a.Stdout, "canary found in memory and disk: build %s\n", result.BuildID)
		return 0
	}
	fmt.Fprintf(a.Stdout, "canary missing: memory=%t disk=%t build %s\n", result.MemoryFound, result.DiskFound, result.BuildID)
	return 1
}

func (a *App) runCanaryScan(ctx context.Context, cfg config, e engine.Engine, m engine.Machine, name, canary string) (hibapi.LeakScan, error) {
	req := engine.ExecRequest{
		Cmd:  "sh",
		Args: []string{"-lc", `printf %s "$HIB_CANARY" >/root/hibernaut-leakscan-canary; sync; nohup sh -c 'while :; do sleep 3600; done' >/dev/null 2>&1 &`},
		Env:  map[string]string{"HIB_CANARY": canary},
		User: "root",
	}
	if res, err := e.Exec(ctx, m.ID, req); err != nil {
		return hibapi.LeakScan{}, err
	} else if res.ExitCode != 0 {
		return hibapi.LeakScan{}, fmt.Errorf("planting canary failed: exit %d: %s", res.ExitCode, termsafe.String(string(res.Stderr)))
	}
	if err := e.Pause(ctx, m.ID); err != nil {
		return hibapi.LeakScan{}, err
	}
	build, err := scanpkg.SnapshotBuild(ctx, a.query(), m.ID)
	if err != nil {
		return hibapi.LeakScan{}, err
	}
	result, err := scanBuild(cfg, leakscanTarget{kind: "canary", name: name, id: m.ID}, build, []scanpkg.Secret{{Name: "canary", Value: canary}})
	if err != nil {
		return hibapi.LeakScan{}, err
	}
	result.MemoryFound, result.DiskFound = classifyCanaryHits(result.Hits)
	return result, nil
}

func classifyCanaryHits(hits []hibapi.LeakScanHit) (memory, disk bool) {
	for _, h := range hits {
		base := filepath.Base(h.Where)
		if h.Where == "memory" || base == "memfile" {
			memory = true
		}
		if h.Where == "disk" || base == "rootfs.ext4" {
			disk = true
		}
	}
	return memory, disk
}

func (a *App) printLeakscanResult(asJSON bool, result hibapi.LeakScan) int {
	if asJSON {
		a.reply(hibapi.Response{LeakScan: &result})
		if len(result.Hits) > 0 {
			return 1
		}
		return 0
	}
	if len(result.Hits) == 0 {
		fmt.Fprintf(a.Stdout, "clean: %s %s build %s scanned %d secrets\n", result.Target.Kind, termsafe.String(result.Target.Name), result.BuildID, result.SecretCount)
		return 0
	}
	for _, h := range result.Hits {
		fmt.Fprintf(a.Stdout, "%s %s %s %d\n", termsafe.String(h.Secret), termsafe.String(h.Encoding), termsafe.String(h.Where), h.Offset)
	}
	return 1
}

func loadLeakscanSecrets(cfg config) ([]scanpkg.Secret, error) {
	f, err := vault.Load(cfg.vaultPath)
	if err != nil {
		return nil, err
	}
	if len(f.Secrets()) == 0 {
		return nil, nil
	}
	key, err := vault.LoadKey(cfg.vaultKeyPath)
	if err != nil {
		return nil, err
	}
	revealed, err := f.Open(key)
	if err != nil {
		return nil, err
	}
	secrets := make([]scanpkg.Secret, len(revealed))
	for i, s := range revealed {
		secrets[i] = scanpkg.Secret{Name: s.Name, Value: s.Value}
	}
	return secrets, nil
}

func findLeakscanMachine(ctx context.Context, e engine.Engine, name string) (engine.Machine, error) {
	if err := hibapi.CheckName(name); err != nil {
		return engine.Machine{}, err
	}
	ms, err := e.List(ctx, engine.ListOptions{States: []engine.State{engine.Running, engine.Paused}})
	if err != nil {
		return engine.Machine{}, err
	}
	var found []engine.Machine
	for _, m := range ms {
		if machines.Name(m) == name {
			found = append(found, m)
		}
	}
	switch len(found) {
	case 0:
		return engine.Machine{}, fmt.Errorf("no machine called %q", name)
	case 1:
		return found[0], nil
	default:
		return engine.Machine{}, fmt.Errorf("more than one machine called %q", name)
	}
}

func (a *App) wantRoot(command string) error {
	euid := os.Geteuid
	if a.EUID != nil {
		euid = a.EUID
	}
	if euid() != 0 {
		return fmt.Errorf("%s must run as root. Use sudo hibd %s", command, command)
	}
	return nil
}

func (a *App) query() scanpkg.Query {
	if a.Query != nil {
		return a.Query
	}
	return cleanup.Postgres
}

func (a *App) newCanary() (string, error) {
	if a.NewCanary != nil {
		return a.NewCanary()
	}
	b := make([]byte, 16)
	if _, err := crand.Read(b); err != nil {
		return "", err
	}
	return "hibcanary_" + hex.EncodeToString(b), nil
}

func apiLeakscanTarget(t leakscanTarget) hibapi.LeakScanTarget {
	return hibapi.LeakScanTarget{Kind: t.kind, Name: t.name, ID: t.id}
}

func apiLeakscanHits(hits []scanpkg.Hit) []hibapi.LeakScanHit {
	out := make([]hibapi.LeakScanHit, len(hits))
	for i, h := range hits {
		out[i] = hibapi.LeakScanHit{Secret: h.Secret, Encoding: h.Encoding, Where: h.Where, Offset: h.Offset}
	}
	return out
}
