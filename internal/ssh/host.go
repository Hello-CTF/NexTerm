package ssh

import (
	"context"
	"encoding/hex"
	"io/fs"
	"net"
	"strings"
	"time"

	sshfs "github.com/Hello-CTF/NexTerm/internal/fs/ssh"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	transportssh "github.com/Hello-CTF/NexTerm/internal/transport/ssh"
)

const sha256HexLength = 64

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

func (h *clientHost) UploadWithProgress(ctx context.Context, localPath, remotePath string, options sshfs.TransferOptions) (int64, error) {
	filesystem, err := h.filesystem(ctx)
	if err != nil {
		return 0, err
	}
	return filesystem.Upload(ctx, localPath, remotePath, options)
}

func (h *clientHost) Rename(ctx context.Context, from, to string) error {
	filesystem, err := h.filesystem(ctx)
	if err != nil {
		return err
	}
	return filesystem.Rename(ctx, from, to)
}

func (h *clientHost) ChecksumSHA256(ctx context.Context, path string) (string, error) {
	// Hashing through the remote shell avoids downloading the whole helper over
	// SFTP just to validate a cache entry. Keep the SFTP implementation as a
	// fallback for minimal targets without sha256sum.
	stdout, exitCode, err := h.Run(ctx, "sha256sum -- "+quoteShell(path), probeTimeout)
	if err == nil && exitCode == 0 {
		if digest, ok := parseSHA256Output(stdout); ok {
			return digest, nil
		}
	}
	filesystem, err := h.filesystem(ctx)
	if err != nil {
		return "", err
	}
	return filesystem.Checksum(ctx, path, "sha256")
}

func parseSHA256Output(stdout string) (string, bool) {
	fields := strings.Fields(stdout)
	if len(fields) == 0 || len(fields[0]) != sha256HexLength {
		return "", false
	}
	if _, err := hex.DecodeString(fields[0]); err != nil {
		return "", false
	}
	return strings.ToLower(fields[0]), true
}

func (h *clientHost) DialExec(ctx context.Context, command string) (net.Conn, error) {
	return h.client.OpenExecConn(ctx, command, base.ExecOptions{})
}
