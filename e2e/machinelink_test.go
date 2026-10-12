//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"net/netip"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/solev/hibernaut/internal/engine"
	"github.com/solev/hibernaut/internal/hibapi"
	"github.com/solev/hibernaut/internal/links"
)

const (
	linkHome       = "/home/user"
	linkPy         = linkHome + "/link.py"
	linkActivity   = "/var/lib/hibernaut/" + links.ActivityDir
	linkHostsBegin = "# BEGIN hibernaut machines: hibd manages this block and replaces edits"
	linkHostsEnd   = "# END hibernaut machines"
	// linkWakeGap is how long to wait after a link woke a machine before another may.
	linkWakeGap = links.WakeEvery + 2*time.Second
	// linkUnallocated is in links.CIDR, past every address hibd hands out first.
	linkUnallocated = "198.19.255.254"
	// linkIdle is the idle timeout of the machines the slow tests wait to pause, and linkSlack
	// covers E2B taking its time to pause one and the polls that notice.
	linkIdle  = 2 * time.Minute
	linkSlack = 3 * time.Minute
)

var (
	linkHostRE = regexp.MustCompile(`^(\S+) ([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)\.hib$`)
	linkRange  = netip.MustParsePrefix(links.CIDR)
	// linkNobody is the far end of a link to an address no machine has.
	linkNobody = linkMachine{name: "?"}
)

// linkScript runs in the machines. It serves lines, asks for them and holds links open, and
// writes what happened as plain lines the tests read back.
const linkScript = `import socket
import socketserver
import sys
import threading
import time


def classify(e):
    if isinstance(e, socket.gaierror):
        return "unresolved"
    if isinstance(e, TimeoutError):
        return "timeout"
    if isinstance(e, ConnectionRefusedError):
        return "refused"
    if isinstance(e, (ConnectionResetError, BrokenPipeError)):
        return "reset"
    if isinstance(e, EOFError):
        return "closed"
    if isinstance(e, OSError) and e.errno:
        return "error %d" % e.errno
    return "error " + type(e).__name__


class Conn:
    def __init__(self, host, port, timeout):
        self.sock = socket.create_connection((host, port), timeout=timeout)
        self.sock.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
        self.buf = b""

    def send(self, line):
        self.sock.sendall(line.encode() + b"\n")

    def readline(self, timeout):
        self.sock.settimeout(timeout)
        while b"\n" not in self.buf:
            data = self.sock.recv(4096)
            if not data:
                raise EOFError()
            self.buf += data
        line, _, self.buf = self.buf.partition(b"\n")
        return line.decode(errors="replace")

    def close(self):
        self.sock.close()


def exchange(host, port, line, timeout):
    try:
        c = Conn(host, port, timeout)
    except Exception as e:
        return classify(e)
    try:
        c.send(line)
        return "reply: " + c.readline(timeout)
    except Exception as e:
        return classify(e)
    finally:
        c.close()


def append(path, line):
    with open(path, "a") as f:
        f.write(line + "\n")


def serve(port, tag, log):
    lock = threading.Lock()

    class Handler(socketserver.StreamRequestHandler):
        disable_nagle_algorithm = True

        def handle(self):
            for raw in self.rfile:
                line = raw.decode(errors="replace").strip()
                if log:
                    with lock:
                        append(log, "%.3f %s" % (time.time(), line))
                self.wfile.write(("%s %s\n" % (tag, line)).encode())

    class Server(socketserver.ThreadingTCPServer):
        allow_reuse_address = True
        daemon_threads = True

    with Server(("0.0.0.0", port), Handler) as server:
        server.serve_forever()


def hold(host, port, seconds, every, out):
    start = time.monotonic()
    end = start + seconds
    try:
        c = Conn(host, port, 75)
        c.send("hello")
        append(out, "open " + c.readline(75))
    except Exception as e:
        append(out, "failed " + classify(e))
        return
    sent = 0
    try:
        while time.monotonic() < end:
            if every > 0:
                time.sleep(every)
                sent += 1
                c.send("beat %d" % sent)
                append(out, "reply " + c.readline(15))
                continue
            c.sock.settimeout(max(end - time.monotonic(), 0.001))
            try:
                data = c.sock.recv(4096)
            except TimeoutError:
                break
            if not data:
                raise EOFError()
            append(out, "data " + data.decode(errors="replace").strip())
    except Exception as e:
        append(out, "cut %s after %d sent %d" % (classify(e), time.monotonic() - start, sent))
        return
    finally:
        c.close()
    append(out, "held")


def loop(host, port, every, seconds, out):
    end = time.monotonic() + seconds
    while time.monotonic() < end:
        append(out, "%d %s" % (time.time(), exchange(host, port, "ping", 10)))
        time.sleep(every)


def percentiles(name, ms):
    ms = sorted(ms)
    at = lambda p: ms[min(len(ms) - 1, int(round(p / 100.0 * (len(ms) - 1))))]
    print("%s ms p50 %.2f p95 %.2f p99 %.2f" % (name, at(50), at(95), at(99)))


def bench(host, port, setups, trips):
    for _ in range(3):
        exchange(host, port, "warm", 10)
    setup = []
    for _ in range(setups):
        time.sleep(0.06)
        start = time.monotonic()
        result = exchange(host, port, "setup", 10)
        if not result.startswith("reply: "):
            sys.exit("setting up a link failed: " + result)
        setup.append((time.monotonic() - start) * 1000)
    c = Conn(host, port, 10)
    for _ in range(20):
        c.send("warm")
        c.readline(10)
    trip = []
    for _ in range(trips):
        start = time.monotonic()
        c.send("trip")
        c.readline(10)
        trip.append((time.monotonic() - start) * 1000)
    c.close()
    percentiles("setup", setup)
    percentiles("round trip", trip)


def main():
    cmd, args = sys.argv[1], sys.argv[2:]
    if cmd == "ask":
        print(exchange(args[0], int(args[1]), args[2], float(args[3])))
    elif cmd == "timed":
        start = time.monotonic()
        result = exchange(args[0], int(args[1]), args[2], float(args[3]))
        print("%.3f %s" % (time.monotonic() - start, result))
    elif cmd == "serve":
        serve(int(args[0]), args[1], args[2] if len(args) > 2 else "")
    elif cmd == "hold":
        hold(args[0], int(args[1]), float(args[2]), float(args[3]), args[4])
    elif cmd == "loop":
        loop(args[0], int(args[1]), float(args[2]), float(args[3]), args[4])
    elif cmd == "bench":
        bench(args[0], int(args[1]), int(args[2]), int(args[3]))
    else:
        sys.exit("unknown command " + cmd)


main()
`

// linkMachine is a machine the link tests made.
type linkMachine struct{ name, id string }

func (m linkMachine) host() string { return m.name + ".hib" }

// is reports whether endpoint, a From or To of the link log, is m. The log names a machine
// by its sandbox ID once it is gone.
func (m linkMachine) is(endpoint string) bool {
	return endpoint == m.name || m.id != "" && endpoint == m.id
}

func linkNew(t *testing.T, ctx context.Context, idle string) linkMachine {
	t.Helper()
	return linkNewFor(t, t, ctx, idle)
}

// linkNewFor makes a small machine that owner removes when it ends, so a subtest can make a
// machine a later one uses, and copies the link script into it.
func linkNewFor(t, owner *testing.T, ctx context.Context, idle string) linkMachine {
	t.Helper()
	name := fmt.Sprintf("e2e-%08x", rand.Uint32())
	owner.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		runHib(ctx, "rm", name, "--yes")
		if vm.engine == nil {
			return
		}
		if ms, err := vm.engine.List(ctx, engine.ListOptions{Metadata: map[string]string{"hib.name": name}}); err != nil || len(ms) > 0 {
			owner.Logf("leaving machine %s for the next run to delete", name)
		}
	})
	hib(t, ctx, "new", name, "--size", "small", "--idle", idle)
	m := linkMachine{name: name, id: machineID(t, ctx, name)}
	sh(t, ctx, m.id, fmt.Sprintf("echo %s | base64 -d > %s", base64.StdEncoding.EncodeToString([]byte(linkScript)), linkPy))
	return m
}

