package device

import (
	"context"
	"fmt"
	"net"

	"github.com/Hello-CTF/NexTerm/internal/deviceproto"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// DialContext opens a forward-purpose bridge, requests a proxied connection
// from the device, and wraps the stream the way the SSH transport wraps
// channels, so callers get CloseWrite for half-close propagation.
func (t *Transport) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	switch network {
	case "tcp", "tcp4", "tcp6", "unix":
	default:
		return nil, fmt.Errorf("unsupported device dial network %q", network)
	}
	conn, err := t.dial(ctx, deviceproto.PurposeForward)
	if err != nil {
		return nil, err
	}
	stream, err := deviceproto.NewClient(conn).Dial(ctx, network, address)
	if err != nil {
		return nil, err
	}
	local := dialAddr{network: "device", address: t.deviceID}
	remote := dialAddr{network: network, address: address}
	return base.NewStreamConn(stream, local, remote), nil
}

type dialAddr struct {
	network string
	address string
}

func (a dialAddr) Network() string { return a.network }
func (a dialAddr) String() string  { return a.address }
