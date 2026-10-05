package hibd

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/engine/fake"
	"github.com/solev/hibernaut/internal/room"
)

func TestCommonErrorsEndWithNextStep(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		setup func(*testing.T, *App, *fake.Engine)
		want  string
	}{
		{"E2B unreachable", []string{"ls", "-json"}, func(_ *testing.T, a *App, _ *fake.Engine) {
			a.NewEngine = func() (engine.Engine, error) {
				return nil, &url.Error{Op: "Get", URL: "http://127.0.0.1:3000/sandboxes", Err: syscall.ECONNREFUSED}
			}
		}, "Check E2B on the host, then run hib status before trying again"},
		{"no room", []string{"new", "app", "-json"}, func(_ *testing.T, a *App, _ *fake.Engine) {
			a.ReadRoom = func() (room.Host, error) { return full, nil }
		}, "Run hib ls, then pause or remove machines you don't need before trying again"},
		{"image not built", []string{"new", "app", "-json"}, func(_ *testing.T, a *App, _ *fake.Engine) {
			a.NewEngine = func() (engine.Engine, error) { return fake.New(fake.WithTemplates()), nil }
		}, "(run: hib image build dev)"},
		{"busy pausing", []string{"image", "save", "app", "copy", "-json"}, func(t *testing.T, a *App, e *fake.Engine) {
			if code := a.Run(t.Context(), []string{"new", "app", "-json"}); code != 0 {
				t.Fatalf("new exit %d", code)
			}
			e.RejectNextSave(fmt.Errorf("machine is busy pausing: %w", engine.ErrNotRunning))
		}, "Wait for pausing or saving to finish, then check hib ls before trying again"},
		{"login in progress", []string{"connect", "github", "-json"}, func(t *testing.T, a *App, _ *fake.Engine) {
			unlock, ok, err := tryLock(a.Getenv("HIBD_STATE_DIR"), githubLoginLock, "login")
			if err != nil || !ok {
				t.Fatalf("login lock %v, %v", ok, err)
			}
			t.Cleanup(unlock)
		}, "finish it, or stop it, then try again"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, e, out, errOut := imageApp(t)
			withSecretConfig(t, a)
			tc.setup(t, a, e)
			out.Reset()
			errOut.Reset()
			code, reply, err := runJSON(t, a, out, tc.args...)
			if code != 1 || reply.V != 1 || !strings.HasSuffix(reply.Error, tc.want) {
				t.Fatalf("exit %d, reply %+v, %v, stderr %s. Want ending %q", code, reply, err, errOut, tc.want)
			}
			if strings.Contains(reply.Error, ".. ") {
				t.Fatalf("repeated sentence ending: %s", reply.Error)
			}
		})
	}
}

func TestVaultLockedErrorEndsWithNextStep(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, out, errOut := newApp(t, fake.New())
		withSecretConfig(t, a)
		a.EUID = func() int { return 0 }
		unlock, ok, err := tryLock(a.Getenv("HIBD_STATE_DIR"), "vault.json.lock", "vault")
		if err != nil || !ok {
			t.Fatalf("vault lock %v, %v", ok, err)
		}
		defer unlock()
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if code := a.Run(ctx, []string{"disconnect", "github", "-json"}); code != 1 ||
			!strings.Contains(out.String(), "Finish the command holding the vault lock, then try again") {
			t.Fatalf("exit %d, stdout %s, stderr %s", code, out, errOut)
		}
	})
}