// linkServe starts a server on port in m that answers each line with tag and the line, and
// appends what it gets to log unless that is empty. It returns once the server answers.
func linkServe(t *testing.T, ctx context.Context, m linkMachine, port int, tag, log string) {
	t.Helper()
	sudo := ""
	if port < 1024 {
		sudo = "sudo -n "
	}
	sshMachine(t, ctx, m.name, fmt.Sprintf(`%[1]ssetsid -f python3 %[2]s serve %[3]d %[4]s %[5]s </dev/null >/dev/null 2>&1
for i in $(seq 100); do
	[ "$(python3 %[2]s ask 127.0.0.1 %[3]d ping 2)" = "reply: %[4]s ping" ] && exit 0
	sleep 0.1
done
echo "nothing answers on port %[3]d" >&2
exit 1`, sudo, linkPy, port, tag, log))
}

func linkAskCmd(host string, port int, line string, timeout time.Duration) string {
	return fmt.Sprintf("python3 %s ask %s %d %s %.0f", linkPy, host, port, line, timeout.Seconds())
}

// linkAsk sends one line from m to host:port and returns the reply as "reply: LINE", or what
// went wrong: unresolved, timeout, refused, reset or closed.
func linkAsk(t *testing.T, ctx context.Context, from linkMachine, host string, port int, timeout time.Duration) string {
	t.Helper()
	return sshMachine(t, ctx, from.name, linkAskCmd(host, port, "ping", timeout))
}

// linkWantReply asks host:port from m until the server tagged tag answers, for up to 30
// seconds, since a port or a hosts block pushed a moment ago may still be on its way. Each ask
// waits long enough for a link to wake the target.
func linkWantReply(t *testing.T, ctx context.Context, from linkMachine, host string, port int, tag string) {
	t.Helper()
	want := "reply: " + tag + " ping"
	start := time.Now()
	for {
		got := linkAsk(t, ctx, from, host, port, 75*time.Second)
		if got == want {
			return
		}
		if time.Since(start) > 30*time.Second {
			t.Fatalf("%s asked %s:%d and got %q, want %q", from.name, host, port, got, want)
		}
		sleepCtx(t, ctx, 2*time.Second)
	}
}

// linkWantNoReply asks host:port once from m and fails the test if anything answers.
func linkWantNoReply(t *testing.T, ctx context.Context, from linkMachine, host string, port int) {
	t.Helper()
	if got := linkAsk(t, ctx, from, host, port, 10*time.Second); strings.HasPrefix(got, "reply: ") {
		t.Errorf("%s asked %s:%d and got %q, want no answer", from.name, host, port, got)
	}
}

// linkRun runs script in m. Quietly it goes through E2B, which doesn't count as m's own
// activity, and otherwise over ssh, which does, and wakes m if it is paused.
func linkRun(t *testing.T, ctx context.Context, m linkMachine, quiet bool, script string) string {
	t.Helper()
	if quiet {
		return strings.TrimSpace(sh(t, ctx, m.id, script))
	}
	return sshMachine(t, ctx, m.name, script)
}

// linkHold opens a link from m to host:port in the background and keeps it for seconds,
// sending a beat every every seconds, or nothing when every is 0. It returns the file in m
// that says how the link opened and ended.
func linkHold(t *testing.T, ctx context.Context, m linkMachine, quiet bool, host string, port, seconds, every int) string {
	t.Helper()
	out := fmt.Sprintf("%s/hold-%08x.out", linkHome, rand.Uint32())
	linkRun(t, ctx, m, quiet, fmt.Sprintf("setsid -f python3 %s hold %s %d %d %d %s </dev/null >/dev/null 2>&1",
		linkPy, host, port, seconds, every, out))
	return out
}

