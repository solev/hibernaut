package e2b_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/e2b"
)

func TestSaveTemplateDistinguishesNoAnswer(t *testing.T) {
	for _, answered := range []bool{false, true} {
		t.Run(map[bool]string{false: "connection lost", true: "malformed HTTP answer"}[answered], func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if answered {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusCreated)
					w.Write([]byte("{"))
					return
				}
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				conn.Close()
			}))
			defer srv.Close()
			e, err := e2b.New(e2b.Config{APIURL: srv.URL, SandboxURL: srv.URL, APIKey: apiKey})
			if err != nil {
				t.Fatal(err)
			}
			_, err = e.SaveTemplate(t.Context(), origin, "hibimg-web")
			if err == nil || errors.Is(err, engine.ErrNoAnswer) == answered {
				t.Fatalf("answered %v: SaveTemplate = %v", answered, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, e := newReplayE2B(t, nil)
	_, err := e.SaveTemplate(ctx, origin, "hibimg-web")
	if !errors.Is(err, engine.ErrNoAnswer) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled request = %v", err)
	}
}

func TestSaveTemplateReadbackFailureIsAnswered(t *testing.T) {
	_, e := newReplayE2B(t, map[string]reply{
		"POST /sandboxes/" + origin + "/snapshots": {status: 201, body: map[string]any{
			"names": []string{"hibimg-web"}, "snapshotID": "hibimg-web:default",
		}},
		"GET /templates/aliases/hibimg-web": e2bError(500, "readback failed"),
	})
	_, err := e.SaveTemplate(t.Context(), origin, "hibimg-web")
	if err == nil || errors.Is(err, engine.ErrNoAnswer) {
		t.Fatalf("201 then failed readback = %v", err)
	}
}
