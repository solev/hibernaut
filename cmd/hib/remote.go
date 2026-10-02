package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/termsafe"
)

// sshArgs returns ssh's arguments to run hibd with args on the host. They keep the user's ssh
// config from adding anything to the connection that could get in the way: forwardings, a
// local command printing into the bridge, or a remote command of its own.
func (c *config) sshArgs(args ...string) []string {
	out := []string{"-T",
		"-o", "ClearAllForwardings=yes",
		"-o", "ForwardAgent=no",
		"-o", "ForwardX11=no",
		"-o", "PermitLocalCommand=no",
		"-o", "RemoteCommand=none",
		"-o", "ConnectTimeout=15",
		"-o", "ServerAliveInterval=60",
		"-o", "ServerAliveCountMax=3",
	}
	if !c.prompt {
		out = append(out, "-o", "BatchMode=yes")
	}
	if c.Identity != "" {
		out = append(out, "-i", c.Identity, "-o", "IdentitiesOnly=yes")
	}
	// The host's shell splits the command on spaces. Every arg is a checked name, a flag, or a
	// time.Duration's String, which has only digits, letters, '.' and '-'.
	return append(append(out, "--", c.Host, "hibd"), args...)
}

// runCommand returns a runFunc for program.
func runCommand(program string) runFunc {
	return func(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
		cmd := exec.CommandContext(ctx, program, args...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
		// Don't wait for output from anything ssh leaves running, such as a connection master.
		cmd.WaitDelay = 5 * time.Second
		err := cmd.Run()
		var exit *exec.ExitError
		switch {
		case err == nil:
			return 0, nil
		case errors.As(err, &exit) && exit.ExitCode() >= 0:
			return exit.ExitCode(), nil
		case errors.Is(err, exec.ErrWaitDelay):
			return cmd.ProcessState.ExitCode(), nil
		case ctx.Err() != nil:
			return -1, ctx.Err()
		}
		return -1, err
	}
}

func runAz(ctx context.Context, args ...string) ([]byte, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "az", args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 500 {
			msg = "…" + msg[len(msg)-500:]
		}
		return nil, fmt.Errorf("az %s %s: %w: %s", args[0], args[1], err, msg)
	}
	return out, nil
}

// remote runs hibd with args, which end in --json, on the host and decodes its reply. ssh's and
// hibd's messages go to stderr, made safe for a terminal. If ssh can't reach the host and it's
// an Azure VM that isn't running, remote starts it, waits for it and tries again.
func (a *app) remote(ctx context.Context, cfg *config, timeout time.Duration, args ...string) (hibapi.Response, error) {
	for woke := false; ; woke = true {
		var out bytes.Buffer
		code, err := a.runRemote(ctx, cfg, timeout, args, nil, &out)
		if err != nil {
			return hibapi.Response{}, err
		}
		if code == 255 && !woke {
			err := a.wake(ctx, cfg)
			if err == nil {
				continue
			}
			if !errors.Is(err, errAwake) {
				return hibapi.Response{}, err
			}
		}
		if code != 0 && code != 1 {
			return hibapi.Response{}, exitError(cfg, code)
		}
		return hibapi.DecodeResponse(out.Bytes())
	}
}

func (a *app) runRemote(ctx context.Context, cfg *config, timeout time.Duration, args []string, stdin io.Reader, stdout io.Writer) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stderr := termsafe.NewWriter(a.stderr)
	defer stderr.Flush()
	code, err := a.ssh(ctx, cfg.sshArgs(args...), stdin, stdout, stderr)
	if errors.Is(err, context.DeadlineExceeded) {
		return code, fmt.Errorf("gave up on hibd %s after %v", args[0], timeout)
	}
	return code, err
}

// exitError explains an exit code from ssh running hibd, other than hibd's own 0 and 1.
func exitError(cfg *config, code int) error {
	switch code {
	case 255:
		return fmt.Errorf("couldn't reach %s: see ssh's message above", cfg.Host)
	case 127:
		return fmt.Errorf("hibd isn't installed on %s: run make deploy", cfg.Host)
	}
	return fmt.Errorf("hibd on %s exited %d", cfg.Host, code)
}

const (
	wakeTimeout     = 10 * time.Minute
	powerStateQuery = "instanceView.statuses[?starts_with(code,'PowerState')].displayStatus | [0]"
)

// errAwake means the host's VM is running, so being asleep isn't why ssh failed.
var errAwake = errors.New("the host is awake")