func linkLines(t *testing.T, ctx context.Context, m linkMachine, quiet bool, path string) []string {
	t.Helper()
	out := linkRun(t, ctx, m, quiet, fmt.Sprintf("cat %s 2>/dev/null; true", path))
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// linkTryLines reads path in m through E2B, and reports false if m isn't running.
func linkTryLines(ctx context.Context, m linkMachine, path string) ([]string, bool) {
	res, err := vm.engine.Exec(ctx, m.id, engine.ExecRequest{Cmd: "sh", Args: []string{"-c", "cat " + path + " 2>/dev/null; true"}})
	if err != nil || res.ExitCode != 0 {
		return nil, false
	}
	out := strings.TrimSpace(string(res.Stdout))
	if out == "" {
		return nil, true
	}
	return strings.Split(out, "\n"), true
}

func linkHoldOpen(t *testing.T, ctx context.Context, m linkMachine, quiet bool, path, want string) {
	t.Helper()
	waitFor(t, ctx, m.name+"'s held link to open", 2*time.Minute, func() bool {
		lines := linkLines(t, ctx, m, quiet, path)
		if len(lines) == 0 {
			return false
		}
		if lines[0] != "open "+want {
			t.Fatalf("%s's held link began with %q, want %q", m.name, lines[0], "open "+want)
		}
		return true
	})
}

// linkHoldEnd waits for a held link to end and returns its last line: "cut REASON after
// SECONDS sent BEATS", "failed REASON" or "held".
func linkHoldEnd(t *testing.T, ctx context.Context, m linkMachine, quiet bool, path string, limit time.Duration) string {
	t.Helper()
	var last string
	waitFor(t, ctx, m.name+"'s held link to end", limit, func() bool {
		lines := linkLines(t, ctx, m, quiet, path)
		if len(lines) == 0 {
			return false
		}
		last = lines[len(lines)-1]
		return strings.HasPrefix(last, "cut ") || strings.HasPrefix(last, "failed ") || last == "held"
	})
	return last
}

// linkHostNow is the host's clock, which writes the link log and the journal.
func linkHostNow(t *testing.T, ctx context.Context) time.Time {
	t.Helper()
	out := mustSSH(t, ctx, "date +%s")
	sec, err := strconv.ParseInt(out, 10, 64)
	if err != nil {
		t.Fatalf("the host's date is %q: %v", out, err)
	}
	return time.Unix(sec, 0)
}

func linkLog(t *testing.T, ctx context.Context) []hibapi.LinkEntry {
	t.Helper()
	var r hibapi.Response
	if err := json.Unmarshal([]byte(hib(t, ctx, "log", "--links", "--n", "1000", "--json")), &r); err != nil {
		t.Fatalf("hib log --links --json: %v", err)
	}
	return r.Links
}

func linkFind(t *testing.T, entries []hibapi.LinkEntry, since time.Time, match func(hibapi.LinkEntry) bool) []hibapi.LinkEntry {
	t.Helper()
	var found []hibapi.LinkEntry
	for _, e := range entries {
		at, err := time.Parse(time.RFC3339, e.Time)
		if err != nil {
			t.Fatalf("link log time %q: %v", e.Time, err)
		}
		if !at.Before(since) && match(e) {
			found = append(found, e)
		}
	}
	return found
}

// linkLogged waits for the link log to have an entry since since that matches, and returns
// the first. A refusal is written once links.FoldWindow has folded its repeats.
func linkLogged(t *testing.T, ctx context.Context, since time.Time, what string, match func(hibapi.LinkEntry) bool) hibapi.LinkEntry {
	t.Helper()
	var found []hibapi.LinkEntry
	waitFor(t, ctx, what+" in the link log", links.FoldWindow+45*time.Second, func() bool {
		found = linkFind(t, linkLog(t, ctx), since, match)
		return len(found) > 0
	})
	return found[0]
}

func linkRefused(from, to linkMachine, port int, reason error) func(hibapi.LinkEntry) bool {
	return func(e hibapi.LinkEntry) bool {
		return e.Event == "refused" && from.is(e.From) && to.is(e.To) && e.Port == port && e.Reason == reason.Error()
	}
}

func linkEvent(event string, from, to linkMachine, port int) func(hibapi.LinkEntry) bool {
	return func(e hibapi.LinkEntry) bool {
		return e.Event == event && from.is(e.From) && to.is(e.To) && e.Port == port
	}
}

// linkPorts is hib port ls by machine name.
func linkPorts(t *testing.T, ctx context.Context) map[string]hibapi.Ports {
	t.Helper()
	var r hibapi.Response
	if err := json.Unmarshal([]byte(hib(t, ctx, "port", "ls", "--json")), &r); err != nil {
		t.Fatalf("hib port ls --json: %v", err)
	}
	ports := map[string]hibapi.Ports{}
	for _, p := range r.Ports {
		ports[p.Machine] = p
	}
	return ports
}

func linkPublished(p hibapi.Ports) []int {
	var ports []int
	for _, pp := range p.Published {
		ports = append(ports, pp.Port)
	}
	slices.Sort(ports)
	return ports
}

// linkOpen is how many links hib port ls says are open to port, or -1 if it doesn't say.
func linkOpen(p hibapi.Ports, port int) int {
	for _, pp := range p.Published {
		if pp.Port == port && pp.Open != nil {
			return *pp.Open
		}
	}
	return -1
}

// linkHosts reads m's /etc/hosts: hibd's block as addresses by machine name, and the lines
// outside it. A line in the block that isn't an address in links.CIDR and NAME.hib fails the
// test.
func linkHosts(t *testing.T, ctx context.Context, m linkMachine) (block map[string]string, outside []string) {
	t.Helper()
	block = map[string]string{}
	in := false
	for line := range strings.SplitSeq(strings.TrimSpace(sh(t, ctx, m.id, "cat /etc/hosts")), "\n") {
		switch {
		case line == linkHostsBegin:
			in = true
		case line == linkHostsEnd:
			in = false
		case !in:
			outside = append(outside, line)
		default:
			sub := linkHostRE.FindStringSubmatch(line)
			if sub == nil {
				t.Errorf("%s's hosts block has %q, want ADDRESS NAME.hib", m.name, line)
				continue
			}
			if addr, err := netip.ParseAddr(sub[1]); err != nil || !linkRange.Contains(addr) {
				t.Errorf("%s's hosts block gives %s.hib the address %s, outside %s", m.name, sub[2], sub[1], links.CIDR)
			}
			block[sub[2]] = sub[1]
		}
	}
	if in {
		t.Errorf("%s's hosts block has no end line", m.name)
	}
	return block, outside
}

// linkSetHost points name.hib at addr in m's /etc/hosts, as a process in m could.
func linkSetHost(t *testing.T, ctx context.Context, m linkMachine, name, addr string) {
	t.Helper()
	sshMachine(t, ctx, m.name, fmt.Sprintf(`sed -E 's/^[0-9.]+ %[1]s\.hib$/%[2]s %[1]s.hib/' /etc/hosts > %[3]s/hosts.new &&
sudo -n sh -c 'cat %[3]s/hosts.new > /etc/hosts' &&
grep -qxF '%[2]s %[1]s.hib' /etc/hosts`, name, addr, linkHome))
}

// linkRoom is how many more default machines hib ls says fit.
func linkRoom(t *testing.T, ctx context.Context) int {
	t.Helper()
	var r hibapi.Response
	if err := json.Unmarshal([]byte(hib(t, ctx, "ls", "--json")), &r); err != nil || r.Room == nil {
		t.Fatalf("hib ls --json gave no room: %v", err)
	}
	return r.Room.Fits
}

// linkPause pauses m as its idle timeout would, not by hand, so a link may wake it.
func linkPause(t *testing.T, ctx context.Context, m linkMachine) {
	t.Helper()
	if err := vm.engine.Pause(ctx, m.id); err != nil && !errors.Is(err, engine.ErrNotRunning) {
		t.Logf("Pause(%s): %v", m.name, err)
	}
	linkWaitState(t, ctx, m, engine.Paused, 2*time.Minute)
}

func linkState(t *testing.T, ctx context.Context, m linkMachine) engine.State {
	t.Helper()
	got, err := vm.engine.Get(ctx, m.id)
	if err != nil {
		t.Fatalf("Get(%s): %v", m.name, err)
	}
	return got.State
}

func linkWaitState(t *testing.T, ctx context.Context, m linkMachine, want engine.State, limit time.Duration) {
	t.Helper()
	waitFor(t, ctx, fmt.Sprintf("%s to be %s", m.name, want), limit, func() bool {
		return linkState(t, ctx, m) == want
	})
}

// linkStaysPaused fails the test if any of ms runs again within d.
func linkStaysPaused(t *testing.T, ctx context.Context, d time.Duration, ms ...linkMachine) {
	t.Helper()
	for end := time.Now().Add(d); time.Now().Before(end); sleepCtx(t, ctx, 15*time.Second) {
		for _, m := range ms {
			if got := linkState(t, ctx, m); got != engine.Paused {
				t.Fatalf("%s is %s again, want it to stay paused", m.name, got)
			}
		}
	}
}

// linkBackdate sets m's last own activity to the host's clock plus offset, which puts it in
// the past when offset is negative. The host reads its own clock, so no skew creeps in.
func linkBackdate(t *testing.T, ctx context.Context, m linkMachine, offset time.Duration) {
	t.Helper()
	mustSSH(t, ctx, fmt.Sprintf(`f=%[1]s/%[2]s
sudo -n test -f "$f" || { echo "$f is missing" >&2; exit 1; }
at=$(awk -v now="$(date +%%s.%%N)" 'BEGIN { printf "%%.3f", now + %.3[3]f }')
sudo -n touch -c -m -d "@$at" "$f"`, linkActivity, m.id, offset.Seconds()))
}

func linkJournal(t *testing.T, ctx context.Context, since time.Time) string {
	t.Helper()
	return mustSSH(t, ctx, fmt.Sprintf("journalctl -u hibd-proxy --since @%d --no-pager -o cat", since.Unix()))
}

// linkHookRight reports whether the host's nat hook is the one Keep keeps with links on: the
// jump at the top of PREROUTING as the only hibernaut rule there, and a chain sending link
// addresses to the link port and then 443 to the egress proxy. It returns the rules it read.
func linkHookRight(t *testing.T, ctx context.Context) (bool, string) {
	t.Helper()
	pre := mustSSH(t, ctx, "sudo -n iptables -w 5 -t nat -S PREROUTING")
	chain, err := vm.ssh(ctx, "sudo -n iptables -w 5 -t nat -S HIBERNAUT")
	raw := pre + "\n" + chain
	if err != nil {
		return false, raw + "\n" + err.Error()
	}
	var rules, ours, chained []string
	for line := range strings.SplitSeq(pre, "\n") {
		if strings.HasPrefix(line, "-A PREROUTING ") {
			rules = append(rules, line)
			if strings.Contains(line, "--comment hibernaut") {
				ours = append(ours, line)
			}
		}
	}
	for line := range strings.SplitSeq(chain, "\n") {
		if strings.HasPrefix(line, "-A HIBERNAUT ") {
			chained = append(chained, line)
		}
	}
	redirects := func(rule string, port int) bool {
		return strings.Contains(rule, "-j REDIRECT") && strings.HasSuffix(rule, " "+strconv.Itoa(port))
	}
	return len(ours) == 1 && rules[0] == ours[0] && strings.HasSuffix(ours[0], "-j HIBERNAUT") &&
		len(chained) == 2 &&
		strings.Contains(chained[0], "-d "+links.CIDR) && redirects(chained[0], links.Port) &&
		strings.Contains(chained[1], "--dport 443") && redirects(chained[1], 5443), raw
}

// linkFixture is the machines TestMachineToMachine's steps share. api serves 8080, 7070 and
// 9090 and publishes the first two, web and client each serve and publish 8080, all three
// with an idle time long enough not to pause on their own.
type linkFixture struct {
	parent           *testing.T
	ctx              context.Context
	api, web, client linkMachine
	// tmp is the machine the fork step makes and the rm step removes.
	tmp linkMachine
	// woke is when a link last woke each machine, for links.WakeEvery.
	woke map[string]time.Time
}

// wakeGap waits until a link may wake m again.
func (f *linkFixture) wakeGap(t *testing.T, m linkMachine) {
	t.Helper()
	if wait := time.Until(f.woke[m.id].Add(linkWakeGap)); wait > 0 {
		sleepCtx(t, f.ctx, wait)
	}
}

func (f *linkFixture) wakes(m linkMachine) { f.woke[m.id] = time.Now() }

// TestMachineToMachine checks links between machines with the host's real firewall, proxy and
// E2B: who reaches what by name, what is refused and logged, and what cuts a link.
func TestMachineToMachine(t *testing.T) {
	ensureImage(t)
	ctx := testContext(t, 50*time.Minute)
	f := &linkFixture{parent: t, ctx: ctx, woke: map[string]time.Time{}}
	f.api = linkNew(t, ctx, "1h")
	f.web = linkNew(t, ctx, "1h")
	f.client = linkNew(t, ctx, "1h")
	linkServe(t, ctx, f.api, 8080, "api", "")
	linkServe(t, ctx, f.api, 7070, "api7", "")
	linkServe(t, ctx, f.api, 9090, "api9", "")
	linkServe(t, ctx, f.web, 8080, "web", "")
	linkServe(t, ctx, f.client, 8080, "client", "")
	hib(t, ctx, "port", "add", f.api.name, "8080", "7070")
	hib(t, ctx, "port", "add", f.web.name, "8080")
	hib(t, ctx, "port", "add", f.client.name, "8080")

	for _, step := range []struct {
		name string
		run  func(t *testing.T)
	}{
		{"a published port is reachable by name", f.reachable},
		{"unpublished port is refused", f.unpublished},
		{"a paused target is woken by a link", f.pausedWoken},
		{"a machine paused by hand is not woken by a link", f.pausedByHand},
		{"reserved ports are refused", f.reserved},
		{"a self link is refused", f.selfLink},
		{"no room refuses fast", f.noRoom},
		{"an unallocated address is refused", f.unallocated},
		{"a published port on 443 is reached not intercepted", f.port443},
		{"port rm cuts an open link", f.portRMCuts},
		{"the hosts block holds only machine names", f.hostsBlock},
		{"a rewritten hosts line reaches the right machine or none", f.rewrittenHosts},
		{"a fork inherits no ports", f.forkNoPorts},
		{"hib rm closes the links and removes the entry", f.rmCloses},
		{"port rm cuts a link waiting on a wake", f.portRMDuringWake},
		{"a guard table removed by hand cuts links and comes back", f.guardRemoved},
		{"a stopped proxy fails closed", f.proxyStopped},
		{"latency", f.latency},
	} {
		t.Run(step.name, step.run)
	}
}

func (f *linkFixture) reachable(t *testing.T) {
	ctx := f.ctx
	since := linkHostNow(t, ctx)
	linkWantReply(t, ctx, f.client, f.api.host(), 8080, "api")
	linkWantReply(t, ctx, f.client, f.web.host(), 8080, "web")
	linkWantReply(t, ctx, f.web, f.api.host(), 8080, "api")
	linkLogged(t, ctx, since, "client's link to api opening", linkEvent("open", f.client, f.api, 8080))
	closed := linkLogged(t, ctx, since, "client's link to api closing", linkEvent("close", f.client, f.api, 8080))
	if closed.Reason != "" || closed.BytesIn == 0 || closed.BytesOut == 0 {
		t.Errorf("close = %+v, want bytes both ways and no reason", closed)
	}
}

func (f *linkFixture) unpublished(t *testing.T) {
	ctx := f.ctx
	if got := linkPublished(linkPorts(t, ctx)[f.api.name]); !slices.Equal(got, []int{7070, 8080}) {
		t.Fatalf("%s publishes %v, want [7070 8080]", f.api.name, got)
	}
	since := linkHostNow(t, ctx)
	linkWantNoReply(t, ctx, f.client, f.api.host(), 9090)
	linkLogged(t, ctx, since, "the refusal of api:9090", linkRefused(f.client, f.api, 9090, links.ErrNotPublished))
	if got := strings.TrimSpace(sh(t, ctx, f.api.id, linkAskCmd("127.0.0.1", 9090, "ping", 10*time.Second))); got != "reply: api9 ping" {
		t.Errorf("%s asking its own 9090 got %q, want the server there to answer", f.api.name, got)
	}
}

func (f *linkFixture) pausedWoken(t *testing.T) {
	ctx := f.ctx
	linkPause(t, ctx, f.web)
	f.wakeGap(t, f.web)
	since := linkHostNow(t, ctx)
	f.wakes(f.web)
	if got := linkAsk(t, ctx, f.client, f.web.host(), 8080, 75*time.Second); got != "reply: web ping" {
		t.Fatalf("asking paused %s got %q, want the woken server to answer", f.web.name, got)
	}
	if open := linkLogged(t, ctx, since, "the link that woke web opening", linkEvent("open", f.client, f.web, 8080)); !open.Woke {
		t.Errorf("open = %+v, want it to say it woke %s", open, f.web.name)
	}
	if got := linkState(t, ctx, f.web); got != engine.Running {
		t.Errorf("%s is %s after a link woke it, want running", f.web.name, got)
	}
}

// pausedByHand also saves the machine, which runs it for a while behind the fence that pausing
// holds. Every link that asks meanwhile must be refused, for either reason.
func (f *linkFixture) pausedByHand(t *testing.T) {
	ctx := f.ctx
	hib(t, ctx, "pause", f.api.name)
	since := linkHostNow(t, ctx)
	linkWantNoReply(t, ctx, f.client, f.api.host(), 8080)
	linkLogged(t, ctx, since, "the refusal of api, paused by hand", linkRefused(f.client, f.api, 8080, links.ErrPausedByHand))
	if got := linkState(t, ctx, f.api); got != engine.Paused {
		t.Fatalf("%s is %s after a link asked for it, want it left paused", f.api.name, got)
	}

	image := fmt.Sprintf("e2e-image-%08x", rand.Uint32())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		runHib(ctx, "image", "rm", image)
	})
	saved := make(chan string, 1)
	go func() {
		stdout, stderr, code := runHib(ctx, "image", "save", f.api.name, image)
		if code != 0 {
			saved <- fmt.Sprintf("hib image save exited %d: %s%s", code, stdout, stderr)
			return
		}
		saved <- ""
	}()
	asked := 0
	for saving := true; saving; {
		select {
		case msg := <-saved:
			if msg != "" {
				t.Fatal(msg)
			}
			saving = false
		default:
			if got := linkAsk(t, ctx, f.client, f.api.host(), 8080, 5*time.Second); strings.HasPrefix(got, "reply: ") {
				t.Errorf("while save ran %s, a link reached it: %q", f.api.name, got)
			}
			asked++
		}
	}
	t.Logf("%d links asked for %s while save ran it", asked, f.api.name)
	if got := linkState(t, ctx, f.api); got != engine.Paused {
		t.Errorf("%s is %s after the save, want it paused again", f.api.name, got)
	}
	sleepCtx(t, ctx, links.FoldWindow+2*time.Second)
	toAPI := func(e hibapi.LinkEntry) bool { return f.client.is(e.From) && f.api.is(e.To) }
	for _, e := range linkFind(t, linkLog(t, ctx), since, toAPI) {
		if e.Event != "refused" || e.Reason != links.ErrPausedByHand.Error() && e.Reason != links.ErrFenced.Error() {
			t.Errorf("while %s was paused by hand the link log has %+v, want only refusals for that or a pause in progress", f.api.name, e)
		}
	}

	sshMachine(t, ctx, f.api.name, "true")
	linkWantReply(t, ctx, f.client, f.api.host(), 8080, "api")
}

