package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Hello-CTF/NexTerm/internal/deviceproto"
	sshfs "github.com/Hello-CTF/NexTerm/internal/fs/ssh"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// Extract unpacks an archive next to it through the server; a server that
// predates it, or a format it cannot read, falls back to the host's tar and
// unzip on POSIX hosts.
func (f *FileSystem) Extract(ctx context.Context, archivePath string) (string, error) {
	client, done, err := f.client(ctx)
	if err != nil {
		return "", err
	}
	dir, err := client.Extract(ctx, archivePath)
	done()
	if err == nil {
		return dir, nil
	}
	if (deviceproto.UnsupportedRequest(err) || errors.Is(err, base.ErrUnsupported)) && f.platform != "windows" && f.exec != nil {
		return f.extractWithShell(ctx, archivePath)
	}
	return "", f.unsupportedRequest(err, "解压")
}

// PackDownload downloads a directory as a gzipped tar packed by the server
// as it streams; a server that predates it falls back to tar through the
// shell on POSIX hosts.
func (f *FileSystem) PackDownload(ctx context.Context, remotePath, localPath string, options sshfs.TransferOptions) (int64, error) {
	conn, err := f.dial(ctx)
	if err != nil {
		return 0, err
	}
	stream, err := deviceproto.NewClient(conn).Pack(ctx, remotePath)
	if err != nil {
		if deviceproto.UnsupportedRequest(err) && f.platform != "windows" && f.exec != nil {
			return f.packDownloadWithShell(ctx, remotePath, localPath, options)
		}
		return 0, f.unsupportedRequest(err, "打包下载")
	}
	defer stream.Close()
	return writeDownload(localPath, stream, options)
}

// writeDownload writes a stream of unknown size to localPath through a
// temporary file, reporting progress and keeping to options.MaxBytes.
func writeDownload(localPath string, source io.Reader, options sshfs.TransferOptions) (int64, error) {
	dir := filepath.Dir(localPath)
	file, err := os.CreateTemp(dir, "."+filepath.Base(localPath)+".part-*")
	if err != nil {
		return 0, err
	}
	temporary := file.Name()
	committed := false
	defer func() {
		_ = file.Close()
		if !committed {
			_ = os.Remove(temporary)
		}
	}()
	buffer := make([]byte, 256<<10)
	var written int64
	for {
		count, readErr := source.Read(buffer)
		if count > 0 {
			if options.MaxBytes > 0 && written+int64(count) > options.MaxBytes {
				return written, base.ErrTransferTooLarge
			}
			if _, err := file.Write(buffer[:count]); err != nil {
				return written, err
			}
			written += int64(count)
			if options.Progress != nil {
				options.Progress(sshfs.Progress{Transferred: written})
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return written, readErr
		}
	}
	if err := file.Close(); err != nil {
		return written, err
	}
	if err := os.Rename(temporary, localPath); err != nil {
		return written, fmt.Errorf("finish download: %w", err)
	}
	committed = true
	if options.Progress != nil {
		options.Progress(sshfs.Progress{Transferred: written, Total: written, Done: true})
	}
	return written, nil
}
