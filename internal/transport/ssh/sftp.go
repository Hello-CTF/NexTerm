package ssh

import (
	"context"
	"fmt"
	"io"

	sshfs "github.com/ProbiusOfficial/NexTerm/internal/fs/ssh"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
	"github.com/pkg/sftp"
	gossh "golang.org/x/crypto/ssh"
)

type sftpState struct {
	client     *sftp.Client
	session    *gossh.Session
	filesystem *sshfs.FS
}

func (c *Client) FileSystem(ctx context.Context) (base.FileSystem, error) {
	return c.SFTP(ctx)
}

func (c *Client) SFTP(ctx context.Context) (*sshfs.FS, error) {
	c.sftpMu.Lock()
	defer c.sftpMu.Unlock()
	if c.sftp != nil {
		return c.sftp.filesystem, nil
	}
	if err := c.check(ctx, 0); err != nil {
		return nil, err
	}
	session, err := c.newSession(ctx)
	if err != nil {
		return nil, fmt.Errorf("open SFTP session: %w", err)
	}
	stop := context.AfterFunc(ctx, func() { session.Close() })
	failed := true
	defer func() {
		if failed {
			session.Close()
		}
	}()
	stdin, err := session.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := session.RequestSubsystem("sftp"); err != nil {
		return nil, fmt.Errorf("request SFTP subsystem: %w", err)
	}
	go io.Copy(io.Discard, stderr)
	client, err := sftp.NewClientPipe(stdout, stdin)
	if err != nil {
		return nil, fmt.Errorf("initialize SFTP: %w", err)
	}
	if !stop() && ctx.Err() != nil {
		client.Close()
		return nil, ctx.Err()
	}
	state := &sftpState{client: client, session: session}
	state.filesystem = sshfs.New(client, c)
	c.sftp = state
	failed = false
	return state.filesystem, nil
}

func (c *Client) closeSFTP() {
	c.sftpMu.Lock()
	defer c.sftpMu.Unlock()
	if c.sftp == nil {
		return
	}
	c.sftp.client.Close()
	c.sftp.session.Close()
	c.sftp = nil
}
