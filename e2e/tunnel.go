package e2e

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/solev/hibernaut/internal/termsafe"
)

type sshTunnel struct {
	apiURL   string
	proxyURL string
	cmd      *exec.Cmd
	exited   chan error
}

type tunnelOutput struct {
	buffer    bytes.Buffer
	ready     chan struct{}
	announced bool
}

// OpenSSH enters its interactive loop only after setting up the requested forwards.
func (o *tunnelOutput) Write(p []byte) (int, error) {
	n, err := o.buffer.Write(p)
	if !o.announced && bytes.Contains(o.buffer.Bytes(), []byte("Entering interactive session.")) {
		o.announced = true
		close(o.ready)
	}
	return n, err
}

func startSSHTunnel(ctx context.Context, target string, options []string, command func(string, ...string) *exec.Cmd) (*sshTunnel, error) {
	var err error
	for range 5 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var tunnel *sshTunnel
		tunnel, err = trySSHTunnel(ctx, target, options, command)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "address already in use") {
			return tunnel, err
		}
	}
	return nil, err
}

func trySSHTunnel(ctx context.Context, target string, options []string, command func(string, ...string) *exec.Cmd) (*sshTunnel, error) {
	apiPort, proxyPort, err := freePorts()
	if err != nil {
		return nil, err
	}
	stderr := &tunnelOutput{ready: make(chan struct{})}
	cmd := command("ssh", append(slices.Clone(options), "-v", "-N",
		"-o", "ControlPath=none", "-o", "ExitOnForwardFailure=yes",
		"-L", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:3000", apiPort),
		"-L", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:3002", proxyPort),
		target)...)
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	tunnel := &sshTunnel{
		apiURL:   fmt.Sprintf("http://127.0.0.1:%d", apiPort),
		proxyURL: fmt.Sprintf("http://127.0.0.1:%d", proxyPort),
		cmd:      cmd,
		exited:   make(chan error, 1),
	}
	go func() { tunnel.exited <- cmd.Wait() }()
	ready, announced := false, stderr.ready
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for !ready || !healthy(ctx, tunnel.apiURL) {
		select {
		case <-announced:
			ready, announced = true, nil
		case err := <-tunnel.exited:
			tunnel.cmd = nil
			return nil, fmt.Errorf("ssh tunnel to %s: %v: %s", termsafe.String(target), err, termsafe.String(strings.TrimSpace(stderr.buffer.String())))
		case <-ctx.Done():
			tunnel.close()
			return nil, fmt.Errorf("e2b's API isn't healthy through the ssh tunnel to %s: %w", termsafe.String(target), ctx.Err())
		case <-tick.C:
		}
	}
	return tunnel, nil
}

func (t *sshTunnel) close() {
	if t == nil || t.cmd == nil {
		return
	}
	_ = t.cmd.Process.Kill()
	<-t.exited
	t.cmd = nil
}

func healthy(ctx context.Context, url string) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func freePorts() (int, int, error) {
	api, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, 0, err
	}
	defer api.Close()
	proxy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, 0, err
	}
	defer proxy.Close()
	return api.Addr().(*net.TCPAddr).Port, proxy.Addr().(*net.TCPAddr).Port, nil
}
