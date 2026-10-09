//go:build linux

package linkguard

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// fwmarkAccept is the setting that makes an accepted socket take its SYN's mark. Under /proc
// it reads and writes the namespace of the thread that opens it.
const fwmarkAcceptPath = "/proc/sys/net/ipv4/tcp_fwmark_accept"

// nlTimeout bounds every netlink send and receive, so a kernel that never answers is an error
// rather than a Keeper stuck for good.
const nlTimeout = 5 * time.Second

// dumpTries bounds the re-reads of a dump the kernel marks inconsistent.
const dumpTries = 5

// netlinkBackend speaks nfnetlink to the kernel, one socket per operation.
type netlinkBackend struct {
	// netns, when set, is a network namespace file to work in instead of the caller's. Tests
	// use it to keep the guard out of the host's own namespace.
	netns string
}

func newBackend(netns string) backend { return &netlinkBackend{netns: netns} }

func (b *netlinkBackend) replace(r ruleset) error {
	return b.do(func(c *conn) error { return c.batch(r.replace()) })
}

func (b *netlinkBackend) exists(table string) (bool, error) {
	var ok bool
	err := b.do(func(c *conn) error {
		var err error
		_, ok, err = c.table(table)
		return err
	})
	return ok, err
}

func (b *netlinkBackend) dump(table string) (tableDump, bool, error) {
	var (
		d  tableDump
		ok bool
	)
	err := b.do(func(c *conn) error {
		var err error
		if d.table, ok, err = c.table(table); err != nil || !ok {
			return err
		}
		reads := []struct {
			typ, tableAttr uint16
			filter         bool
			into           *[][]byte
		}{
			{nftMsgGetSet, nftaSetTable, true, &d.sets},
			{nftMsgGetChain, nftaChainTable, false, &d.chains},
			{nftMsgGetRule, nftaRuleTable, true, &d.rules},
		}
		for _, r := range reads {
			req := message{typ: r.typ, family: nfprotoINET}
			if r.filter {
				req.attrs = []attr{aStr(r.tableAttr, table)}
			}
			msgs, err := c.dump(req)
			if errors.Is(err, syscall.ENOENT) {
				ok = false
				return nil
			}
			if err != nil {
				return err
			}
			// The kernel filters some dumps by table and not others, so filter all of them.
			for _, m := range msgs {
				if attrString(m.attrs(), r.tableAttr) == table {
					*r.into = append(*r.into, m.attrs())
				}
			}
		}
		return nil
	})
	return d, ok, err
}

func (b *netlinkBackend) deleteTable(table string) error {
	return b.do(func(c *conn) error {
		return c.batch([]message{{typ: nftMsgDelTable, family: nfprotoINET, attrs: []attr{aStr(nftaTableName, table)}}})
	})
}

func (b *netlinkBackend) batch(table string, ops []elemOp) error {
	msgs := make([]message, len(ops))
	for i, op := range ops {
		msgs[i] = elemMessage(table, op)
	}
	return b.do(func(c *conn) error { return c.batch(msgs) })
}

func (b *netlinkBackend) list(table, set string) ([]element, error) {
	var out []element
	err := b.do(func(c *conn) error {
		msgs, err := c.dump(message{typ: nftMsgGetSetElem, family: nfprotoINET, attrs: []attr{
			aStr(nftaSetElemListTable, table),
			aStr(nftaSetElemListSet, set),
		}})
		if err != nil {
			return err
		}
		for _, m := range msgs {
			elems, err := parseElements(m.attrs())
			if err != nil {
				return err
			}
			out = append(out, elems...)
		}
		return nil
	})
	return out, err
}

func (b *netlinkBackend) get(table, set string, key []byte) (element, error) {
	var e element
	err := b.do(func(c *conn) error {
		m, err := c.get(message{typ: nftMsgGetSetElem, family: nfprotoINET, attrs: []attr{
			aStr(nftaSetElemListTable, table),
			aStr(nftaSetElemListSet, set),
			aNest(nftaSetElemListElements, aNest(nftaListElem, aNest(nftaSetElemKey, aBytes(nftaDataValue, key)))),
		}})
		if err != nil {
			return err
		}
		elems, err := parseElements(m.attrs())
		if err != nil {
			return err
		}
		if len(elems) != 1 || !bytes.Equal(elems[0].key, key) {
			return fmt.Errorf("asked for one element and the kernel answered with %d", len(elems))
		}
		e = elems[0]
		return nil
	})
	return e, err
}

