package device

import (
	"context"
	"fmt"
	"net"

	"github.com/Hello-CTF/NexTerm/internal/deviceproto"
	"github.com/Hello-CTF/NexTerm/internal/forward"
)

// ListenRemote opens a listen-purpose bridge and asks the device agent to
// bind network/address, returning the remote listener the forward service
// drives for remote port forwards: inbound connections arrive on the listen
// bridge and each Accept claims one on a fresh accept-purpose bridge, giving
// device sessions SSH tcpip-forward parity.
func (t *Transport) ListenRemote(ctx context.Context, network, address string) (net.Listener, error) {
	switch network {
	case "tcp", "tcp4", "tcp6":
	default:
		return nil, fmt.Errorf("unsupported device remote listen network %q", network)
	}
	conn, err := t.dial(ctx, deviceproto.PurposeListen)
	if err != nil {
		return nil, err
	}
	return deviceproto.NewClient(conn, t.dial).Listen(ctx, network, address)
}

var _ forward.RemoteListener = (*Transport)(nil)