func (f *linkFixture) reserved(t *testing.T) {
	ctx := f.ctx
	since := linkHostNow(t, ctx)
	for _, port := range slices.Sorted(maps.Keys(links.Reserved)) {
		_, stderr, code := runHib(ctx, "port", "add", f.api.name, strconv.Itoa(port))
		if code == 0 || !strings.Contains(stderr, "can't be published") {
			t.Errorf("hib port add %s %d exited %d: %s, want it refused as reserved", f.api.name, port, code, stderr)
		}
		linkWantNoReply(t, ctx, f.client, f.api.host(), port)
	}
	for _, port := range slices.Sorted(maps.Keys(links.Reserved)) {
		linkLogged(t, ctx, since, fmt.Sprintf("the refusal of api:%d", port), linkRefused(f.client, f.api, port, links.ErrReservedPort))
	}
}

func (f *linkFixture) selfLink(t *testing.T) {
	ctx := f.ctx
	since := linkHostNow(t, ctx)
	linkWantNoReply(t, ctx, f.client, f.client.host(), 8080)
	linkLogged(t, ctx, since, "the refusal of client's link to itself", linkRefused(f.client, f.client, 8080, links.ErrSelfLink))
}

// noRoom fills the host until a wake would leave it less than the one default machine kept for
// its owner, then asks for a paused target.
func (f *linkFixture) noRoom(t *testing.T) {
	ctx := f.ctx
	linkPause(t, ctx, f.web)
	for fillers := 0; linkRoom(t, ctx) >= 2; fillers++ {
		if fillers == 8 {
			t.Fatalf("hib ls still has room after %d more machines", fillers)
		}
		newHibMachine(t, ctx)
	}
	f.wakeGap(t, f.web)
	since := linkHostNow(t, ctx)
	got := sshMachine(t, ctx, f.client.name, fmt.Sprintf("python3 %s timed %s 8080 ping 75", linkPy, f.web.host()))
	took, result, _ := strings.Cut(got, " ")
	secs, err := strconv.ParseFloat(took, 64)
	if err != nil {
		t.Fatalf("timed ask printed %q", got)
	}
	if strings.HasPrefix(result, "reply: ") || secs > 5 {
		t.Errorf("with no room, asking paused %s gave %q after %.1fs, want a refusal within 5s", f.web.name, result, secs)
	}
	linkLogged(t, ctx, since, "the refusal for lack of room", linkRefused(f.client, f.web, 8080, links.ErrNoRoom))
	if got := linkState(t, ctx, f.web); got != engine.Paused {
		t.Errorf("%s is %s, want it left paused for lack of room", f.web.name, got)
	}
}

