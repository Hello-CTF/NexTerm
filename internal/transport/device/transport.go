// Package device adapts a fleet device bridge to the session transport
// surface. A bridge is a net.Conn carrying the supervisor protocol to the
// device's supervisor helper; the v2 protocol has no fs/exec frames, so the
// transport only exposes liveness probing and the durable provider, with Exec
// and the filesystem surface staying unsupported.
package device

import (
	"context"
	"net"
	"sync/atomic"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// DialFunc opens one bridge connection to a device's supervisor helper. Each
// supervisor operation dials a fresh bridge, mirroring the SSH daemon's
// exec-channel dial.
type DialFunc func(ctx context.Context, deviceID string) (net.Conn, error)

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

func (t *Transport) Exec(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
	return base.ExecResult{}, base.ErrUnsupported
}

// Ping probes device reachability with a supervisor list round-trip over a
// fresh bridge.
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
		return t.dial(ctx, t.deviceID)
	}, t.digest)
}

var _ base.Transport = (*Transport)(nil)
