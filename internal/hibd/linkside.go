package hibd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/solev/hibernaut/images"
	"github.com/solev/hibernaut/internal/audit"
	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/linkadmit"
	"github.com/solev/hibernaut/internal/linkctl"
	"github.com/solev/hibernaut/internal/linkguard"
	"github.com/solev/hibernaut/internal/linkproxy"
	"github.com/solev/hibernaut/internal/links"
	"github.com/solev/hibernaut/internal/machines"
	"github.com/solev/hibernaut/internal/slots"
	"github.com/solev/hibernaut/internal/statefile"
)

// linkHost is what the links side of hibd proxy takes from the host. Tests replace
// proxyLinkHost.
type linkHost struct {
	guard  func() links.Guard
	tokens func() links.Tokens
	socket string
	// checks are the protections links need besides the guard. Links stay off until all of them
	// pass.
	checks []linkCheck
	// recheck is how often a failed check is run again.
	recheck time.Duration
}

// linkCheck is one protection hibd proxy checks before it serves links. A check that cannot
// run has failed.
type linkCheck struct {
	name string
	run  func(context.Context) error
}

var proxyLinkHost = linkHost{
	guard:  func() links.Guard { return linkguard.New() },
	tokens: func() links.Tokens { return linkguard.NewTokens() },
	socket: links.CtlSocket,
	checks: []linkCheck{
		{name: "the sandbox firewall", run: checkSandboxFirewall},
		{name: "the orchestrator's IPAddressDeny", run: checkOrchestratorDeny},
	},
	recheck: time.Minute,
}

// linkSide is the links half of hibd proxy: the Keeper, the control socket and the listener.
// It exists only while links are configured on.
type linkSide struct {
	host     linkHost
	port     int
	keeper   *linkguard.Keeper
	admitter *linkadmit.Admitter
	listener *linkproxy.Proxy
	ctl      *linkctl.Client
	log      *audit.LinkLog
	listen   func(port int) (net.Listener, error)
	logf     func(format string, args ...any)

	// keeperErr is why Keeper.Run returned, readable once keeperDone is closed.
	keeperDone chan struct{}
	keeperErr  error
	stop       func() error
}

// newLinkSide builds the links side: the guard and the tokens, the run watcher, the resolver,
// the store and the clock, the Keeper, the admitter and then the listener. Each part is built
// after the parts it is given, and the admitter gets the listener as its closer once both exist.
// It touches no kernel state and binds nothing.
func (a *App) newLinkSide(setup proxySetup, logf func(format string, args ...any)) (*linkSide, error) {
	host, cfg := proxyLinkHost, setup.cfg
	guard, tokens := host.guard(), host.tokens()
	runs, err := linkadmit.NewRuns(cfg.cgroups)
	if err != nil {
		return nil, err
	}
	resolver := slots.Resolver{Finder: setup.finder, Runs: runs}
	store, clock := links.NewStore(cfg.stateDir), links.NewClock(cfg.stateDir)
	keeper, err := linkguard.NewKeeper(linkguard.KeeperConfig{
		Guard: guard, Tokens: tokens, Runs: runs, Resolver: resolver, Store: store,
		Alive: func(owner string) (bool, error) { return statefile.Held(cfg.stateDir, owner) },
		Logf:  logf,
	})
	if err != nil {
		return nil, err
	}
	quiescing := func() bool {
		// Only a marker that is certainly absent lets links in.
		_, err := os.Stat(cfg.quiescing)
		return !errors.Is(err, fs.ErrNotExist)
	}
	admitter, err := linkadmit.New(linkadmit.Config{
		Engine: setup.engine, Runs: runs, Keeper: keeper, Dropper: guard, Resolver: resolver,
		Store: store, Clock: clock, Room: a.linkRoom(setup.engine), Quiescing: quiescing,
		Socket: host.socket,
	})
	if err != nil {
		return nil, err
	}
	linkLog := openLinkLog(cfg.linkLog, logf)
	listener, err := linkproxy.New(linkproxy.Config{
		Addr: fmt.Sprintf(":%d", cfg.linkPort), Admitter: admitter, Runs: runs, Keeper: keeper,
		Ingress: linkproxy.NewIngress(setup.finder), Dialer: linkproxy.NewDialer(),
		Doc: linkproxy.NewDocCache(store, filepath.Join(cfg.stateDir, links.StateName)),
		Log: linkLog, Quiescing: quiescing, Logf: logf,
	})
	if err != nil {
		_ = linkLog.Close()
		return nil, err
	}
	admitter.SetCloser(listener)
	ctl, err := linkctl.Dial(host.socket)
	if err != nil {
		_ = linkLog.Close()
		return nil, err
	}
	return &linkSide{
		host: host, port: cfg.linkPort, keeper: keeper, admitter: admitter, listener: listener,
		ctl: ctl, log: linkLog, listen: a.listenProxy, logf: logf,
	}, nil
}

// openLinkLog opens the link log, or returns nil, which drops entries: losing a log line is not
// worth losing a link.
func openLinkLog(path string, logf func(format string, args ...any)) *audit.LinkLog {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		logf("links will not be logged: %v", err)
		return nil
	}
	l, err := audit.OpenLinks(path, links.LogMaxBytes)
	if err != nil {
		logf("links will not be logged: %v", err)
		return nil
	}
	return l
}