func (f *linkFixture) unallocated(t *testing.T) {
	ctx := f.ctx
	since := linkHostNow(t, ctx)
	linkWantNoReply(t, ctx, f.client, linkUnallocated, 8080)
	linkLogged(t, ctx, since, "the refusal of an unallocated address", linkRefused(f.client, linkNobody, 8080, links.ErrUnknownAddress))
}

func (f *linkFixture) port443(t *testing.T) {
	ctx := f.ctx
	linkServe(t, ctx, f.api, 443, "api443", "")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		runHib(ctx, "port", "rm", f.api.name, "443")
	})
	hib(t, ctx, "port", "add", f.api.name, "443")
	since := linkHostNow(t, ctx)
	linkWantReply(t, ctx, f.client, f.api.host(), 443, "api443")
	linkLogged(t, ctx, since, "the link to api:443 opening", linkEvent("open", f.client, f.api, 443))
}

func (f *linkFixture) portRMCuts(t *testing.T) {
	ctx := f.ctx
	hold := linkHold(t, ctx, f.client, false, f.api.host(), 7070, 600, 0)
	linkHoldOpen(t, ctx, f.client, false, hold, "api7 hello")
	if open := linkOpen(linkPorts(t, ctx)[f.api.name], 7070); open < 1 {
		t.Errorf("hib port ls says %d links are open to %s:7070, want at least the held one", open, f.api.name)
	}
	since := linkHostNow(t, ctx)
	hib(t, ctx, "port", "rm", f.api.name, "7070")
	if end := linkHoldEnd(t, ctx, f.client, false, hold, time.Minute); !strings.HasPrefix(end, "cut ") {
		t.Errorf("the held link ended with %q, want port rm to cut it", end)
	}
	if closed := linkLogged(t, ctx, since, "the held link closing", linkEvent("close", f.client, f.api, 7070)); closed.Reason != links.ErrRevoked.Error() {
		t.Errorf("close = %+v, want the reason %q", closed, links.ErrRevoked)
	}
	linkWantReply(t, ctx, f.client, f.api.host(), 8080, "api")
}

func (f *linkFixture) hostsBlock(t *testing.T) {
	ctx := f.ctx
	const own = "10.9.9.9 own-line.example"
	sshMachine(t, ctx, f.client.name, fmt.Sprintf("echo '%s' | sudo -n tee -a /etc/hosts >/dev/null", own))
	// Publishing a port pushes the block to every running machine.
	hib(t, ctx, "port", "add", f.web.name, "9090")
	hib(t, ctx, "port", "rm", f.web.name, "9090")
	block, outside := linkHosts(t, ctx, f.client)
	ports := linkPorts(t, ctx)
	for _, m := range []linkMachine{f.api, f.web, f.client} {
		if _, ok := block[m.name]; !ok {
			t.Errorf("%s's hosts block has no %s", f.client.name, m.host())
		}
	}
	for name, addr := range block {
		if want := ports[name].Addr; addr != want {
			t.Errorf("%s's hosts block gives %s.hib %s, want %q from hib port ls", f.client.name, name, addr, want)
		}
	}
	if !slices.Contains(outside, own) {
		t.Errorf("%s's own line %q didn't survive hibd writing the block:\n%s", f.client.name, own, strings.Join(outside, "\n"))
	}
}

// rewrittenHosts edits the client's hosts block as a process in it could: the link goes where
// the address says, or nowhere.
func (f *linkFixture) rewrittenHosts(t *testing.T) {
	ctx := f.ctx
	ports := linkPorts(t, ctx)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		linkSetHost(t, ctx, f.client, f.web.name, ports[f.web.name].Addr)
	})
	linkSetHost(t, ctx, f.client, f.web.name, ports[f.api.name].Addr)
	linkWantReply(t, ctx, f.client, f.web.host(), 8080, "api")
	since := linkHostNow(t, ctx)
	linkSetHost(t, ctx, f.client, f.web.name, "198.19.255.253")
	linkWantNoReply(t, ctx, f.client, f.web.host(), 8080)
	linkLogged(t, ctx, since, "the refusal of a made-up address", linkRefused(f.client, linkNobody, 8080, links.ErrUnknownAddress))
}

func (f *linkFixture) forkNoPorts(t *testing.T) {
	ctx := f.ctx
	f.tmp = linkNewFor(t, f.parent, ctx, "1h")
	linkServe(t, ctx, f.tmp, 8080, "tmp", "")
	hib(t, ctx, "port", "add", f.tmp.name, "8080")
	copyName := f.tmp.name + "-1"
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		runHib(ctx, "rm", copyName, "--yes")
	})
	stdout, stderr, code := runHib(ctx, "fork", f.tmp.name, "--json")
	if code != 0 {
		t.Fatalf("hib fork %s exited %d: %s%s", f.tmp.name, code, stdout, stderr)
	}
	var r hibapi.Response
	if err := json.Unmarshal([]byte(stdout), &r); err != nil || len(r.Machines) != 1 {
		t.Fatalf("hib fork --json printed %s (%v), want one copy", stdout, err)
	}
	copyName = r.Machines[0].Name
	if !strings.Contains(stderr, "copies don't inherit published ports") {
		t.Errorf("hib fork said nothing about published ports: %q", stderr)
	}
	if p, ok := linkPorts(t, ctx)[copyName]; ok {
		t.Errorf("the copy %s publishes %v, want nothing", copyName, linkPublished(p))
	}
	linkWantNoReply(t, ctx, f.client, copyName+".hib", 8080)
}

