package hibd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/solev/hibernaut/internal/audit"
	capkg "github.com/solev/hibernaut/internal/ca"
	"github.com/solev/hibernaut/internal/egress"
	"github.com/solev/hibernaut/internal/egress/hook"
	proxypkg "github.com/solev/hibernaut/internal/proxy"
	"github.com/solev/hibernaut/internal/slots"
)

func (a *App) ca(args []string) int {
	if len(args) != 1 || args[0] != "init" {
		fmt.Fprintln(a.Stderr, "usage: hibd ca init")
		return 2
	}
	if err := a.needRoot("ca init"); err != nil {
		return a.fail(false, "ca init", err)
	}
	cfg, err := loadSettings(a.Getenv)
	if err != nil {
		return a.fail(false, "ca init", err)
	}
	created, err := capkg.Init(cfg.caCert, cfg.caKey, "Hibernaut CA")
	if err != nil {
		return a.fail(false, "ca init", err)
	}
	if created {
		fmt.Fprintln(a.Stdout, "created Hibernaut CA")
	} else {
		fmt.Fprintln(a.Stdout, "found existing Hibernaut CA")
	}
	return 0
}

func (a *App) proxy(ctx context.Context, args []string) int {
	fl := a.flags("proxy")
	unhook := fl.Bool("unhook", false, "remove the proxy's iptables rule")
	if _, ok := a.parse(fl, args, 0, "proxy [-unhook]"); !ok {
		return 2
	}
	if err := a.needRoot("proxy"); err != nil {
		return a.fail(false, "proxy", err)
	}
	if *unhook {
		if err := hook.Remove(ctx, ""); err != nil {
			return a.fail(false, "proxy", err)
		}
		return 0
	}
	cfg, err := loadConfig(a.Getenv)
	if err != nil {
		return a.fail(false, "proxy", err)
	}
	ca, err := capkg.Load(cfg.caCert, cfg.caKey)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			err = fmt.Errorf("CA is missing. Run sudo hibd ca init: %w", err)
		}
		return a.fail(false, "proxy", err)
	}
	port, err := strconv.Atoi(cfg.proxyPort)
	if err != nil || port <= 0 || port > 65535 {
		return a.fail(false, "proxy", fmt.Errorf("HIBD_PROXY_PORT must be a port from 1 to 65535"))
	}
	if err := os.MkdirAll(filepath.Dir(cfg.auditLogPath), 0o755); err != nil {
		return a.fail(false, "proxy", err)
	}
	writer, err := audit.Open(cfg.auditLogPath, 10<<20)
	if err != nil {
		return a.fail(false, "proxy", err)
	}
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		_ = writer.Close()
		return a.fail(false, "proxy", err)
	}
	e, err := a.engine()
	if err != nil {
		_ = listener.Close()
		_ = writer.Close()
		return a.fail(false, "proxy", err)
	}
	logf := log.New(a.Stderr, "", log.LstdFlags).Printf
	source := &proxypkg.VaultSource{Path: cfg.vaultPath, KeyPath: cfg.vaultKeyPath, Now: time.Now, Logf: logf}
	identifier := &proxypkg.Identifier{
		Context: ctx, Sandbox: (slots.Finder{Cgroups: cfg.cgroups}).Sandbox,
		List: e.List, Now: time.Now, Logf: logf,
	}
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	server := &egress.Server{
		Swapper: source.Swapper, Leaf: ca.Leaf, Accept: hook.OnVeth, OriginalDst: hook.OriginalDst,
		Identify: identifier.Identify, Dial: dialer.DialContext, Audit: writer.Write, Now: time.Now, Logf: logf,
	}
	service := &proxypkg.Service{
		Server: server, Rule: proxyRule{hook.Rule{Port: port}}, Audit: writer, Logf: logf,
	}
	if err := service.Serve(ctx, listener); err != nil {
		return a.fail(false, "proxy", err)
	}
	return 0
}

type proxyRule struct{ hook.Rule }

func (r proxyRule) Remove(ctx context.Context) error { return hook.Remove(ctx, r.Iptables) }
