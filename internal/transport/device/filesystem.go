package device

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"path"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/deviceproto"
	fslocal "github.com/Hello-CTF/NexTerm/internal/fs/local"
	sshfs "github.com/Hello-CTF/NexTerm/internal/fs/ssh"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

const archiveExecTimeout = 10 * time.Minute

// fileSystem serves the base filesystem surface through fresh file-purpose
// bridge dials per operation, mirroring how the supervisor client dials per
// call; streaming operations hold their bridge until the stream closes.
// exec runs shell commands through the transport for archive extraction.
type fileSystem struct {
	dial func(ctx context.Context) (net.Conn, error)
	exec func(ctx context.Context, command string, options base.ExecOptions) (base.ExecResult, error)
}

// FileSystem returns the device filesystem surface. It does not probe: each
// operation dials its own bridge, so a missing capability or an offline
// device surfaces at operation time as the bridge dial error.
func (t *Transport) FileSystem(ctx context.Context) (base.FileSystem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &fileSystem{
		dial: func(ctx context.Context) (net.Conn, error) {
			return t.dial(ctx, deviceproto.PurposeFile)
		},
		exec: t.Exec,
	}, nil
}

// client dials one operation bridge; done releases it.
func (f *fileSystem) client(ctx context.Context) (*deviceproto.Client, func(), error) {
	conn, err := f.dial(ctx)
	if err != nil {
		return nil, nil, err
	}
	return deviceproto.NewClient(conn), func() { _ = conn.Close() }, nil
}

