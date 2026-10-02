package ssh

import (
	"context"
	"fmt"
	"net"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

func (c *Client) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if err := c.check(ctx, 0); err != nil {
		return nil, err
	}
	switch network {
	case "tcp", "tcp4", "tcp6", "unix":
	default:
		return nil, fmt.Errorf("unsupported SSH dial network %q", network)
	}
	conn, err := c.ssh.DialContext(ctx, network, address)
	if err != nil {
		return nil, fmt.Errorf("SSH %s dial %s: %w", network, address, err)
	}
	stream, ok := conn.(base.WriteCloseStream)
	if !ok {
		conn.Close()
		return nil, fmt.Errorf("SSH %s channel does not support CloseWrite", network)
	}
	return base.NewStreamConn(stream, conn.LocalAddr(), conn.RemoteAddr()), nil
}
