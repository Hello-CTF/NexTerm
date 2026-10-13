// Package device adapts fleet device bridges to the session transport
// surface. A bridge is a net.Conn to the device agent selected by purpose:
// "terminal" carries the supervisor protocol to the device's supervisor
// helper, while "file" and "forward" speak the deviceproto protocol to the
// agent's file and dial servers, giving the transport its filesystem and
// dialer capabilities. Exec runs non-interactive commands through the
// supervisor's non-PTY exec (docker CLI backend, AI exec_commands); PTY
// channels stay unsupported because tabs are supervisor durable sessions.
package device

import (
	"context"
	"errors"
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
// dial server. Each operation dials a fresh bridge, mirroring the SSH
// daemon's exec-channel dial.
type DialFunc func(ctx context.Context, purpose string) (net.Conn, error)

type Transport struct {
	deviceID   string
	digest     string
	dial       DialFunc
	generation uint64
	closed     atomic.Bool
}

func New(deviceID, digest string, dial DialFunc, generation uint64) *Transport {
	return &Transport{deviceID: deviceID, digest: digest, dial: dial, generation: generation}
}

func (t *Transport) DeviceID() string   { return t.deviceID }
func (t *Transport) Kind() string       { return "device" }
func (t *Transport) Generation() uint64 { return t.generation }
func (t *Transport) IsAlive() bool      { return !t.closed.Load() }

// Exec runs one non-interactive command through the device shell (`sh -c`) via the supervisor's non-PTY exec over
// a terminal bridge, returning separated stdout/stderr and the exit code. Output beyond the supervisor's cap comes
// back as partial buffers marked Truncated, mirroring the SSH exec output-limit behavior.
func (t *Transport) Exec(ctx context.Context, command string, options base.ExecOptions) (base.ExecResult, error) {
	if options.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, options.Timeout)
		defer cancel()
	}
	stdout, stderr, exitCode, err := t.client().Exec(ctx, supervisor.ExecSpec{Command: []string{"/bin/sh", "-c", command}})
	result := base.ExecResult{Stdout: string(stdout), Stderr: string(stderr)}
	if exitCode >= 0 {
		result.ExitCode = &exitCode
	} else {
		result.Signal = "terminated"
	}
	if errors.Is(err, supervisor.ErrUnavailable) && len(result.Stdout)+len(result.Stderr) > 0 {
		// Only the output cap can produce ErrUnavailable with buffered output; dial/spawn failures carry none.
		result.Truncated = true
		return result, nil
	}
	return result, err
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
		return t.dial(ctx, deviceproto.PurposeTerminal)
	}, t.digest)
}

var (
	_ base.Transport     = (*Transport)(nil)
	_ base.FileTransport = (*Transport)(nil)
	_ base.Dialer        = (*Transport)(nil)
)