func (f *linkFixture) rmCloses(t *testing.T) {
	ctx := f.ctx
	if f.tmp.id == "" {
		t.Skip("needs the machine the fork step makes")
	}
	hold := linkHold(t, ctx, f.client, false, f.tmp.host(), 8080, 600, 0)
	linkHoldOpen(t, ctx, f.client, false, hold, "tmp hello")
	since := linkHostNow(t, ctx)
	hib(t, ctx, "rm", f.tmp.name, "--yes")
	if end := linkHoldEnd(t, ctx, f.client, false, hold, time.Minute); !strings.HasPrefix(end, "cut ") {
		t.Errorf("the held link ended with %q, want hib rm to cut it", end)
	}
	linkLogged(t, ctx, since, "the link to the removed machine closing", linkEvent("close", f.client, f.tmp, 8080))
	if p, ok := linkPorts(t, ctx)[f.tmp.name]; ok {
		t.Errorf("hib port ls still lists %s after hib rm: %+v", f.tmp.name, p)
	}
	// hib rm pushes nothing. The daemon's next delivery pass rewrites the other machines' block.
	waitFor(t, ctx, f.tmp.host()+" to leave "+f.client.name+"'s hosts block", time.Minute, func() bool {
		block, _ := linkHosts(t, ctx, f.client)
		_, ok := block[f.tmp.name]
		return !ok
	})
}

// portRMDuringWake lands port rm while a link waits for its target to wake. When it lands is
// a guess at how long the wake takes, adjusted after each try: an open link means it came too
// late, and a refusal for an unpublished port too early.
func (f *linkFixture) portRMDuringWake(t *testing.T) {
	ctx := f.ctx
	republish := func(t *testing.T, ctx context.Context) {
		if !slices.Contains(linkPublished(linkPorts(t, ctx)[f.web.name]), 8080) {
			hib(t, ctx, "port", "add", f.web.name, "8080")
		}
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		republish(t, ctx)
	})
	delay, step := 1500*time.Millisecond, 400*time.Millisecond
	for attempt := 1; attempt <= 6; attempt++ {
		republish(t, ctx)
		linkPause(t, ctx, f.web)
		f.wakeGap(t, f.web)
		since := linkHostNow(t, ctx)
		f.wakes(f.web)
		asked := make(chan string, 1)
		go func() {
			out, stderr, err := sshResult(ctx, nil, f.client.name, linkAskCmd(f.web.host(), 8080, "ping", 75*time.Second))
			if err != nil {
				out = fmt.Sprintf("ssh failed: %v: %s", err, stderr)
			}
			asked <- out
		}()
		sleepCtx(t, ctx, delay)
		hib(t, ctx, "port", "rm", f.web.name, "8080")
		got := <-asked
		end := linkLogged(t, ctx, since, "how the waiting link ended", func(e hibapi.LinkEntry) bool {
			return (e.Event == "open" || e.Event == "refused") && f.client.is(e.From) && f.web.is(e.To) && e.Port == 8080
		})
		switch {
		case end.Event == "refused" && end.Reason == links.ErrRevoked.Error():
			t.Logf("try %d: port rm %v after the ask cut the link waiting on the wake (%s)", attempt, delay, got)
			return
		case end.Event == "open" || strings.HasPrefix(got, "reply: "):
			t.Logf("try %d: port rm %v after the ask came after the link opened", attempt, delay)
			delay -= step
		case end.Reason == links.ErrNotPublished.Error():
			t.Logf("try %d: port rm %v after the ask came before the link was admitted", attempt, delay)
			delay += step
		default:
			t.Logf("try %d: port rm %v after the ask, the link log has %+v and the ask %q", attempt, delay, end, got)
		}
		delay = max(delay, 100*time.Millisecond)
	}
	t.Skip("port rm didn't land inside a wake in 6 tries, so this run can't say whether it cuts the waiting link")
}

func (f *linkFixture) guardRemoved(t *testing.T) {
	ctx := f.ctx
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		// A proxy that didn't put the guard back serves no links until it restarts.
		cmd := "sudo -n nft list table inet " + links.GuardTable + " >/dev/null || sudo -n systemctl restart hibd-proxy"
		if _, err := vm.ssh(ctx, cmd); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	hold := linkHold(t, ctx, f.client, false, f.api.host(), 8080, 600, 5)
	linkHoldOpen(t, ctx, f.client, false, hold, "api hello")
	since := linkHostNow(t, ctx)
	mustSSH(t, ctx, "sudo -n nft delete table inet "+links.GuardTable)
	if end := linkHoldEnd(t, ctx, f.client, false, hold, time.Minute); !strings.HasPrefix(end, "cut ") {
		t.Errorf("the held link ended with %q, want the guard's removal to cut it", end)
	}
	waitFor(t, ctx, "hibd proxy to say the guard is down and put it back", 2*time.Minute, func() bool {
		journal := linkJournal(t, ctx, since)
		down := strings.Index(journal, "linkguard: the guard is down")
		return down >= 0 && strings.Contains(journal[down:], "linkguard: reinstalled the guard")
	})
	mustSSH(t, ctx, "sudo -n nft list table inet "+links.GuardTable+" >/dev/null")
	linkWantReply(t, ctx, f.client, f.api.host(), 8080, "api")
}

func (f *linkFixture) proxyStopped(t *testing.T) {
	ctx := f.ctx
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if _, err := vm.ssh(ctx, "sudo -n systemctl start hibd-proxy"); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	mustSSH(t, ctx, "sudo -n systemctl stop hibd-proxy")
	linkWantNoReply(t, ctx, f.client, f.api.host(), 8080)
	since := linkHostNow(t, ctx)
	mustSSH(t, ctx, "sudo -n systemctl start hibd-proxy")
	waitFor(t, ctx, "hibd proxy to serve links again", 3*time.Minute, func() bool {
		return strings.Contains(linkJournal(t, ctx, since), fmt.Sprintf("serving machine links on port %d", links.Port))
	})
	waitFor(t, ctx, "the nat hook with links on", time.Minute, func() bool {
		ok, _ := linkHookRight(t, ctx)
		return ok
	})
	linkWantReply(t, ctx, f.client, f.api.host(), 8080, "api")
}

// latency logs what a link costs: connection setup and warm round trips over a link, the same
// inside the target without one, and a wake. The numbers go in the milestone's Changes.
func (f *linkFixture) latency(t *testing.T) {
	ctx := f.ctx
	t.Logf("over a link:\n%s", sshMachine(t, ctx, f.client.name, fmt.Sprintf("python3 %s bench %s 8080 20 1000", linkPy, f.api.host())))
	t.Logf("inside the target, without a link:\n%s", sshMachine(t, ctx, f.api.name, fmt.Sprintf("python3 %s bench 127.0.0.1 8080 20 1000", linkPy)))
	linkPause(t, ctx, f.web)
	f.wakeGap(t, f.web)
	f.wakes(f.web)
	got := sshMachine(t, ctx, f.client.name, fmt.Sprintf("python3 %s timed %s 8080 ping 60", linkPy, f.web.host()))
	took, result, _ := strings.Cut(got, " ")
	if result != "reply: web ping" {
		t.Fatalf("asking paused %s gave %q", f.web.name, got)
	}
	t.Logf("a link woke %s and got its reply in %ss", f.web.name, took)
}

