//go:build !e2e

package e2e

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestSSHTunnelWithOpenSSH(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("OpenSSH isn't installed")
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		server, channels, requests, err := ssh.NewServerConn(conn, config)
		if err != nil {
			t.Error("the local fake SSH server couldn't finish its handshake")
			return
		}
		defer server.Close()
		go ssh.DiscardRequests(requests)
		for channel := range channels {
			var destination struct {
				Host       string
				Port       uint32
				Origin     string
				OriginPort uint32
			}
			if channel.ChannelType() != "direct-tcpip" || ssh.Unmarshal(channel.ExtraData(), &destination) != nil ||
				destination.Host != "127.0.0.1" || destination.Port != 3000 {
				channel.Reject(ssh.Prohibited, "only the fake API is reachable")
				t.Error("the tunnel requested something other than the fake API")
				continue
			}
			stream, requests, err := channel.Accept()
			if err != nil {
				t.Error("the fake API couldn't accept its forwarded connection")
				continue
			}
			go ssh.DiscardRequests(requests)
			workers.Go(func() {
				defer stream.Close()
				request, err := http.ReadRequest(bufio.NewReader(stream))
				if err != nil {
					t.Error("the fake API didn't receive an HTTP request")
					return
				}
				request.Body.Close()
				if request.Method != http.MethodGet || request.URL.Path != "/health" {
					t.Error("the tunnel didn't request the fake API's health")
					return
				}
				io.WriteString(stream, "HTTP/1.1 200 OK\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
			})
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		<-done
		workers.Wait()
	})
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	options := []string{
		"-F", "/dev/null", "-p", strconv.Itoa(listener.Addr().(*net.TCPAddr).Port),
		"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=no",
		"-o", "StdinNull=no",
		"-o", "UserKnownHostsFile=/dev/null", "-o", "GlobalKnownHostsFile=/dev/null",
		"-o", "IdentityAgent=none", "-o", "IdentityFile=none", "-o", "IdentitiesOnly=yes",
	}
	tunnel, err := startSSHTunnel(ctx, "127.0.0.1", options, exec.Command)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tunnel.close)
}

func TestSSHTunnelRetriesBindFailure(t *testing.T) {
	for _, port := range []string{"api", "proxy"} {
		t.Run(port, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			attempts := 0
			command := func(name string, args ...string) *exec.Cmd {
				attempts++
				mode := "ready"
				if attempts == 1 {
					mode = port
				}
				return tunnelProcess(t, mode, name, args...)
			}
			tunnel, err := startSSHTunnel(ctx, "fake-host", nil, command)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(tunnel.close)
			if attempts != 2 {
				t.Errorf("started ssh %d times, want a retry after the bind failure", attempts)
			}
			if !healthy(ctx, tunnel.apiURL) {
				t.Fatal("the retried tunnel didn't reach a healthy API")
			}
			proxyAddr := strings.TrimPrefix(tunnel.proxyURL, "http://")
			conn, err := net.Dial("tcp", proxyAddr)
			if err != nil {
				t.Fatalf("connecting to the client proxy: %v", err)
			}
			conn.Close()
			tunnel.close()
			for _, url := range []string{tunnel.apiURL, tunnel.proxyURL} {
				listener, err := net.Listen("tcp", strings.TrimPrefix(url, "http://"))
				if err != nil {
					t.Fatalf("closing the tunnel didn't release %s: %v", url, err)
				}
				listener.Close()
			}
		})
	}
}

func TestSSHTunnelDoesNotMistakeAnotherServerForItsAPI(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	attempts := 0
	command := func(name string, args ...string) *exec.Cmd {
		attempts++
		if attempts == 1 {
			for i, arg := range args {
				if arg != "-L" {
					continue
				}
				addr := "127.0.0.1:" + strings.Split(args[i+1], ":")[1]
				listener, err := net.Listen("tcp", addr)
				if err != nil {
					t.Fatal(err)
				}
				server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusOK)
				}))
				server.Listener.Close()
				server.Listener = listener
				server.Start()
				t.Cleanup(server.Close)
				break
			}
		}
		return tunnelProcess(t, "ready", name, args...)
	}
	tunnel, err := startSSHTunnel(ctx, "fake-host", nil, command)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tunnel.close)
	if attempts != 2 {
		t.Fatalf("started ssh %d times, want to reject the competing healthy server and retry", attempts)
	}
}

