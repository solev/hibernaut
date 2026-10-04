package e2b_test

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/solev/hibernaut/internal/engine"
)

const proxyAbort = "the connection to sandbox aaaaaaaaaaaaaaaaaaaa ended before the stream completed"

func TestExecReportsStreamCutByE2BProxy(t *testing.T) {
	f, e := newFakeE2B(t)
	f.add("aaaaaaaaaaaaaaaaaaaa", "running", nil)
	f.mu.Lock()
	f.envd = http.HandlerFunc(cutByProxy(t))
	f.mu.Unlock()

	_, err := e.Exec(t.Context(), "aaaaaaaaaaaaaaaaaaaa", engine.ExecRequest{Cmd: "machine-init"})
	if !errors.Is(err, engine.ErrConnectionLost) {
		t.Fatalf("Exec cut by E2B's proxy = %v, want ErrConnectionLost", err)
	}
	if got := connect.CodeOf(err); got != connect.CodeUnavailable {
		t.Errorf("connect.CodeOf(Exec's error) = %v, want unavailable", got)
	}
	if msg := err.Error(); !strings.Contains(msg, "unavailable: "+proxyAbort) {
		t.Errorf("Exec's error %q lost E2B's code and message", msg)
	}
}

// cutByProxy answers the way E2B's sandbox proxy does when the connection to the sandbox breaks
// mid-stream: the data frames already sent, then an end-of-stream frame with code unavailable.
func cutByProxy(t *testing.T) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		data, err := proto.Marshal(stdout([]byte("partial")))
		if err != nil {
			t.Error(err)
			return
		}
		end, err := json.Marshal(map[string]any{"error": map[string]string{"code": "unavailable", "message": proxyAbort}})
		if err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/connect+proto")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(envelope(0, data))
		_, _ = w.Write(envelope(2, end))
	}
}

func envelope(flags byte, body []byte) []byte {
	frame := make([]byte, 5, 5+len(body))
	frame[0] = flags
	binary.BigEndian.PutUint32(frame[1:], uint32(len(body)))
	return append(frame, body...)
}

func TestExecReportsBadGatewayAsConnectionLost(t *testing.T) {
	f, e := newFakeE2B(t)
	f.add("aaaaaaaaaaaaaaaaaaaa", "running", nil)
	f.mu.Lock()
	f.envd = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad gateway", http.StatusBadGateway)
	})
	f.mu.Unlock()

	_, err := e.Exec(t.Context(), "aaaaaaaaaaaaaaaaaaaa", engine.ExecRequest{Cmd: "id"})
	if !errors.Is(err, engine.ErrConnectionLost) {
		t.Errorf("Exec answered 502 = %v, want ErrConnectionLost", err)
	}
}

func TestExecStreamEnds(t *testing.T) {
	tests := []struct {
		name     string
		req      engine.ExecRequest
		wantLost bool
		wantErr  bool
		wantCode connect.Code
	}{
		{name: "without the exit", req: engine.ExecRequest{Cmd: "noend"}, wantLost: true, wantErr: true},
		{name: "unavailable", req: engine.ExecRequest{Cmd: "fail", Args: []string{"unavailable"}}, wantLost: true, wantErr: true, wantCode: connect.CodeUnavailable},
		{name: "canceled", req: engine.ExecRequest{Cmd: "fail", Args: []string{"canceled"}}, wantErr: true, wantCode: connect.CodeCanceled},
		{name: "deadline exceeded", req: engine.ExecRequest{Cmd: "fail", Args: []string{"deadline_exceeded"}}, wantErr: true, wantCode: connect.CodeDeadlineExceeded},
		{name: "unauthenticated", req: engine.ExecRequest{Cmd: "fail", Args: []string{"unauthenticated"}}, wantErr: true, wantCode: connect.CodeUnauthenticated},
		{name: "internal", req: engine.ExecRequest{Cmd: "fail", Args: []string{"internal"}}, wantErr: true, wantCode: connect.CodeInternal},
		{name: "exit 0", req: engine.ExecRequest{Cmd: "true"}},
		{name: "exit 7", req: engine.ExecRequest{Cmd: "sh"}},
	}
	f, e := newFakeE2B(t)
	f.add("aaaaaaaaaaaaaaaaaaaa", "running", nil)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := e.Exec(t.Context(), "aaaaaaaaaaaaaaaaaaaa", tt.req)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Exec = %v, want error %t", err, tt.wantErr)
			}
			if got := errors.Is(err, engine.ErrConnectionLost); got != tt.wantLost {
				t.Errorf("Exec = %v, ErrConnectionLost %t, want %t", err, got, tt.wantLost)
			}
			if tt.wantCode != 0 {
				if got := connect.CodeOf(err); got != tt.wantCode {
					t.Errorf("connect.CodeOf(%v) = %v, want %v", err, got, tt.wantCode)
				}
			}
		})
	}
}

func TestExecInPausedMachineIsNotConnectionLost(t *testing.T) {
	f, e := newFakeE2B(t)
	f.add("aaaaaaaaaaaaaaaaaaaa", "paused", nil)

	_, err := e.Exec(t.Context(), "aaaaaaaaaaaaaaaaaaaa", engine.ExecRequest{Cmd: "id"})
	if !errors.Is(err, engine.ErrNotRunning) || errors.Is(err, engine.ErrConnectionLost) {
		t.Errorf("Exec(paused) = %v, want only ErrNotRunning", err)
	}
}
