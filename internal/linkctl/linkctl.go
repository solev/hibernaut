// Package linkctl is the client for the link control socket.
//
// hibd's port, pause, rm, save, fork and cleanup paths all run as separate invocations from the
// one serving links, so every cancellation crosses a process boundary. This is that boundary:
// one unix socket, one request per connection, a reply that means the sockets are gone.
package linkctl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/solev/hibernaut/internal/links"
)

// Request is one control-socket call. Exactly one of the three ops is set.
type Request struct {
	// Op is "revoke", "close" or "stat".
	Op string `json:"op"`
	// ID is the machine the call is about, for revoke and close.
	ID string `json:"id,omitempty"`
	// Ports are the published ports to revoke, for revoke.
	Ports []int `json:"ports,omitempty"`
}

// Response is one reply. A reply arrives only after the work is done: for revoke and close that
// means every socket in scope is closed, not marked or scheduled.
type Response struct {
	// Closed is how many links were aborted.
	Closed int `json:"closed"`
	// Ports is open links per published port, which is what hibapi.PublishedPort.Open shows.
	// A map cannot be keyed on a pair in JSON, so it travels as a list.
	Ports []OpenPort `json:"ports,omitzero"`
	// Machines is open links by sandbox ID, counting a machine at either end.
	Machines map[string]int `json:"machines,omitzero"`
	// Err is the failure, empty when there was none.
	Err string `json:"err,omitempty"`
}

// OpenPort is how many links one published port is carrying.
type OpenPort struct {
	Target string `json:"target"`
	Port   int    `json:"port"`
	Open   int    `json:"open"`
}

// Client is a connection to the control socket. It implements links.Controller.
type Client struct {
	path    string
	timeout time.Duration
}

var _ links.Controller = (*Client)(nil)

// Dial returns a client for the socket at path, or links.CtlSocket when path is "". It does not
// connect: each call opens its own connection, so a client is safe to keep and cheap to make.
func Dial(path string) (*Client, error) {
	if path == "" {
		path = links.CtlSocket
	}
	return &Client{path: path, timeout: links.CtlTimeout}, nil
}

// Path is the socket this client talks to.
func (c *Client) Path() string { return c.path }

// Revoke aborts the links using id's listed ports. hib port rm reports success only once this
// has returned without an error: it is the one caller that never downgrades a failure to a
// warning, because an unpublished port that is still carrying bytes is the whole of the risk.
func (c *Client) Revoke(ctx context.Context, id string, ports []int) (int, error) {
	if id == "" {
		return 0, errors.New("linkctl: revoke needs a machine ID")
	}
	if len(ports) == 0 {
		return 0, errors.New("linkctl: revoke needs at least one port")
	}
	resp, err := c.call(ctx, Request{Op: "revoke", ID: id, Ports: ports})
	return resp.Closed, err
}

// Close aborts every link with id at either end. Callers pause or remove the machine only after
// it returns, or after the Guard fallback has confirmed the kernel will pass nothing more.
func (c *Client) Close(ctx context.Context, id string) (int, error) {
	if id == "" {
		return 0, errors.New("linkctl: close needs a machine ID")
	}
	resp, err := c.call(ctx, Request{Op: "close", ID: id})
	return resp.Closed, err
}

// Stat reports the open links, per published port and per machine. hib port ls uses the first
// to show why a machine is awake while it is still happening.
func (c *Client) Stat(ctx context.Context) (links.Stats, error) {
	resp, err := c.call(ctx, Request{Op: "stat"})
	if err != nil {
		return links.Stats{}, err
	}
	stats := links.Stats{Ports: map[links.Open]int{}, Machines: map[string]int{}}
	for _, p := range resp.Ports {
		stats.Ports[links.Open{Target: p.Target, Port: p.Port}] += p.Open
	}
	for id, n := range resp.Machines {
		stats.Machines[id] = n
	}
	return stats, nil
}

// call sends one request on a connection of its own and reads the one reply, all within the
// client's timeout. A reply that carries err comes back as an error, alongside whatever else it
// carried.
func (c *Client) call(ctx context.Context, req Request) (Response, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.path)
	if err != nil {
		return Response{}, fmt.Errorf("linkctl: %s: %w", req.Op, err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return Response{}, c.failed(ctx, req, err)
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return Response{}, c.failed(ctx, req, err)
	}
	if resp.Err != "" {
		return resp, remote(resp.Err)
	}
	return resp, nil
}

func (c *Client) failed(ctx context.Context, req Request, err error) error {
	switch {
	case ctx.Err() != nil:
		err = ctx.Err()
	case errors.Is(err, os.ErrDeadlineExceeded):
		err = context.DeadlineExceeded
	}
	return fmt.Errorf("linkctl: %s on %s: %w", req.Op, c.path, err)
}

// shared are the errors a reply's err can name, so a caller can still tell them apart with
// errors.Is once they have crossed the socket.
var shared = []error{
	links.ErrLinksOff, links.ErrNoCloser, links.ErrNotPublished, links.ErrReservedPort,
	links.ErrUnknownAddress, links.ErrFenced, links.ErrQuiescing, links.ErrRevoked,
	links.ErrRunEnded, links.ErrNotRunning, links.ErrGuardDown, links.ErrNotFound,
	links.ErrStateUnreadable, links.ErrTokensSpent,
}

func remote(text string) error {
	for _, err := range shared {
		if text == err.Error() {
			return fmt.Errorf("linkctl: %w", err)
		}
		if prefix, ok := strings.CutSuffix(text, ": "+err.Error()); ok {
			return fmt.Errorf("linkctl: %s: %w", prefix, err)
		}
	}
	return fmt.Errorf("linkctl: %s", text)
}
