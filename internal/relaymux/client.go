package relaymux

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"
)

// Client keeps one persistent Link to a server and opens device streams on it, redialing when the link ended.
// It is the client core's entry: the desktop and mobile Go core build one per signed-in server, with a dial
// function that fetches Config over the HTTPS API and calls Dial, and use OpenDeviceStream where they dial a
// bridge websocket today. The returned net.Conn is the bridge byte stream, so rstream wraps it unchanged.
type Client struct {
	dial func(context.Context) (*Link, error)

	mu      sync.Mutex
	link    *Link
	pending *clientDial
	closed  bool
}

// clientDial is one shared dial; done closes once link or err is set.
type clientDial struct {
	done chan struct{}
	link *Link
	err  error
}

// ClientDialTimeout bounds one shared dial, fetching the config included.
const ClientDialTimeout = 30 * time.Second

// NewClient returns a client that dials with dial on first use and after the link ended.
func NewClient(dial func(context.Context) (*Link, error)) *Client {
	return &Client{dial: dial}
}

// Link returns the live link, dialing one if there is none. Concurrent callers share one dial, which runs on
// its own ClientDialTimeout so no caller's cancellation tears it down under the others; each caller waits
// only as long as its own ctx allows.
func (c *Client) Link(ctx context.Context) (*Link, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, ErrClosed
	}
	if c.link != nil && c.link.Err() == nil {
		link := c.link
		c.mu.Unlock()
		return link, nil
	}
	pending := c.pending
	if pending == nil {
		pending = &clientDial{done: make(chan struct{})}
		c.pending = pending
		go c.runDial(pending)
	}
	c.mu.Unlock()
	select {
	case <-pending.done:
		if pending.err != nil {
			return nil, pending.err
		}
		return pending.link, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *Client) runDial(pending *clientDial) {
	ctx, cancel := context.WithTimeout(context.Background(), ClientDialTimeout)
	defer cancel()
	link, err := c.dial(ctx)
	c.mu.Lock()
	c.pending = nil
	if err == nil && c.closed {
		_ = link.Close()
		link, err = nil, ErrClosed
	}
	if err == nil {
		c.link = link
	}
	pending.link, pending.err = link, err
	close(pending.done)
	c.mu.Unlock()
}

// OpenDeviceStream opens a stream to header.DeviceID for header.Purpose. A link that turns out dead is
// redialed once; a refusal (a *RefusedError with the bridge endpoint's status, code and message) is returned
// as is. The reply's Params tell whether the agent reads the stream as an rstream carrier (ParamStream=1).
func (c *Client) OpenDeviceStream(ctx context.Context, header StreamHeader) (net.Conn, Reply, error) {
	for attempt := 0; ; attempt++ {
		link, err := c.Link(ctx)
		if err != nil {
			return nil, Reply{}, err
		}
		conn, reply, err := link.OpenDeviceStream(ctx, header)
		if err == nil {
			return conn, reply, nil
		}
		if _, refused := Refused(err); refused || ctx.Err() != nil || attempt > 0 {
			return nil, reply, err
		}
		select {
		case <-link.Done():
		default:
			return nil, reply, err
		}
	}
}

// Close closes the link and makes further calls fail.
func (c *Client) Close() error {
	c.mu.Lock()
	c.closed = true
	link := c.link
	c.link = nil
	c.mu.Unlock()
	if link != nil {
		return link.Close()
	}
	return nil
}

// errNoLink is returned by Current when no link is up.
var errNoLink = errors.New("relaymux: no link")

// Current returns the live link without dialing.
func (c *Client) Current() (*Link, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.link == nil || c.link.Err() != nil {
		return nil, errNoLink
	}
	return c.link, nil
}
