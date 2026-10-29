package linkadmit_test

import (
	"errors"
	"testing"
	"testing/synctest"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/linkadmit"
	"github.com/solev/hibernaut/internal/room"
)

func TestWakeRechecksPeopleReserveUnderRoomLock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		v := newEnv(t)
		v.source("src")
		v.target("web", engine.Paused, 8080)
		linkadmit.SetReadRoom(v.a, func() (room.Host, error) {
			return room.Host{PageSize: 2 << 20, Pages: 8192, FreePages: 1024, DiskFree: 80 << 30}, nil
		})
		ad, err := v.admit("src", "web", 8080)
		if err == nil {
			v.a.Release(ad)
		}
		if !errors.Is(err, room.ErrFull) {
			t.Fatalf("wake with only the people's 2 GiB left = %v, want ErrFull", err)
		}
		if n := v.e.resumed(); n != 0 {
			t.Fatalf("resumed %d times after room advice became stale", n)
		}
	})
}