func (f *fileSystem) List(ctx context.Context, path string) ([]base.FileEntry, error) {
	client, done, err := f.client(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	return client.List(ctx, path)
}

func (f *fileSystem) ReadFile(ctx context.Context, path string, maxBytes int64) ([]byte, error) {
	client, done, err := f.client(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	return client.ReadFile(ctx, path, maxBytes)
}

// ReadRange mirrors the SSH filesystem's range probe used by the file editor.
func (f *fileSystem) ReadRange(ctx context.Context, path string, offset, maxBytes int64) ([]byte, int64, error) {
	client, done, err := f.client(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer done()
	return client.ReadRange(ctx, path, offset, maxBytes)
}

func (f *fileSystem) WriteFile(ctx context.Context, path string, data []byte, backup bool) error {
	client, done, err := f.client(ctx)
	if err != nil {
		return err
	}
	defer done()
	return client.WriteFile(ctx, path, data, backup)
}

func (f *fileSystem) Mkdir(ctx context.Context, path string) error {
	client, done, err := f.client(ctx)
	if err != nil {
		return err
	}
	defer done()
	return client.Mkdir(ctx, path)
}

func (f *fileSystem) Rename(ctx context.Context, from, to string) error {
	client, done, err := f.client(ctx)
	if err != nil {
		return err
	}
	defer done()
	return client.Rename(ctx, from, to)
}

func (f *fileSystem) Delete(ctx context.Context, path string, isDir bool) error {
	client, done, err := f.client(ctx)
	if err != nil {
		return err
	}
	defer done()
	return client.Delete(ctx, path, isDir)
}

func (f *fileSystem) Chmod(ctx context.Context, path string, mode fs.FileMode) error {
	client, done, err := f.client(ctx)
	if err != nil {
		return err
	}
	defer done()
	return client.Chmod(ctx, path, mode)
}

func (f *fileSystem) Checksum(ctx context.Context, path, algorithm string) (string, error) {
	client, done, err := f.client(ctx)
	if err != nil {
		return "", err
	}
	defer done()
	return client.Checksum(ctx, path, algorithm)
}

func (f *fileSystem) Exists(ctx context.Context, path string) (bool, error) {
	client, done, err := f.client(ctx)
	if err != nil {
		return false, err
	}
	defer done()
	if _, err := client.Stat(ctx, path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (f *fileSystem) Size(ctx context.Context, path string) (int64, error) {
	client, done, err := f.client(ctx)
	if err != nil {
		return 0, err
	}
	defer done()
	return client.Stat(ctx, path)
}

func (f *fileSystem) OpenRead(ctx context.Context, path string) (base.RemoteReader, error) {
	conn, err := f.dial(ctx)
	if err != nil {
		return nil, err
	}
	return deviceproto.NewClient(conn).OpenRead(ctx, path)
}

func (f *fileSystem) OpenWrite(ctx context.Context, path string, appendMode bool) (base.RemoteWriter, error) {
	conn, err := f.dial(ctx)
	if err != nil {
		return nil, err
	}
	return deviceproto.NewClient(conn).OpenWrite(ctx, path, appendMode)
}

// Extract unpacks a tar archive into its own directory through the device
// shell (agents are linux/darwin, so POSIX sh and tar are present) and
// returns that directory; a non-zero tar exit surfaces its stderr.
func (f *fileSystem) Extract(ctx context.Context, archivePath string) (string, error) {
	if archivePath == "" || strings.ContainsRune(archivePath, 0) {
		return "", fmt.Errorf("archive path is empty or contains NUL")
	}
	dir := path.Dir(archivePath)
	command := "tar -xf " + shellQuoteHome(archivePath) + " -C " + shellQuoteHome(dir)
	result, err := f.exec(ctx, command, base.ExecOptions{Timeout: archiveExecTimeout})
	if err != nil {
		return "", fmt.Errorf("extract device archive: %w", err)
	}
	if result.ExitCode == nil {
		return "", fmt.Errorf("extract device archive: %w", base.ErrExitStatusMissing)
	}
	if *result.ExitCode != 0 {
		message := strings.TrimSpace(result.Stderr + result.Stdout)
		return "", fmt.Errorf("extract device archive: remote command exited %d: %s", *result.ExitCode, message)
	}
	if result.Truncated {
		return "", fmt.Errorf("extract device archive: command output exceeded safety limit")
	}
	return dir, nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// shellQuoteHome expands a leading ~ through the shell's $HOME; a quoted tilde
// is never expanded, so the home prefix stays unquoted and adjacent to the
// quoted remainder.
func shellQuoteHome(path string) string {
	if path == "~" {
		return `"$HOME"`
	}
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		return `"$HOME"` + shellQuote("/"+rest)
	}
	return shellQuote(path)
}

// PackDownload tars a directory through the device shell into a random temp
// archive, downloads it through the regular transfer path, and removes the
// temp archive on both success and failure.
func (f *fileSystem) PackDownload(ctx context.Context, remotePath, localPath string, options sshfs.TransferOptions) (int64, error) {
	parent, name, err := splitArchivePath(remotePath)
	if err != nil {
		return 0, err
	}
	var random [12]byte
	_, _ = rand.Read(random[:])
	temporary := "/tmp/nexterm-pack-" + hex.EncodeToString(random[:]) + ".tar.gz"
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = f.Delete(cleanupCtx, temporary, false)
	}()
	command := "umask 077 && tar -czf " + shellQuote(temporary) + " -C " + shellQuoteHome(parent) + " -- " + shellQuote(shellOperand(name))
	result, err := f.exec(ctx, command, base.ExecOptions{Timeout: archiveExecTimeout})
	if err != nil {
		return 0, fmt.Errorf("create device archive: %w", err)
	}
	if result.ExitCode == nil {
		return 0, fmt.Errorf("create device archive: %w", base.ErrExitStatusMissing)
	}
	if *result.ExitCode != 0 {
		message := strings.TrimSpace(result.Stderr + result.Stdout)
		return 0, fmt.Errorf("create device archive: remote command exited %d: %s", *result.ExitCode, message)
	}
	if result.Truncated {
		return 0, fmt.Errorf("create device archive: command output exceeded safety limit")
	}
	return fslocal.Download(ctx, f, temporary, localPath, fslocal.TransferOptions{
		Progress: func(progress fslocal.Progress) {
			if options.Progress != nil {
				options.Progress(sshfs.Progress{Transferred: progress.Transferred, Total: progress.Total, Done: progress.Done})
			}
		},
	})
}

// splitArchivePath decomposes a pack source into a -C parent and an operand
// name; a ~ prefix is kept literal for later shell expansion by shellQuoteHome.
func splitArchivePath(remotePath string) (string, string, error) {
	if remotePath == "" || strings.ContainsRune(remotePath, 0) {
		return "", "", fmt.Errorf("remote path is empty or contains NUL")
	}
	cleaned := path.Clean(remotePath)
	if cleaned == "/" || cleaned == "~" {
		return cleaned, ".", nil
	}
	parent, name := path.Split(cleaned)
	if parent == "" {
		parent = "."
	} else {
		parent = strings.TrimSuffix(parent, "/")
		if parent == "" {
			parent = "/"
		}
	}
	if name == "" {
		return "", "", fmt.Errorf("remote path has no file name")
	}
	return parent, name, nil
}

func shellOperand(value string) string {
	if strings.HasPrefix(value, "-") {
		return "./" + value
	}
	return value
}

var _ base.FileSystem = (*fileSystem)(nil)
