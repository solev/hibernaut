//go:build host

package linkadmit_test

import (
	"context"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/linkadmit"
	"github.com/solev/hibernaut/internal/links"
)

// TestHostControlSocketBelongsToTheStateOwner: hibd proxy runs as root and gives the control
// socket to whoever owns the state directory, so the login user's hibd reaches it without sudo.
func TestHostControlSocketBelongsToTheStateOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Fatal("needs root to give the socket away")
	}
	const nobody = 65534
	state := t.TempDir()
	if err := os.Chown(state, nobody, nobody); err != nil {
		t.Fatal(err)
	}
	path := socketPath(t)
	v := newEnv(t, func(c *linkadmit.Config) {
		c.Socket = path
		c.Store = links.NewStore(state)
		c.StateDir = state
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- v.a.Serve(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := call(path, `{"op":"stat"}`); err == nil {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("the socket never answered: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st := fi.Sys().(*syscall.Stat_t); st.Uid != nobody || st.Gid != nobody {
		t.Errorf("the socket belongs to %d:%d, want the state directory's owner %d:%d",
			st.Uid, st.Gid, nobody, nobody)
	}
	cancel()
	if err := <-served; err != nil {
		t.Errorf("serve = %v", err)
	}
}