// wake starts the host's Azure VM unless it's running, and waits until hibd says E2B is ready.
// It returns errAwake if the VM is running, so being asleep isn't why ssh failed, or if there's
// no VM to start.
func (a *app) wake(ctx context.Context, cfg *config) error {
	if cfg.Azure == nil {
		return errAwake
	}
	ctx, cancel := context.WithTimeout(ctx, wakeTimeout)
	defer cancel()
	state, err := a.powerState(ctx, cfg.Azure)
	if err != nil {
		return err
	}
	if state == "VM running" {
		return errAwake
	}
	start := time.Now()
	fmt.Fprintf(a.stderr, "hib: the host is asleep (%s); starting it, which takes a few minutes\n", termsafe.String(state))
	// Azure won't start a VM on its way down.
	for state == "VM stopping" || state == "VM deallocating" {
		if err := a.sleep(ctx); err != nil {
			return err
		}
		if state, err = a.powerState(ctx, cfg.Azure); err != nil {
			return err
		}
	}
	if state != "VM starting" && state != "VM running" {
		if _, err := a.az(ctx, "vm", "start", "-g", cfg.Azure.Group, "-n", cfg.Azure.VM, "-o", "none"); err != nil {
			return wakeError(ctx, fmt.Errorf("starting the host: %w", err))
		}
	}
	for {
		code, err := a.ssh(ctx, cfg.sshArgs("ready", "--timeout", "5m"), nil, io.Discard, io.Discard)
		switch {
		case err != nil:
			return wakeError(ctx, err)
		case code == 0:
			fmt.Fprintf(a.stderr, "hib: the host is up after %v\n", time.Since(start).Round(time.Second))
			return nil
		case code != 255:
			return fmt.Errorf("the host is up, but hibd ready exited %d", code)
		}
		if err := a.sleep(ctx); err != nil {
			return err
		}
	}
}

func (a *app) powerState(ctx context.Context, vm *azureVM) (string, error) {
	out, err := a.az(ctx, "vm", "get-instance-view", "-g", vm.Group, "-n", vm.VM, "--query", powerStateQuery, "-o", "tsv")
	if err != nil {
		return "", wakeError(ctx, fmt.Errorf("checking whether the host is asleep: %w", err))
	}
	return strings.TrimSpace(string(out)), nil
}

// sleep waits a.wakePoll, unless wake's time is up.
func (a *app) sleep(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return wakeError(ctx, ctx.Err())
	case <-time.After(a.wakePoll):
		return nil
	}
}

func wakeError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("the host didn't come up within %v", wakeTimeout)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// bridge connects stdin and stdout to machine name's SSH server, through hibd bridge on the
// host. hibd first says the machine's host key, which bridge pins for ssh to check.
func (a *app) bridge(ctx context.Context, cfg *config, name string) int {
	for woke := false; ; woke = true {
		code, err := a.bridgeOnce(ctx, cfg, name)
		if err == nil {
			return 0
		}
		if code == 255 && !woke {
			werr := a.wake(ctx, cfg)
			if werr == nil {
				continue
			}
			if !errors.Is(werr, errAwake) {
				return a.fail(werr)
			}
		}
		switch {
		case code == 1:
			// hibd said why.
		case code == 0 && errors.Is(err, io.EOF):
			a.fail(errors.New("hibd closed the bridge before it was up"))
		case errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF):
			a.fail(exitError(cfg, code))
		default:
			a.fail(err)
		}
		return 1
	}
}

// bridgeOnce runs one hibd bridge. It reads nothing from stdin until the bridge is up, so it can
// run again after waking the host. err is nil if the bridge came up.
func (a *app) bridgeOnce(ctx context.Context, cfg *config, name string) (int, error) {
	// ssh gets the pipes' far ends as its stdin and stdout, so it ends without waiting on us.
	inR, inW, err := os.Pipe()
	if err != nil {
		return -1, err
	}
	defer inW.Close()
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		return -1, err
	}
	defer outR.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		code int
		err  error
	}
	done := make(chan result, 1)
	go func() {
		stderr := termsafe.NewWriter(a.stderr)
		code, err := a.ssh(ctx, cfg.sshArgs("bridge", name), inR, outW, stderr)
		stderr.Flush()
		inR.Close()
		outW.Close()
		done <- result{code, err}
	}()

	br := bufio.NewReaderSize(outR, hibapi.MaxHeader)
	key, err := hibapi.ReadBridgeHeader(br)
	if err == nil {
		err = cfg.pin(name, key)
	}
	if err != nil {
		cancel()
		r := <-done
		if r.err != nil && !errors.Is(r.err, context.Canceled) {
			err = r.err
		}
		return r.code, err
	}
	go func() {
		io.Copy(inW, a.stdin)
		inW.Close()
	}()
	// br may hold the start of the machine's SSH stream.
	io.Copy(a.stdout, br)
	r := <-done
	return r.code, nil
}