func (b *netlinkBackend) fwmarkAccept() (bool, error) {
	var on bool
	err := b.inNetns(func() error {
		v, err := os.ReadFile(fwmarkAcceptPath)
		on = strings.TrimSpace(string(v)) == "1"
		return err
	})
	return on, err
}

func (b *netlinkBackend) setFwmarkAccept() error {
	return b.inNetns(func() error { return os.WriteFile(fwmarkAcceptPath, []byte("1\n"), 0o644) })
}

// do runs f over a fresh socket in the backend's namespace.
func (b *netlinkBackend) do(f func(c *conn) error) error {
	return b.inNetns(func() error {
		c, err := dial()
		if err != nil {
			return err
		}
		defer unix.Close(c.fd)
		return f(c)
	})
}

// inNetns runs f on a thread that is in the backend's namespace. A socket stays in the
// namespace it was created in, and /proc/sys/net shows the opener's, so both happen inside f.
func (b *netlinkBackend) inNetns(f func() error) error {
	if b.netns == "" {
		return f()
	}
	runtime.LockOSThread()
	self, err := os.Open("/proc/thread-self/ns/net")
	if err != nil {
		runtime.UnlockOSThread()
		return err
	}
	defer self.Close()
	target, err := os.Open(b.netns)
	if err != nil {
		runtime.UnlockOSThread()
		return err
	}
	defer target.Close()
	if err := unix.Setns(int(target.Fd()), unix.CLONE_NEWNET); err != nil {
		runtime.UnlockOSThread()
		return fmt.Errorf("enter %s: %w", b.netns, err)
	}
	ferr := f()
	if err := unix.Setns(int(self.Fd()), unix.CLONE_NEWNET); err != nil {
		// The thread stays locked, so it exits with this goroutine instead of running another
		// one in the wrong namespace.
		return errors.Join(ferr, fmt.Errorf("leave %s: %w", b.netns, err))
	}
	runtime.UnlockOSThread()
	return ferr
}

// conn is one nfnetlink socket.
type conn struct {
	fd  int
	seq uint32
	buf []byte
}

func dial() (*conn, error) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_NETFILTER)
	if err != nil {
		return nil, fmt.Errorf("open a netfilter netlink socket: %w", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("bind a netfilter netlink socket: %w", err)
	}
	// Acknowledgements without a copy of the request keep a big batch's replies small. An
	// older kernel without it sends the copies, which only costs buffer.
	_ = unix.SetsockoptInt(fd, unix.SOL_NETLINK, unix.NETLINK_CAP_ACK, 1)
	tv := unix.NsecToTimeval(nlTimeout.Nanoseconds())
	for _, opt := range []int{unix.SO_RCVTIMEO, unix.SO_SNDTIMEO} {
		if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, opt, &tv); err != nil {
			unix.Close(fd)
			return nil, fmt.Errorf("set a netlink timeout: %w", err)
		}
	}
	return &conn{fd: fd, buf: make([]byte, 1<<17)}, nil
}

func (c *conn) next() uint32 {
	c.seq++
	return c.seq
}

// fit sizes the socket's buffers for a batch of n messages encoded in size bytes: the whole
// batch goes in one send, and every message's acknowledgement waits in the receive queue until
// the kernel has processed the last one.
func (c *conn) fit(size, n int) {
	set := func(force, plain, v int) {
		if unix.SetsockoptInt(c.fd, unix.SOL_SOCKET, force, v) != nil {
			_ = unix.SetsockoptInt(c.fd, unix.SOL_SOCKET, plain, v)
		}
	}
	set(unix.SO_SNDBUFFORCE, unix.SO_SNDBUF, size+1<<16)
	set(unix.SO_RCVBUFFORCE, unix.SO_RCVBUF, n*1024+1<<16)
}

