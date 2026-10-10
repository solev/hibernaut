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
	"sync"
	"time"

	"github.com/solev/hibernaut/internal/audit"
	capkg "github.com/solev/hibernaut/internal/ca"
	"github.com/solev/hibernaut/internal/codex"
	"github.com/solev/hibernaut/internal/egress"
	"github.com/solev/hibernaut/internal/egress/hook"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/links"
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
	var err error
	if *unhook {
		err = a.proxyHook(0, 0).Remove(ctx)
		if errors.Is(err, hook.ErrStrayRule) {
			// Every rule of ours is gone. The one iptables kept is of a shape no hibd writes.
			fmt.Fprintf(a.Stderr, "hibd proxy: warning: %v\n", err)
			return 0
		}
	} else {
		err = a.runProxy(ctx)
	}
	if err != nil {
		return a.fail(false, "proxy", err)
	}
	return 0
}

type proxySetup struct {
	cfg    config
	ca     *capkg.CA
	port   int
	engine engine.Engine
	// finder is the one slots.Finder egress and links share.
	finder slots.Finder
}

func (a *App) runProxy(ctx context.Context) error {
	setup, err := a.loadProxySetup()
	if err != nil {
		return err
	}
	logf := log.New(a.Stderr, "", log.LstdFlags).Printf
	var side *linkSide
	if setup.cfg.linksConfiguredOn() {
		if setup.cfg.linkPort == setup.port {
			return fmt.Errorf("HIBD_LINK_PORT and HIBD_PROXY_PORT are both %d, but links and egress need a port each", setup.port)
		}
		if side, err = a.newLinkSide(setup, logf); err != nil {
			return err
		}
	}
	writer, err := openAuditLog(setup.cfg.auditLogPath)
	if err != nil {
		side.discard()
		return err
	}
	listener, err := a.listenProxy(setup.port)
	if err != nil {
		_ = writer.Close()
		side.discard()
		return err
	}
	service := &proxypkg.Service{
		Server: a.egressServer(ctx, setup, writer, logf), Rule: a.proxyHook(setup.port, setup.cfg.linkPort),
		Audit: writer, Logf: logf,
	}
	return a.serveProxy(ctx, service, listener, side)
}

// serveProxy serves egress and, with links on, the links side, until ctx is done or one of them
// stops, and then stops them all: the two listeners go up and down together. Meanwhile it pings
// systemd's watchdog.
func (a *App) serveProxy(ctx context.Context, service *proxypkg.Service, l net.Listener, side *linkSide) error {
	ended := make(chan struct{})
	var once sync.Once
	end := func() { once.Do(func() { close(ended) }) }
	// Every part is stopped when it is told to rather than when ctx ends, so they stop in order.
	detached := context.WithoutCancel(ctx)
	egressCtx, stopEgress := context.WithCancel(detached)
	defer stopEgress()
	egressDone := make(chan error, 1)
	go func() {
		err := service.Serve(egressCtx, l)
		end()
		egressDone <- err
	}()
	var healthy func(context.Context) error
	if side != nil {
		side.start(detached, end)
		healthy = side.healthy
	}
	watchdogCtx, stopWatchdog := context.WithCancel(detached)
	defer stopWatchdog()
	watchdogDone := make(chan struct{})
	go func() {
		defer close(watchdogDone)
		a.watchdog(watchdogCtx, ended, healthy, service.Logf)
	}()
	select {
	case <-ctx.Done():
	case <-ended:
	}
	stopWatchdog()
	<-watchdogDone
	stopEgress()
	var linksErr error
	if side != nil {
		linksErr = side.stop()
	}
	return errors.Join(<-egressDone, linksErr)
}

func (a *App) loadProxySetup() (proxySetup, error) {
	cfg, err := loadConfig(a.Getenv)
	if err != nil {
		return proxySetup{}, err
	}
	ca, err := loadProxyCA(cfg)
	if err != nil {
		return proxySetup{}, err
	}
	port, err := proxyPort(cfg.proxyPort)
	if err != nil {
		return proxySetup{}, err
	}
	e, err := a.engine()
	if err != nil {
		return proxySetup{}, err
	}
	return proxySetup{cfg: cfg, ca: ca, port: port, engine: e, finder: slots.Finder{Cgroups: cfg.cgroups}}, nil
}

func loadProxyCA(cfg config) (*capkg.CA, error) {
	ca, err := capkg.Load(cfg.caCert, cfg.caKey)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("CA is missing. Run sudo hibd ca init: %w", err)
	}
	return ca, err
}

func proxyPort(setting string) (int, error) {
	port, err := strconv.Atoi(setting)
	if err != nil || port <= 0 || port > 65535 {
		return 0, errors.New("HIBD_PROXY_PORT must be a port from 1 to 65535")
	}
	return port, nil
}

func openAuditLog(path string) (*audit.Log, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return audit.Open(path, 10<<20)
}

func (a *App) listenProxy(port int) (net.Listener, error) {
	listen := a.ProxyListen
	if listen == nil {
		listen = net.Listen
	}
	return listen("tcp", fmt.Sprintf(":%d", port))
}

// proxyHook is the iptables hook. With linkPort 0 it carries the egress rule alone, with no link
// rule.
func (a *App) proxyHook(port, linkPort int) proxypkg.Rule {
	if a.ProxyHook != nil {
		return a.ProxyHook
	}
	rule := hook.Rule{ProxyPort: port}
	if linkPort != 0 {
		rule.LinkPort, rule.LinkCIDR = linkPort, links.CIDR
	}
	return proxyRule{rule}
}

func (a *App) egressServer(ctx context.Context, setup proxySetup, writer *audit.Log, logf func(format string, args ...any)) *egress.Server {
	source := &proxypkg.VaultSource{Path: setup.cfg.vaultPath, KeyPath: setup.cfg.vaultKeyPath, Now: time.Now, Logf: logf}
	identifier := &proxypkg.Identifier{
		Context: ctx, Sandbox: setup.finder.Sandbox,
		List: setup.engine.List, Now: time.Now, Logf: logf,
	}
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	server := &egress.Server{
		Swapper: source.Swapper, Leaf: setup.ca.Leaf, Accept: hook.OnVeth, OriginalDst: hook.OriginalDst,
		Identify: identifier.Identify, Dial: dialer.DialContext, Audit: writer.Write, Now: time.Now, Logf: logf,
	}
	a.serveCodex(server, setup.cfg, logf)
	return server
}

func (a *App) serveCodex(server *egress.Server, cfg config, logf func(format string, args ...any)) {
	ep := codex.NewEndpoint(a.codexStore(cfg, logf), logf)
	server.Endpoints = []egress.Endpoint{ep.Egress()}
	server.Rejected = ep.Rejected
}

type proxyRule struct{ hook.Rule }

func (r proxyRule) Remove(ctx context.Context) error { return hook.Remove(ctx, r.Iptables) }