// TestMachineToMachineSlow waits out idle timeouts and link leases: links keep a machine awake
// only on behalf of a machine someone is using.
func TestMachineToMachineSlow(t *testing.T) {
	if os.Getenv("HIB_E2E_SLOW") == "" {
		t.Skip("waits out idle timeouts and link leases; run make e2e-slow")
	}
	ensureImage(t)
	t.Run("an idle link doesn't hold a machine awake", linkIdleLink)
	t.Run("rings of machines stop and stay stopped", linkRings)
	t.Run("own activity", func(t *testing.T) {
		t.Run("an open ssh session keeps its links going", linkSessionKeeps)
		t.Run("a finite-idle machine pauses within the bound", linkFiniteIdle)
	})
	t.Run("a target resumed on another slot is not misrouted", linkResumedElsewhere)
	t.Run("no byte reaches a target E2B paused", linkNoByteAfterPause)
	t.Run("a wake that outlives the lease pauses the target again", linkWakeOutlivesLease)
	t.Run("an interrupted chain migration is repaired", linkChainRepaired)
}

// linkPair makes a source with an idle time of sourceIdle and a target with targetIdle that
// serves and publishes 8080, tagging its answers "t".
func linkPair(t *testing.T, ctx context.Context, sourceIdle, targetIdle string) (source, target linkMachine) {
	t.Helper()
	source = linkNew(t, ctx, sourceIdle)
	target = linkNew(t, ctx, targetIdle)
	linkServe(t, ctx, target, 8080, "t", "")
	hib(t, ctx, "port", "add", target.name, "8080")
	return source, target
}

// linkLoop starts asking host:8080 from m every 5 seconds for 25 minutes, over ssh, and returns
// the file in m it writes each answer to.
func linkLoop(t *testing.T, ctx context.Context, m linkMachine, host string) string {
	t.Helper()
	out := fmt.Sprintf("%s/loop-%08x.out", linkHome, rand.Uint32())
	sshMachine(t, ctx, m.name, fmt.Sprintf("setsid -f python3 %s loop %s 8080 5 1500 %s </dev/null >/dev/null 2>&1", linkPy, host, out))
	return out
}

// linkWaitPaused waits for every one of ms to be paused at once.
func linkWaitPaused(t *testing.T, ctx context.Context, what string, limit time.Duration, ms ...linkMachine) {
	t.Helper()
	waitFor(t, ctx, what, limit, func() bool {
		for _, m := range ms {
			if linkState(t, ctx, m) != engine.Paused {
				return false
			}
		}
		return true
	})
}

func linkIdleLink(t *testing.T) {
	ctx := testContext(t, 30*time.Minute)
	source, target := linkPair(t, ctx, "1h", "2m")
	hold := linkHold(t, ctx, source, false, target.host(), 8080, 1200, 0)
	linkHoldOpen(t, ctx, source, true, hold, "t hello")
	since := linkHostNow(t, ctx)
	linkWaitState(t, ctx, target, engine.Paused, links.LinkGrace+linkIdle+linkSlack)
	if end := linkHoldEnd(t, ctx, source, true, hold, time.Minute); !strings.HasPrefix(end, "cut ") {
		t.Errorf("the idle link ended with %q, want the target's pause to cut it", end)
	}
	if closed := linkLogged(t, ctx, since, "the idle link closing", linkEvent("close", source, target, 8080)); closed.Reason != links.ErrRunEnded.Error() {
		t.Errorf("close = %+v, want the reason %q", closed, links.ErrRunEnded)
	}
	linkStaysPaused(t, ctx, 3*time.Minute, target)
}

// linkRings runs a ring of two machines and one of three, each asking the next for as long as
// it runs. Nobody uses any of them, so they all pause once their own activity is
// links.LinkGrace old and an idle timeout has passed, and none wakes another.
func linkRings(t *testing.T) {
	ctx := testContext(t, 45*time.Minute)
	partner := []int{1, 0, 3, 4, 2}
	ring := make([]linkMachine, len(partner))
	for i := range ring {
		ring[i] = linkNew(t, ctx, "2m")
	}
	for i, m := range ring {
		linkServe(t, ctx, m, 8080, fmt.Sprintf("ring%d", i), "")
		hib(t, ctx, "port", "add", m.name, "8080")
	}
	outs := make([]string, len(ring))
	for i, m := range ring {
		outs[i] = linkLoop(t, ctx, m, ring[partner[i]].host())
	}
	reached := make([]bool, len(ring))
	waitFor(t, ctx, "every machine in the rings to reach the next", 2*time.Minute, func() bool {
		for i, m := range ring {
			lines, _ := linkTryLines(ctx, m, outs[i])
			want := fmt.Sprintf(" reply: ring%d ping", partner[i])
			reached[i] = reached[i] || slices.ContainsFunc(lines, func(l string) bool { return strings.HasSuffix(l, want) })
		}
		return !slices.Contains(reached, false)
	})
	linkWaitPaused(t, ctx, "every machine in the rings to pause", links.LinkGrace+linkIdle+linkSlack, ring...)
	stopped := linkHostNow(t, ctx)
	linkStaysPaused(t, ctx, 4*time.Minute, ring...)
	inRing := func(e hibapi.LinkEntry) bool {
		return e.Event == "open" && e.Woke && slices.ContainsFunc(ring, func(m linkMachine) bool { return m.is(e.To) })
	}
	for _, e := range linkFind(t, linkLog(t, ctx), stopped, inRing) {
		t.Errorf("a link woke a machine in the rings after they stopped: %+v", e)
	}
}

// linkSessionKeeps holds an ssh session open into the source past links.LinkGrace and the
// target's idle time: the source's links keep the target running until the session ends.
func linkSessionKeeps(t *testing.T) {
	t.Parallel()
	ctx := testContext(t, 40*time.Minute)
	source, target := linkPair(t, ctx, "2m", "2m")
	session := sshMachineCmd(ctx, nil, source.name, "sleep 3600")
	if err := session.Start(); err != nil {
		t.Fatalf("ssh %s: %v", source.host(), err)
	}
	ended := false
	end := func() {
		if !ended {
			ended = true
			session.Process.Kill()
			session.Wait()
		}
	}
	t.Cleanup(end)
	out := linkLoop(t, ctx, source, target.host())
	sleepCtx(t, ctx, links.LinkGrace+linkIdle+2*time.Minute)
	if got := linkState(t, ctx, target); got != engine.Running {
		t.Fatalf("%s is %s while %s has an ssh session open, want running", target.name, got, source.name)
	}
	lines, _ := linkTryLines(ctx, source, out)
	if len(lines) < 6 {
		t.Fatalf("the loop in %s wrote %d lines, want one every 5 seconds", source.name, len(lines))
	}
	for _, line := range lines[len(lines)-6:] {
		if !strings.HasSuffix(line, " reply: t ping") {
			t.Errorf("with the session open the loop got %q, want answers", line)
		}
	}
	end()
	linkWaitPaused(t, ctx, "both to pause once the session ended", links.LinkGrace+linkIdle+linkSlack, source, target)
}

func linkFiniteIdle(t *testing.T) {
	t.Parallel()
	ctx := testContext(t, 30*time.Minute)
	source, target := linkPair(t, ctx, "2m", "2m")
	out := linkLoop(t, ctx, source, target.host())
	waitFor(t, ctx, "the loop to get an answer", 2*time.Minute, func() bool {
		lines, _ := linkTryLines(ctx, source, out)
		return slices.ContainsFunc(lines, func(l string) bool { return strings.HasSuffix(l, " reply: t ping") })
	})
	linkWaitPaused(t, ctx, "both to pause", links.LinkGrace+linkIdle+linkSlack, source, target)
}

