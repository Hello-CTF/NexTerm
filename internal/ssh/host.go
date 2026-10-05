package ssh

import (
	"context"
	"io/fs"
	"net"
	"time"

	sshfs "github.com/ProbiusOfficial/NexTerm/internal/fs/ssh"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
	transportssh "github.com/ProbiusOfficial/NexTerm/internal/transport/ssh"
)

type clientHost struct {
	client *transportssh.Client
}

func (r *Resolver) host(ctx context.Context, client *transportssh.Client) (Host, error) {
	if r.hostFor != nil {
		return r.hostFor(ctx, client)
	}
	return &clientHost{client: client}, nil
}

func (h *clientHost) Run(ctx context.Context, command string, timeout time.Duration) (string, int, error) {
	result, err := h.client.Exec(ctx, command, base.ExecOptions{
		Timeout: timeout,
		Limits:  base.OutputLimits{Stdout: 1 << 20, Stderr: 64 << 10},
	})
	if err != nil {
		return "", 0, err
	}
	exitCode := 0
	if result.ExitCode != nil {
		exitCode = *result.ExitCode
	}
	return result.Stdout, exitCode, nil
}

func (h *clientHost) filesystem(ctx context.Context) (*sshfs.FS, error) {
	return h.client.SFTP(ctx)
}

func (h *clientHost) Exists(ctx context.Context, path string) (bool, error) {
	filesystem, err := h.filesystem(ctx)
	if err != nil {
		return false, err
	}
	return filesystem.Exists(ctx, path)
}

func (h *clientHost) Mkdir(ctx context.Context, path string) error {
	filesystem, err := h.filesystem(ctx)
	if err != nil {
		return err
	}
	return filesystem.Mkdir(ctx, path)
}

func (h *clientHost) Chmod(ctx context.Context, path string, mode fs.FileMode) error {
	filesystem, err := h.filesystem(ctx)
	if err != nil {
		return err
	}
	return filesystem.Chmod(ctx, path, mode)
}

func (h *clientHost) Upload(ctx context.Context, localPath, remotePath string) (int64, error) {
	filesystem, err := h.filesystem(ctx)
	if err != nil {
		return 0, err
	}
	return filesystem.Upload(ctx, localPath, remotePath, sshfs.TransferOptions{})
}

func (h *clientHost) Rename(ctx context.Context, from, to string) error {
	filesystem, err := h.filesystem(ctx)
	if err != nil {
		return err
	}
	return filesystem.Rename(ctx, from, to)
}

func (h *clientHost) ChecksumSHA256(ctx context.Context, path string) (string, error) {
	filesystem, err := h.filesystem(ctx)
	if err != nil {
		return "", err
	}
	return filesystem.Checksum(ctx, path, "sha256")
}

func (h *clientHost) DialExec(ctx context.Context, command string) (net.Conn, error) {
	return h.client.OpenExecConn(ctx, command, base.ExecOptions{})
}