func (c *conn) send(b []byte) error {
	if err := unix.Sendto(c.fd, b, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return fmt.Errorf("send to nfnetlink: %w", err)
	}
	return nil
}

func (c *conn) recv() ([]nlmsg, error) {
	for {
		n, _, flags, _, err := unix.Recvmsg(c.fd, c.buf, nil, 0)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read from nfnetlink: %w", err)
		}
		if flags&unix.MSG_TRUNC != 0 {
			return nil, errors.New("read from nfnetlink: message truncated")
		}
		return parseMessages(bytes.Clone(c.buf[:n]))
	}
}

// batch sends msgs as one transaction and waits for every acknowledgement. The kernel
// processes the whole batch before it reports anything, commits it only if every message
// succeeded, and reports a failed commit against the batch's first message. It returns the
// first error, wrapping the kernel's errno.
func (c *conn) batch(msgs []message) error {
	begin := c.seq + 1
	first, end := begin+1, begin+1+uint32(len(msgs))
	c.seq = end
	b := encodeBatch(msgs, begin)
	c.fit(len(b), len(msgs))
	if err := c.send(b); err != nil {
		return err
	}
	var failed error
	for acked := 0; acked < len(msgs); {
		rs, err := c.recv()
		if err != nil {
			return err
		}
		for _, r := range rs {
			if r.typ != nlmsgError {
				continue
			}
			if r.seq == begin || r.seq == end {
				if e := r.errno(); e != 0 {
					return fmt.Errorf("nftables transaction: %w", syscall.Errno(e))
				}
				continue
			}
			if r.seq < first || r.seq >= end {
				continue
			}
			acked++
			if e := r.errno(); e != 0 && failed == nil {
				failed = fmt.Errorf("nftables transaction, message %d of %d: %w", r.seq-first+1, len(msgs), syscall.Errno(e))
			}
		}
	}
	return failed
}

// dump reads every object req asks for. It reads again when the kernel says the dump crossed
// a change.
func (c *conn) dump(req message) ([]nlmsg, error) {
	req.flags |= nlmFDump
	for range dumpTries {
		seq := c.next()
		if err := c.send(req.appendTo(nil, seq)); err != nil {
			return nil, err
		}
		var (
			out  []nlmsg
			intr bool
		)
	read:
		for {
			rs, err := c.recv()
			if err != nil {
				return nil, err
			}
			for _, r := range rs {
				if r.seq != seq {
					continue
				}
				intr = intr || r.flags&nlmFDumpIntr != 0
				switch r.typ {
				case nlmsgDone, nlmsgError:
					if e := r.errno(); e != 0 {
						return nil, fmt.Errorf("nftables dump: %w", syscall.Errno(e))
					}
					break read
				default:
					out = append(out, r)
				}
			}
		}
		if !intr {
			return out, nil
		}
	}
	return nil, fmt.Errorf("nftables dump: changed under every one of %d reads", dumpTries)
}

// get sends one request that is answered with one object.
func (c *conn) get(req message) (nlmsg, error) {
	seq := c.next()
	if err := c.send(req.appendTo(nil, seq)); err != nil {
		return nlmsg{}, err
	}
	for {
		rs, err := c.recv()
		if err != nil {
			return nlmsg{}, err
		}
		for _, r := range rs {
			if r.seq != seq {
				continue
			}
			if r.typ == nlmsgError {
				if e := r.errno(); e != 0 {
					return nlmsg{}, fmt.Errorf("nftables get: %w", syscall.Errno(e))
				}
				continue
			}
			return r, nil
		}
	}
}

// table reads the table's own attributes. ok is false when there is no such table.
func (c *conn) table(name string) ([]byte, bool, error) {
	m, err := c.get(message{typ: nftMsgGetTable, family: nfprotoINET, attrs: []attr{aStr(nftaTableName, name)}})
	if errors.Is(err, syscall.ENOENT) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return m.attrs(), true, nil
}
