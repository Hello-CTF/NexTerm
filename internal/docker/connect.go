package docker

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/moby/moby/client"
)

type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

type DialerFunc func(ctx context.Context, network, address string) (net.Conn, error)

func (f DialerFunc) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return f(ctx, network, address)
}

type ClientConfig struct {
	Host       string
	TLSConfig  *tls.Config
	Dialer     Dialer
	HTTPClient *http.Client
	APIVersion string
}

func NewClient(config ClientConfig) (*client.Client, error) {
	options := make([]client.Opt, 0, 5)
	httpClient := config.HTTPClient
	if httpClient == nil && config.TLSConfig != nil {
		httpClient = &http.Client{Transport: &http.Transport{TLSClientConfig: config.TLSConfig.Clone()}}
	}
	if httpClient != nil {
		options = append(options, client.WithHTTPClient(httpClient))
	}
	if config.Host != "" {
		options = append(options, client.WithHost(config.Host))
	}
	if config.Dialer != nil {
		options = append(options, client.WithDialContext(config.Dialer.DialContext))
	}
	if config.APIVersion != "" {
		options = append(options, client.WithAPIVersion(config.APIVersion))
	}
	return client.New(options...)
}

func NewSSHStreamlocalClient(socket string, dialer Dialer) (*client.Client, error) {
	if dialer == nil {
		return nil, errors.New("docker SSH dialer is required")
	}
	if socket == "" {
		socket = "/var/run/docker.sock"
	}
	return NewClient(ClientConfig{
		Host: "unix://" + socket,
		Dialer: DialerFunc(func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socket)
		}),
	})
}

type StdioOpener interface {
	OpenDockerStdio(ctx context.Context) (io.ReadWriteCloser, error)
}

type StdioOpenerFunc func(ctx context.Context) (io.ReadWriteCloser, error)

func (f StdioOpenerFunc) OpenDockerStdio(ctx context.Context) (io.ReadWriteCloser, error) {
	return f(ctx)
}

func NewSSHDialStdioClient(opener StdioOpener) (*client.Client, error) {
	if opener == nil {
		return nil, errors.New("docker dial-stdio opener is required")
	}
	dialer := DialerFunc(func(ctx context.Context, _, _ string) (net.Conn, error) {
		channel, err := opener.OpenDockerStdio(ctx)
		if err != nil {
			return nil, err
		}
		return NewChannelConn(channel), nil
	})
	return NewClient(ClientConfig{Host: "tcp://" + client.DummyHost, Dialer: dialer})
}

func NewChannelConn(channel io.ReadWriteCloser) net.Conn {
	return &channelConn{channel: channel}
}

type channelConn struct {
	channel io.ReadWriteCloser

	mu            sync.Mutex
	readDeadline  time.Time
	writeDeadline time.Time
	readTimer     *time.Timer
	writeTimer    *time.Timer
	closed        bool
	closeOnce     sync.Once
	closeErr      error
}

func (c *channelConn) Read(buffer []byte) (int, error) {
	if err := c.operationErr(true); err != nil {
		return 0, err
	}
	return c.channel.Read(buffer)
}

func (c *channelConn) Write(buffer []byte) (int, error) {
	if err := c.operationErr(false); err != nil {
		return 0, err
	}
	return c.channel.Write(buffer)
}

func (c *channelConn) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		if c.readTimer != nil {
			c.readTimer.Stop()
		}
		if c.writeTimer != nil {
			c.writeTimer.Stop()
		}
		c.mu.Unlock()
		c.closeErr = c.channel.Close()
	})
	return c.closeErr
}

func (c *channelConn) CloseWrite() error {
	if writer, ok := c.channel.(interface{ CloseWrite() error }); ok {
		return writer.CloseWrite()
	}
	return nil
}

func (c *channelConn) LocalAddr() net.Addr {
	return channelAddr("ssh-local")
}

func (c *channelConn) RemoteAddr() net.Addr {
	return channelAddr("docker-dial-stdio")
}

func (c *channelConn) SetDeadline(deadline time.Time) error {
	if err := c.SetReadDeadline(deadline); err != nil {
		return err
	}
	return c.SetWriteDeadline(deadline)
}

func (c *channelConn) SetReadDeadline(deadline time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	c.readDeadline = deadline
	c.readTimer = resetDeadlineTimer(c.readTimer, deadline, c.Close)
	return nil
}

func (c *channelConn) SetWriteDeadline(deadline time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	c.writeDeadline = deadline
	c.writeTimer = resetDeadlineTimer(c.writeTimer, deadline, c.Close)
	return nil
}

func (c *channelConn) operationErr(read bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	deadline := c.writeDeadline
	if read {
		deadline = c.readDeadline
	}
	if !deadline.IsZero() && !time.Now().Before(deadline) {
		return os.ErrDeadlineExceeded
	}
	return nil
}

func resetDeadlineTimer(timer *time.Timer, deadline time.Time, closeConn func() error) *time.Timer {
	if timer != nil {
		timer.Stop()
	}
	if deadline.IsZero() {
		return nil
	}
	duration := time.Until(deadline)
	if duration < 0 {
		duration = 0
	}
	if timer == nil {
		return time.AfterFunc(duration, func() { _ = closeConn() })
	}
	timer.Reset(duration)
	return timer
}

type channelAddr string

func (a channelAddr) Network() string { return "ssh-channel" }
func (a channelAddr) String() string  { return string(a) }
