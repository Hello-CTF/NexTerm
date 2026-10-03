package ssh

import (
	"context"
	"io"
	"net"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

type ExecConn struct {
	*base.StreamConn
	channel base.Channel
}

func (c *Client) OpenExecConn(ctx context.Context, command string, options base.ExecOptions) (*ExecConn, error) {
	channel, err := c.OpenExec(ctx, command, options)
	if err != nil {
		return nil, err
	}
	return &ExecConn{
		StreamConn: base.NewStreamConn(channel, sshAddr("local"), sshAddr(command)),
		channel:    channel,
	}, nil
}

func (c *ExecConn) Stderr() io.Reader {
	return c.channel.Stderr()
}

func (c *ExecConn) Wait(ctx context.Context) error {
	return c.channel.Wait(ctx)
}

func (c *ExecConn) ID() string {
	return c.channel.ID()
}

func (c *ExecConn) Generation() uint64 {
	return c.channel.Generation()
}

var _ base.OrderedOutput = (*ExecConn)(nil)

type sshAddr string

func (a sshAddr) Network() string {
	return "ssh"
}

func (a sshAddr) String() string {
	return string(a)
}

var _ net.Conn = (*ExecConn)(nil)