func TestSSHTunnelStopsOnFailure(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mode     string
		attempts int
		want     string
	}{
		{"bounded retries", "api", 5, "address already in use"},
		{"authentication", "auth", 1, "Permission denied (publickey)"},
		{"missing executable", "missing", 1, "no such file or directory"},
		{"already cancelled", "cancelled", 0, "context canceled"},
		{"cancelled during startup", "cancel", 1, "context canceled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			if tc.mode == "cancelled" {
				cancel()
			}
			var commands []*exec.Cmd
			command := func(name string, args ...string) *exec.Cmd {
				var cmd *exec.Cmd
				switch tc.mode {
				case "missing":
					cmd = exec.Command(filepath.Join(t.TempDir(), "no-ssh"))
				case "cancel":
					cancel()
					cmd = tunnelProcess(t, "ready", name, args...)
				default:
					cmd = tunnelProcess(t, tc.mode, name, args...)
				}
				commands = append(commands, cmd)
				return cmd
			}
			tunnel, err := startSSHTunnel(ctx, "fake-host", nil, command)
			if tunnel != nil {
				tunnel.close()
				t.Fatal("a failed start returned a tunnel")
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
			if strings.ContainsAny(err.Error(), "\x1b\x07") {
				t.Fatal("tunnel diagnostics contain unescaped terminal controls")
			}
			if (tc.mode == "cancelled" || tc.mode == "cancel") && !errors.Is(err, context.Canceled) {
				t.Errorf("the startup error doesn't preserve cancellation: %v", err)
			}
			if len(commands) != tc.attempts {
				t.Errorf("started ssh %d times, want %d", len(commands), tc.attempts)
			}
			for _, cmd := range commands {
				if cmd.Process != nil && cmd.ProcessState == nil {
					t.Error("the failed tunnel left its ssh process running")
				}
			}
		})
	}
}

func tunnelProcess(t *testing.T, mode, name string, args ...string) *exec.Cmd {
	t.Helper()
	if name != "ssh" || len(args) == 0 || args[len(args)-1] != "fake-host" {
		t.Fatalf("unexpected tunnel command %s %q", name, args)
	}
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, append([]string{"-test.run=^TestSSHTunnelProcess$", "--"}, args...)...)
	cmd.Env = append(os.Environ(), "HIB_TEST_SSH_TUNNEL="+mode)
	return cmd
}

func TestSSHTunnelProcess(t *testing.T) {
	mode := os.Getenv("HIB_TEST_SSH_TUNNEL")
	if mode == "" {
		t.Skip("only runs in the fake ssh subprocess")
	}
	if mode == "auth" {
		fmt.Fprint(os.Stderr, "Permission denied (publickey).\x1b]0;changed\x07\n")
		os.Exit(255)
	}
	var ports []int
	for i, arg := range os.Args {
		if arg == "-L" && i+1 < len(os.Args) {
			port, err := strconv.Atoi(strings.Split(os.Args[i+1], ":")[1])
			if err != nil {
				t.Fatal(err)
			}
			ports = append(ports, port)
		}
	}
	if len(ports) != 2 || ports[0] == ports[1] {
		t.Fatalf("want two distinct forwarding ports, got %v", ports)
	}
	if mode == "api" || mode == "proxy" {
		port := ports[0]
		if mode == "proxy" {
			port = ports[1]
		}
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		if other, err := net.Listen("tcp", addr); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(255)
		} else {
			other.Close()
			t.Fatal("binding an occupied port succeeded")
		}
	}
	api, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", ports[0]))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(255)
	}
	defer api.Close()
	proxy, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", ports[1]))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(255)
	}
	defer proxy.Close()
	fmt.Fprintln(os.Stderr, "debug1: Entering interactive session.")
	if err := http.Serve(api, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})); err != nil {
		t.Fatal(err)
	}
}
