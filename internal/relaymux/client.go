package relaymux

import (
	"context"
	"errors"
	"net"
	"sync"
)

// Client keeps one persistent Link to a server and opens device streams on it, redialing when the link ended.
// It is the client core's entry: the desktop and mobile Go core build one per signed-in server, with a dial
// function that fetches Config over the HTTPS API and calls Dial, and use OpenDeviceStream where they dial a
// bridge websocket today. The returned net.Conn is the bridge byte stream, so rstream wraps it unchanged.
type Client struct {
	dial func(context.Context) (*Link, error)

	mu      sync.Mutex
	link    *Link
	dialing chan struct{}
	dialErr error
	closed  bool
}

// NewClient returns a client that dials with dial on first use and after the link ended.
func NewClient(dial func(context.Context) (*Link, error)) *Client {
	return &Client{dial: dial}
}

// Link returns the live link, dialing one if there is none; concurrent callers share one dial.
func (c *Client) Link(ctx context.Context) (*Link, error) {
	for {
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
		if c.dialing == nil {
			c.dialing = make(chan struct{})
			dialing := c.dialing
			c.mu.Unlock()
			// The dial outlives one caller's cancellation so a shared dial is not torn down under the others.
			link, err := c.dial(context.WithoutCancel(ctx))
			c.mu.Lock()
			c.dialing = nil
			c.dialErr = err
			if err == nil {
				if c.closed {
					_ = link.Close()
					err = ErrClosed
				} else {
					c.link = link
				}
			}
			close(dialing)
			c.mu.Unlock()
			if err != nil {
				return nil, err
			}
			return link, nil
		}
		dialing := c.dialing
		c.mu.Unlock()
		select {
		case <-dialing:
			c.mu.Lock()
			err := c.dialErr
			c.mu.Unlock()
			if err != nil {
				return nil, err
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
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