// linkResumedElsewhere pauses a target under an open link and makes another machine, which
// may get the target's old slot, before the target resumes, possibly on a new one. Neither
// the old link nor a new one reaches the wrong machine.
func linkResumedElsewhere(t *testing.T) {
	ctx := testContext(t, 30*time.Minute)
	source, target := linkPair(t, ctx, "1h", "1h")
	hold := linkHold(t, ctx, source, false, target.host(), 8080, 900, 2)
	linkHoldOpen(t, ctx, source, true, hold, "t hello")
	linkPause(t, ctx, target)
	if end := linkHoldEnd(t, ctx, source, true, hold, 2*time.Minute); !strings.HasPrefix(end, "cut ") {
		t.Errorf("the held link ended with %q, want the target's pause to cut it", end)
	}
	x := linkNew(t, ctx, "1h")
	linkServe(t, ctx, x, 8080, "x", "")
	hib(t, ctx, "port", "add", x.name, "8080")
	sshMachine(t, ctx, target.name, "true")
	linkWantReply(t, ctx, source, target.host(), 8080, "t")
	linkWantReply(t, ctx, source, x.host(), 8080, "x")
	for _, line := range linkLines(t, ctx, source, true, hold) {
		if strings.HasPrefix(line, "reply ") && !strings.HasPrefix(line, "reply t beat ") {
			t.Errorf("the held link got %q, want only the target's answers", line)
		}
	}
}

// linkNoByteAfterPause sends a beat a second over a link and lets the target pause on its own
// idle timeout, by putting the source's own activity out of the link grace so the link stops
// renewing it. Nothing sent reaches the target after its run ends, then or after it resumes.
func linkNoByteAfterPause(t *testing.T) {
	ctx := testContext(t, 30*time.Minute)
	source := linkNew(t, ctx, "1h")
	target := linkNew(t, ctx, "2m")
	served := linkHome + "/served.log"
	linkServe(t, ctx, target, 8080, "t", served)
	hib(t, ctx, "port", "add", target.name, "8080")
	hold := linkHold(t, ctx, source, false, target.host(), 8080, 1200, 1)
	linkHoldOpen(t, ctx, source, true, hold, "t hello")
	// Let the ssh session that started the hold finish touching the source first.
	sleepCtx(t, ctx, 10*time.Second)
	linkBackdate(t, ctx, source, -(links.LinkGrace + time.Minute))
	since := linkHostNow(t, ctx)
	linkWaitState(t, ctx, target, engine.Paused, links.LinkRenew+linkIdle+linkSlack)
	end := linkHoldEnd(t, ctx, source, true, hold, 2*time.Minute)
	if !strings.HasPrefix(end, "cut ") {
		t.Fatalf("the held link ended with %q, want the target's pause to cut it", end)
	}
	fields := strings.Fields(end)
	sent, err := strconv.Atoi(fields[len(fields)-1])
	if err != nil {
		t.Fatalf("the held link ended with %q, want a count of beats sent", end)
	}
	if closed := linkLogged(t, ctx, since, "the link closing", linkEvent("close", source, target, 8080)); closed.Reason != links.ErrRunEnded.Error() {
		t.Errorf("close = %+v, want the reason %q", closed, links.ErrRunEnded)
	}

	beats := func() (count, last int) {
		for line := range strings.SplitSeq(sshMachine(t, ctx, target.name, "cat "+served), "\n") {
			count++
			if _, beat, ok := strings.Cut(line, " beat "); ok {
				n, err := strconv.Atoi(beat)
				if err != nil {
					t.Fatalf("%s's server logged %q", target.name, line)
				}
				last = max(last, n)
			}
		}
		return count, last
	}
	count, last := beats()
	if last > sent {
		t.Errorf("%s got beat %d, but the source sent only %d", target.name, last, sent)
	}
	sleepCtx(t, ctx, 2*links.GuardTTL+10*time.Second)
	if later, _ := beats(); later != count {
		t.Errorf("%s's server got %d more lines after it resumed, want none", target.name, later-count)
	}
}

// linkWakeOutlivesLease puts the end of the source's lease just after a link asks for a paused
// target, so the wake finishes after it. The target is paused again rather than left to run
// out its idle time. When the lease ends is a guess at how long the ask and the wake take,
// adjusted after each try.
func linkWakeOutlivesLease(t *testing.T) {
	ctx := testContext(t, 30*time.Minute)
	source, target := linkPair(t, ctx, "1h", "1h")
	var woke time.Time
	var low, high time.Duration
	margin, step := time.Second, 750*time.Millisecond
	for attempt := 1; attempt <= 6; attempt++ {
		linkPause(t, ctx, target)
		if wait := time.Until(woke.Add(linkWakeGap)); wait > 0 {
			sleepCtx(t, ctx, wait)
		}
		linkBackdate(t, ctx, target, -time.Hour)
		since := linkHostNow(t, ctx)
		linkBackdate(t, ctx, source, margin-links.LinkGrace)
		got := strings.TrimSpace(sh(t, ctx, source.id, linkAskCmd(target.host(), 8080, "ping", 75*time.Second)))
		if strings.HasPrefix(got, "reply: ") {
			woke = time.Now()
			t.Logf("try %d: the lease outlived the wake with %v left", attempt, margin)
			high = margin
		} else {
			refused := linkLogged(t, ctx, since, "the late wake's refusal", func(e hibapi.LinkEntry) bool {
				return e.Event == "refused" && source.is(e.From) && target.is(e.To) && e.Port == 8080
			})
			switch refused.Reason {
			case links.ErrLeaseExpired.Error():
				t.Logf("try %d: the lease ran out during the wake with %v left when it was set", attempt, margin)
				linkWaitState(t, ctx, target, engine.Paused, 2*time.Minute)
				linkStaysPaused(t, ctx, 2*time.Minute, target)
				return
			case links.ErrSourceIdle.Error():
				t.Logf("try %d: the lease ran out before the ask with %v left", attempt, margin)
				low = margin
			default:
				t.Logf("try %d: refused for %q", attempt, refused.Reason)
				woke = time.Now()
				continue
			}
		}
		switch {
		case low > 0 && high > 0:
			margin = (low + high) / 2
		case high > 0:
			margin = max(margin-step, 100*time.Millisecond)
		default:
			margin += step
		}
	}
	t.Skip("the lease didn't end inside a wake in 6 tries, so this run can't say whether the target is paused again")
}

// linkChainRepaired leaves the nat hook as an interrupted migration from the single 443 rule
// to the chain would: the jump in place, the chain missing its link rule and the old rule back.
func linkChainRepaired(t *testing.T) {
	ctx := testContext(t, 20*time.Minute)
	source, target := linkPair(t, ctx, "1h", "1h")
	linkWantReply(t, ctx, source, target.host(), 8080, "t")
	if ok, raw := linkHookRight(t, ctx); !ok {
		t.Fatalf("the nat hook isn't the one Keep keeps before the test breaks it:\n%s", raw)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if ok, _ := linkHookRight(t, ctx); !ok {
			if _, err := vm.ssh(ctx, "sudo -n systemctl restart hibd-proxy"); err != nil {
				t.Errorf("cleanup: %v", err)
			}
		}
	})
	since := linkHostNow(t, ctx)
	mustSSH(t, ctx, fmt.Sprintf(`n=$(sudo -n iptables -w 5 -t nat -L HIBERNAUT -n --line-numbers | awk '/redir ports %[1]d[[:space:]]*$/ { print $1; exit }')
[ -n "$n" ] || { echo "HIBERNAUT has no rule to port %[1]d" >&2; exit 1; }
sudo -n iptables -w 5 -t nat -D HIBERNAUT "$n"
sudo -n iptables -w 5 -t nat -A PREROUTING -i veth-+ -p tcp --dport 443 -m comment --comment hibernaut -j REDIRECT --to-ports 5443`, links.Port))
	waitFor(t, ctx, "Keep to repair the nat hook", 3*time.Minute, func() bool {
		ok, _ := linkHookRight(t, ctx)
		return ok
	})
	if journal := linkJournal(t, ctx, since); !strings.Contains(journal, "repaired the hibernaut iptables hook") {
		t.Errorf("hibd proxy repaired the hook without saying so:\n%s", journal)
	}
	linkWantReply(t, ctx, source, target.host(), 8080, "t")
}
