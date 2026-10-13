// Package purpose implements the agent-side servers for non-terminal bridge
// purposes: file serves the device filesystem, forward dials TCP/unix
// targets, and listen binds TCP/unix listeners whose inbound connections are
// claimed over accept bridges, each speaking the deviceproto wire protocol
// over one fleet bridge connection. Bridges are authorized at the fleet
// server edge, so the servers act with the agent user's privileges, mirroring
// the terminal shell.
package purpose

import (
	"context"
	"net"
)

// BridgeDialer claims one purpose bridge and returns its byte stream.
type BridgeDialer interface {
	DialBridge(ctx context.Context, bridgeID, purpose string) (net.Conn, error)
}

// BridgeDialerFunc adapts a plain function to BridgeDialer.
type BridgeDialerFunc func(ctx context.Context, bridgeID, purpose string) (net.Conn, error)

func (f BridgeDialerFunc) DialBridge(ctx context.Context, bridgeID, purpose string) (net.Conn, error) {
	return f(ctx, bridgeID, purpose)
}
