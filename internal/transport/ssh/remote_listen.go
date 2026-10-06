package ssh

import (
	"context"
	"fmt"
	"net"
)

func (c *Client) ListenRemote(ctx context.Context, network, address string) (net.Listener, error) {
	if err := c.check(ctx, 0); err != nil {
		return nil, err
	}
	switch network {
	case "tcp", "tcp4", "tcp6":
	default:
		return nil, fmt.Errorf("unsupported SSH remote listen network %q", network)
	}
	listener, err := c.ssh.Listen(network, address)
	if err != nil {
		return nil, fmt.Errorf("SSH remote listen %s: %w", address, err)
	}
	return listener, nil
}
