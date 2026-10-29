// Package device adapts fleet device bridges to the session transport
// surface. A bridge is a net.Conn to the device agent selected by purpose:
// "terminal" carries the supervisor protocol to the device's supervisor
// helper, while "file", "forward", "listen", and "accept" speak the
// deviceproto protocol to the agent's purpose servers, giving the transport
// its filesystem, dialer, and remote listen capabilities. Remote port
// forwards run one listen bridge per remote listener plus one accept bridge
// per inbound connection (SSH tcpip-forward parity). Exec runs
// non-interactive commands through the supervisor's non-PTY exec (docker CLI
// backend, AI exec_commands); PTY channels stay unsupported because tabs are
// supervisor durable sessions.
package device

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/deviceproto"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// DialFunc opens one bridge connection of the given purpose to the device:
// deviceproto.PurposeTerminal reaches the supervisor helper,
// deviceproto.PurposeFile the file server, deviceproto.PurposeForward the
// dial server, deviceproto.PurposeListen the listen server, and
// deviceproto.PurposeAccept claims one accepted inbound connection. Each
// operation dials a fresh bridge, mirroring the SSH daemon's exec-channel
// dial.
type DialFunc func(ctx context.Context, purpose string) (net.Conn, error)

type Transport struct {
	deviceID   string
	digest     string
	dial       DialFunc
	generation uint64
	platform   string
	closed     atomic.Bool
	// lost is set once a heartbeat failed definitively (the bridge cannot be
	// dialed or answered with an error rather than timing out); IsAlive then
	// reports false so the session reconnects.
	lost atomic.Bool
	// inbound counts bytes read from every bridge this transport dialed.
	inbound atomic.Uint64
}

// New builds the device transport; platform is the agent's GOOS when known ("windows" selects the
// PowerShell exec wrapper, everything else defaults to POSIX sh).
func New(deviceID, digest string, dial DialFunc, generation uint64, platform ...string) *Transport {
	t := &Transport{deviceID: deviceID, digest: digest, dial: dial, generation: generation}
	if len(platform) > 0 {
		t.platform = platform[0]
	}
	return t
}

func (t *Transport) DeviceID() string   { return t.deviceID }
func (t *Transport) Kind() string       { return "device" }
func (t *Transport) Generation() uint64 { return t.generation }
func (t *Transport) IsAlive() bool      { return !t.closed.Load() && !t.lost.Load() }

// Platform returns the agent's GOOS when known (empty means unknown, POSIX default).
func (t *Transport) Platform() string { return t.platform }

// Exec runs one non-interactive command through the device shell via the supervisor's non-PTY exec over a
// terminal bridge, returning separated stdout/stderr and the exit code (-1 with the signal name when the
// process was killed by a signal, mirroring the SSH exec convention). Output beyond the supervisor's cap comes
// back as partial buffers marked Truncated.
func (t *Transport) Exec(ctx context.Context, command string, options base.ExecOptions) (base.ExecResult, error) {
	if err := t.checkExec(options); err != nil {
		return base.ExecResult{}, err
	}
	if options.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, options.Timeout)
		defer cancel()
	}
	stdout, stderr, exitCode, signal, err := t.client().Exec(ctx, supervisor.ExecSpec{Command: execCommand(t.platform, command)})
	result := base.ExecResult{Stdout: string(stdout), Stderr: string(stderr), ExitCode: &exitCode, Signal: signal}
	if errors.Is(err, supervisor.ErrUnavailable) && len(result.Stdout)+len(result.Stderr) > 0 {
		// Only the output cap can produce ErrUnavailable with buffered output; dial/spawn failures carry none.
		result.Truncated = true
		return result, nil
	}
	return result, err
}

// execCommand wraps a command line for the device shell: Windows agents get PowerShell (matching the docker
// PowerShell quoting and the winrm exec convention), everything else defaults to POSIX `sh -c`.
func execCommand(platform, command string) []string {
	if platform == "windows" {
		return []string{"powershell.exe", "-NoProfile", "-Command", command}
	}
	return []string{"/bin/sh", "-c", command}
}

// Ping probes device reachability with a supervisor list round-trip over a
// fresh terminal bridge.
func (t *Transport) Ping(ctx context.Context) (time.Duration, error) {
	start := time.Now()
	if _, err := t.client().List(ctx); err != nil {
		return 0, err
	}
	return time.Since(start), nil
}

// heartbeatTimeout bounds one heartbeat when the caller's context has no
// shorter deadline.
const heartbeatTimeout = 10 * time.Second

// Heartbeat checks that the device still answers with one supervisor list
// round-trip over a fresh terminal bridge. A reply that does not arrive in
// time is returned as context.DeadlineExceeded (a missed beat, not a verdict);
// any other failure means the bridge is unreachable and marks the transport
// not alive.
func (t *Transport) Heartbeat(ctx context.Context) error {
	if t.closed.Load() {
		return base.ErrDisconnected
	}
	ctx, cancel := context.WithTimeout(ctx, heartbeatTimeout)
	defer cancel()
	// A stalled bridge can block a frame write that ignores ctx, so the
	// connection is closed when the heartbeat's deadline passes.
	client := supervisor.NewRemoteClient(func(dialCtx context.Context) (net.Conn, error) {
		conn, err := t.dial(dialCtx, deviceproto.PurposeTerminal)
		if err != nil {
			return nil, err
		}
		context.AfterFunc(ctx, func() { _ = conn.Close() })
		return countingConn{Conn: conn, counter: &t.inbound}, nil
	}, t.digest)
	_, err := client.List(ctx)
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	t.lost.Store(true)
	if errors.Is(err, base.ErrDisconnected) {
		return err
	}
	return fmt.Errorf("%w: %v", base.ErrDisconnected, err)
}

// InboundBytes is the running count of bytes received from the device.
func (t *Transport) InboundBytes() uint64 { return t.inbound.Load() }

type countingConn struct {
	net.Conn
	counter *atomic.Uint64
}

func (c countingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.counter.Add(uint64(n))
	}
	return n, err
}

func (t *Transport) Close() error {
	t.closed.Store(true)
	return nil
}

// Provider returns the durable provider backed by the device's supervisor
// helper, reached through fresh bridge dials per operation.
func (t *Transport) Provider() *supervisor.RemoteProvider {
	return supervisor.NewRemoteProvider(t.client())
}

func (t *Transport) client() *supervisor.Client {
	return supervisor.NewRemoteClient(func(ctx context.Context) (net.Conn, error) {
		conn, err := t.dial(ctx, deviceproto.PurposeTerminal)
		if err != nil {
			return nil, err
		}
		return countingConn{Conn: conn, counter: &t.inbound}, nil
	}, t.digest)
}

var (
	_ base.Transport     = (*Transport)(nil)
	_ base.Heartbeater   = (*Transport)(nil)
	_ base.FileTransport = (*Transport)(nil)
	_ base.Dialer        = (*Transport)(nil)
)
