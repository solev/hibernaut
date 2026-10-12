package hibd

import (
	"context"
	"net"
	"os"
	"strconv"
	"time"
)

// watchdog pings systemd's watchdog, every third of WATCHDOG_USEC, until ctx is done or one of
// hibd proxy's serve goroutines has ended. With links on, it pings only while healthy answers,
// so a proxy whose control socket is wedged is replaced. A slow answer only delays a ping. It
// does nothing unless systemd asked for pings: hibd-proxy.service sets WatchdogSec=.
func (a *App) watchdog(ctx context.Context, ended <-chan struct{}, healthy func(context.Context) error, logf func(format string, args ...any)) {
	socket := a.Getenv("NOTIFY_SOCKET")
	usec, err := strconv.ParseInt(a.Getenv("WATCHDOG_USEC"), 10, 64)
	if socket == "" || err != nil || usec <= 0 {
		return
	}
	if pid := a.Getenv("WATCHDOG_PID"); pid != "" && pid != strconv.Itoa(os.Getpid()) {
		return
	}
	tick := time.NewTicker(time.Duration(usec) * time.Microsecond / 3)
	defer tick.Stop()
	var failing error
	for {
		select {
		case <-ctx.Done():
			return
		case <-ended:
			return
		case <-tick.C:
		}
		var err error
		if healthy != nil {
			err = healthy(ctx)
		}
		if err == nil && !isClosed(ended) {
			err = sdNotify(socket, "WATCHDOG=1")
		}
		switch {
		case ctx.Err() != nil:
			return
		case err != nil && failing == nil:
			logf("not pinging systemd's watchdog: %v", err)
		case err == nil && failing != nil:
			logf("pinging systemd's watchdog again")
		}
		failing = err
	}
}

// sdNotify sends state to systemd's notification socket. Go maps a leading @, the abstract
// namespace systemd may use, to the NUL Linux expects.
func sdNotify(socket, state string) error {
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: socket, Net: "unixgram"})
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Write([]byte(state))
	return err
}