// linkRoom reports the room for a link's wake of target, counted in default machines: how many
// more may run, less the one the owner is always kept, and how many waking target takes. A target
// whose size can't be read is refused, since it can't be charged.
func (a *App) linkRoom(e engine.Engine) func(target string) (int, int, error) {
	m := machines.New(machines.Config{Engine: e, ReadRoom: a.ReadRoom})
	return func(target string) (int, int, error) {
		ctx, cancel := context.WithTimeout(context.Background(), links.AdmitTimeout)
		defer cancel()
		em, err := e.Get(ctx, target)
		if err != nil {
			return 0, 0, err
		}
		if em.MemoryMB <= 0 {
			return 0, 0, fmt.Errorf("%s has no memory size", target)
		}
		def, err := images.MemoryMB(images.Default)
		if err != nil {
			return 0, 0, err
		}
		r, err := m.Room(ctx, images.Default)
		if err != nil {
			return 0, 0, err
		}
		// A machine with at most k times a default machine's memory takes no more pages and no
		// more pause disk than k default machines, so rounding k up charges it enough.
		need := max((em.MemoryMB+def-1)/def, 1)
		return max(r.Fits-1, 0), need, nil
	}
}

// discard closes what a links side that never started holds.
func (s *linkSide) discard() {
	if s != nil {
		_ = s.log.Close()
	}
}

// start runs the Keeper and the control socket, and the listener once the Keeper is ready and
// every check passes. end is called when the control socket or the listener stops, which stops
// hibd proxy. A Keeper whose first Install failed leaves links off until hibd proxy restarts,
// and egress goes on. ctx must not be cancelled: stop stops the pieces in order.
func (s *linkSide) start(ctx context.Context, end func()) {
	keeperCtx, stopKeeper := context.WithCancel(ctx)
	ctlCtx, stopCtl := context.WithCancel(ctx)
	linksCtx, stopLinks := context.WithCancel(ctx)
	s.keeperDone = make(chan struct{})
	go func() {
		defer close(s.keeperDone)
		// Run returns an error only when its first Install failed, and serveLinks logs it.
		s.keeperErr = s.keeper.Run(keeperCtx)
	}()
	ctlDone := make(chan error, 1)
	go func() {
		err := s.admitter.Serve(ctlCtx)
		end()
		ctlDone <- err
	}()
	linksDone := make(chan error, 1)
	go func() {
		err := s.serveLinks(linksCtx)
		end()
		linksDone <- err
	}()
	// Shutdown runs the other way: the listener closes every link, the Keeper retires its gates,
	// and the control socket goes last, so a pause meanwhile still gets an answer.
	s.stop = func() error {
		var errs []error
		stopLinks()
		if err := <-linksDone; err != nil && !errors.Is(err, context.Canceled) {
			errs = append(errs, fmt.Errorf("serving machine links: %w", err))
		}
		stopKeeper()
		<-s.keeperDone
		stopCtl()
		errs = append(errs, <-ctlDone)
		return errors.Join(errs...)
	}
}

// serveLinks serves the listener once the Keeper is ready and every check passes, and checks
// again every recheck until they do. It binds the link port only then, so a machine's link is
// refused rather than left waiting while links are off. A Keeper that stopped before it was
// ready never will be, so then it stops checking.
func (s *linkSide) serveLinks(ctx context.Context) error {
	served := false
	defer func() {
		if !served {
			_ = s.log.Close()
		}
	}()
	wait := time.NewTimer(s.host.recheck)
	defer wait.Stop()
	select {
	case <-s.keeper.Ready():
	case <-s.keeperDone:
	case <-wait.C:
	case <-ctx.Done():
		return nil
	}
	for {
		stopped := isClosed(s.keeperDone)
		err := s.check(ctx)
		if err == nil {
			l, lerr := s.listen(s.port)
			if lerr == nil {
				served = true
				s.logf("serving machine links on port %d", s.port)
				return s.listener.Serve(ctx, l)
			}
			err = fmt.Errorf("listening on port %d: %w", s.port, lerr)
		}
		if ctx.Err() != nil {
			return nil
		}
		if stopped {
			s.logf("links are off until hibd proxy restarts: %v", err)
			<-ctx.Done()
			return nil
		}
		s.logf("links are off, checking again in %v: %v", s.host.recheck, err)
		wait.Reset(s.host.recheck)
		select {
		case <-wait.C:
		case <-ctx.Done():
			return nil
		}
	}
}

func isClosed(c <-chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

// check runs every startup check and reports each one that failed.
func (s *linkSide) check(ctx context.Context) error {
	var failed []string
	if !isClosed(s.keeper.Ready()) {
		why := "it is not installed yet"
		if isClosed(s.keeperDone) {
			why = "its keeper has stopped"
			if s.keeperErr != nil {
				why = s.keeperErr.Error()
			}
		}
		failed = append(failed, "the link guard: "+why)
	}
	if err := s.healthy(ctx); err != nil {
		failed = append(failed, err.Error())
	}
	for _, c := range s.host.checks {
		if err := c.run(ctx); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", c.name, err))
		}
	}
	if len(failed) > 0 {
		return errors.New(strings.Join(failed, "; "))
	}
	return nil
}

// healthy reports whether the control socket answers.
func (s *linkSide) healthy(ctx context.Context) error {
	if _, err := s.ctl.Stat(ctx); err != nil {
		return fmt.Errorf("the link control socket: %w", err)
	}
	return nil
}
