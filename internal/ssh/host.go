package ssh

import (
	"compress/gzip"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
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
	_, gzipExitCode, gzipErr := h.Run(ctx, "command -v gzip >/dev/null 2>&1", probeTimeout)
	compressedPath, compressed, compressErr := compressedHelperSource(localPath)
	if gzipErr == nil && gzipExitCode == 0 && compressErr == nil && compressed {
		decompressed := remotePath
		remoteCompressed := remotePath + ".gz"
		defer func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), probeTimeout)
			defer cancel()
			_ = filesystem.Delete(cleanupCtx, remoteCompressed, false)
		}()
		transferred, uploadErr := filesystem.Upload(ctx, compressedPath, remoteCompressed, options)
		_ = os.Remove(compressedPath)
		if uploadErr != nil {
			return transferred, uploadErr
		}
		_, exitCode, runErr := h.Run(ctx, "gzip -dc < "+quoteShell(remoteCompressed)+" > "+quoteShell(decompressed), probeTimeout)
		if runErr != nil {
			return transferred, runErr
		}
		if exitCode != 0 {
			return transferred, fmt.Errorf("remote gzip decompression exited with %d", exitCode)
		}
		return transferred, nil
	}
	if compressedPath != "" {
		_ = os.Remove(compressedPath)
	}
	return filesystem.Upload(ctx, localPath, remotePath, options)
}

func compressedHelperSource(localPath string) (string, bool, error) {
	info, err := os.Stat(localPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return "", false, err
	}
	source, err := os.Open(localPath)
	if err != nil {
		return "", false, err
	}
	defer source.Close()
	target, err := os.CreateTemp("", "nexterm-helper-*.gz")
	if err != nil {
		return "", false, err
	}
	targetPath := target.Name()
	removeTarget := true
	defer func() {
		if removeTarget {
			_ = os.Remove(targetPath)
		}
	}()
	writer := gzip.NewWriter(target)
	if _, err := io.Copy(writer, source); err != nil {
		_ = writer.Close()
		_ = target.Close()
		return "", false, err
	}
	if err := writer.Close(); err != nil {
		_ = target.Close()
		return "", false, err
	}
	if err := target.Close(); err != nil {
		return "", false, err
	}
	compressedInfo, err := os.Stat(targetPath)
	if err != nil {
		return "", false, err
	}
	if compressedInfo.Size() >= info.Size() {
		return "", false, nil
	}
	removeTarget = false
	return targetPath, true, nil
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
