package ssh

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
	"github.com/pkg/sftp"
)

type Executor interface {
	Exec(context.Context, string, base.ExecOptions) (base.ExecResult, error)
}

type FS struct {
	client   *sftp.Client
	executor Executor
}

func New(client *sftp.Client, executor Executor) *FS {
	return &FS{client: client, executor: executor}
}

func (f *FS) List(ctx context.Context, remotePath string) ([]base.FileEntry, error) {
	remotePath, err := f.real(ctx, remotePath)
	if err != nil {
		return nil, err
	}
	infos, err := f.client.ReadDir(remotePath)
	if err != nil {
		return nil, fmt.Errorf("SFTP list %s: %w", remotePath, err)
	}
	entries := make([]base.FileEntry, 0, len(infos))
	for _, info := range infos {
		if info.Name() == "." || info.Name() == ".." {
			continue
		}
		entry := fileEntry(info, path.Join(remotePath, info.Name()))
		if entry.Kind == base.FileSymlink {
			if target, err := f.client.ReadLink(entry.Path); err == nil {
				entry.SymlinkTarget = target
			}
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		if (entries[i].Kind == base.FileDirectory) != (entries[j].Kind == base.FileDirectory) {
			return entries[i].Kind == base.FileDirectory
		}
		return entries[i].Name < entries[j].Name
	})
	return entries, ctx.Err()
}

func (f *FS) ReadFile(ctx context.Context, remotePath string, maxBytes int64) ([]byte, error) {
	if maxBytes < 0 {
		return nil, fmt.Errorf("maximum read size must not be negative")
	}
	reader, err := f.OpenRead(ctx, remotePath)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	if reader.Size() > maxBytes {
		return nil, fmt.Errorf("remote file exceeds read limit (%d > %d bytes)", reader.Size(), maxBytes)
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("SFTP read %s: %w", remotePath, err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("remote file grew beyond read limit of %d bytes", maxBytes)
	}
	return data, nil
}

func (f *FS) WriteFile(ctx context.Context, remotePath string, data []byte, backup bool) error {
	remotePath, err := f.real(ctx, remotePath)
	if err != nil {
		return err
	}
	if backup {
		info, err := f.client.Stat(remotePath)
		switch {
		case err == nil:
			if err := f.backup(ctx, remotePath, info.Mode()); err != nil {
				return err
			}
		case errors.Is(err, fs.ErrNotExist) || os.IsNotExist(err):
		default:
			return fmt.Errorf("SFTP stat before backup: %w", err)
		}
	}
	writer, err := f.OpenWrite(ctx, remotePath, false)
	if err != nil {
		return err
	}
	defer writer.Close()
	if err := writeAll(ctx, writer, data); err != nil {
		return fmt.Errorf("SFTP write %s: %w", remotePath, err)
	}
	if err := writer.Sync(); err != nil {
		return fmt.Errorf("SFTP sync %s: %w", remotePath, err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("SFTP close %s: %w", remotePath, err)
	}
	return nil
}

func (f *FS) Mkdir(ctx context.Context, remotePath string) error {
	remotePath, err := f.real(ctx, remotePath)
	if err != nil {
		return err
	}
	if err := f.client.Mkdir(remotePath); err != nil {
		return fmt.Errorf("SFTP mkdir %s: %w", remotePath, err)
	}
	return ctx.Err()
}

func (f *FS) Rename(ctx context.Context, from, to string) error {
	from, to, err := f.realPair(ctx, from, to)
	if err != nil {
		return err
	}
	if err := f.client.Rename(from, to); err != nil {
		return fmt.Errorf("SFTP rename %s to %s: %w", from, to, err)
	}
	return ctx.Err()
}

func (f *FS) Delete(ctx context.Context, remotePath string, directory bool) error {
	remotePath, err := f.real(ctx, remotePath)
	if err != nil {
		return err
	}
	if directory {
		err = f.client.RemoveDirectory(remotePath)
	} else {
		err = f.client.Remove(remotePath)
	}
	if err != nil {
		return fmt.Errorf("SFTP delete %s: %w", remotePath, err)
	}
	return ctx.Err()
}

func (f *FS) Chmod(ctx context.Context, remotePath string, mode fs.FileMode) error {
	remotePath, err := f.real(ctx, remotePath)
	if err != nil {
		return err
	}
	if err := f.client.Chmod(remotePath, mode); err != nil {
		return fmt.Errorf("SFTP chmod %s: %w", remotePath, err)
	}
	return ctx.Err()
}

func (f *FS) Checksum(ctx context.Context, remotePath, algorithm string) (string, error) {
	var digest hash.Hash
	switch strings.ToLower(algorithm) {
	case "md5":
		digest = md5.New()
	case "sha256":
		digest = sha256.New()
	default:
		return "", fmt.Errorf("unsupported checksum algorithm %q (use md5 or sha256)", algorithm)
	}
	reader, err := f.OpenRead(ctx, remotePath)
	if err != nil {
		return "", err
	}
	defer reader.Close()
	if _, err := copyBuffer(ctx, digest, reader, nil, nil, -1); err != nil {
		return "", fmt.Errorf("SFTP checksum %s: %w", remotePath, err)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func (f *FS) Exists(ctx context.Context, remotePath string) (bool, error) {
	_, err := f.Size(ctx, remotePath)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) || os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func (f *FS) Size(ctx context.Context, remotePath string) (int64, error) {
	remotePath, err := f.real(ctx, remotePath)
	if err != nil {
		return 0, err
	}
	info, err := f.client.Stat(remotePath)
	if err != nil {
		return 0, fmt.Errorf("SFTP stat %s: %w", remotePath, err)
	}
	return info.Size(), ctx.Err()
}

func (f *FS) OpenRead(ctx context.Context, remotePath string) (base.RemoteReader, error) {
	remotePath, err := f.real(ctx, remotePath)
	if err != nil {
		return nil, err
	}
	file, err := f.client.Open(remotePath)
	if err != nil {
		return nil, fmt.Errorf("SFTP open %s: %w", remotePath, err)
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("SFTP stat open file %s: %w", remotePath, err)
	}
	reader := &remoteReader{ctx: ctx, file: file, size: info.Size()}
	reader.stop = context.AfterFunc(ctx, func() { file.Close() })
	return reader, nil
}

func (f *FS) OpenWrite(ctx context.Context, remotePath string, append bool) (base.RemoteWriter, error) {
	remotePath, err := f.real(ctx, remotePath)
	if err != nil {
		return nil, err
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if append {
		flags = os.O_WRONLY | os.O_CREATE | os.O_APPEND
	}
	file, err := f.client.OpenFile(remotePath, flags)
	if err != nil {
		return nil, fmt.Errorf("SFTP open for write %s: %w", remotePath, err)
	}
	if append {
		if _, err := file.Seek(0, io.SeekEnd); err != nil {
			file.Close()
			return nil, fmt.Errorf("SFTP seek append %s: %w", remotePath, err)
		}
	}
	writer := &remoteWriter{ctx: ctx, file: file}
	writer.stop = context.AfterFunc(ctx, func() { file.Close() })
	return writer, nil
}

func (f *FS) backup(ctx context.Context, remotePath string, mode fs.FileMode) error {
	backupPath := remotePath + ".nexterm-bak"
	source, err := f.OpenRead(ctx, remotePath)
	if err != nil {
		return fmt.Errorf("open SFTP backup source: %w", err)
	}
	defer source.Close()
	target, err := f.OpenWrite(ctx, backupPath, false)
	if err != nil {
		return fmt.Errorf("create SFTP backup: %w", err)
	}
	defer target.Close()
	if _, err := copyBuffer(ctx, target, source, nil, nil, -1); err != nil {
		return fmt.Errorf("copy SFTP backup: %w", err)
	}
	if err := target.Sync(); err != nil {
		return fmt.Errorf("sync SFTP backup: %w", err)
	}
	if err := f.client.Chmod(backupPath, mode.Perm()); err != nil {
		return fmt.Errorf("preserve SFTP backup mode: %w", err)
	}
	if err := target.Close(); err != nil {
		return fmt.Errorf("close SFTP backup: %w", err)
	}
	return nil
}

func (f *FS) real(ctx context.Context, remotePath string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if remotePath != "~" && !strings.HasPrefix(remotePath, "~/") {
		return remotePath, nil
	}
	home, err := f.client.RealPath(".")
	if err != nil {
		return "", fmt.Errorf("resolve SFTP home directory: %w", err)
	}
	if remotePath == "~" {
		return home, nil
	}
	return strings.TrimSuffix(home, "/") + "/" + strings.TrimPrefix(remotePath, "~/"), nil
}

func (f *FS) realPair(ctx context.Context, from, to string) (string, string, error) {
	from, err := f.real(ctx, from)
	if err != nil {
		return "", "", err
	}
	to, err = f.real(ctx, to)
	return from, to, err
}

func fileEntry(info os.FileInfo, remotePath string) base.FileEntry {
	kind := base.FileFile
	switch {
	case info.IsDir():
		kind = base.FileDirectory
	case info.Mode()&fs.ModeSymlink != 0:
		kind = base.FileSymlink
	case !info.Mode().IsRegular():
		kind = base.FileOther
	}
	entry := base.FileEntry{
		Name:    info.Name(),
		Path:    remotePath,
		Kind:    kind,
		Size:    info.Size(),
		Mode:    info.Mode(),
		ModTime: info.ModTime(),
	}
	if stat, ok := info.Sys().(*sftp.FileStat); ok {
		entry.Owner = strconv.FormatUint(uint64(stat.UID), 10)
		entry.Group = strconv.FormatUint(uint64(stat.GID), 10)
	}
	return entry
}

type remoteReader struct {
	ctx       context.Context
	file      *sftp.File
	size      int64
	stop      func() bool
	closeOnce sync.Once
	closeErr  error
}

func (r *remoteReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.file.Read(p)
}

func (r *remoteReader) Size() int64 {
	return r.size
}

func (r *remoteReader) Seek(offset int64, whence int) (int64, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.file.Seek(offset, whence)
}

func (r *remoteReader) Close() error {
	r.closeOnce.Do(func() {
		r.stop()
		r.closeErr = r.file.Close()
	})
	return r.closeErr
}

type remoteWriter struct {
	ctx       context.Context
	file      *sftp.File
	stop      func() bool
	closeOnce sync.Once
	closeErr  error
}

func (w *remoteWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.file.Write(p)
}

func (w *remoteWriter) Sync() error {
	if err := w.ctx.Err(); err != nil {
		return err
	}
	err := w.file.Sync()
	var status *sftp.StatusError
	if errors.As(err, &status) && status.FxCode() == sftp.ErrSSHFxOpUnsupported {
		return nil
	}
	return err
}

func (w *remoteWriter) Close() error {
	w.closeOnce.Do(func() {
		w.stop()
		w.closeErr = w.file.Close()
	})
	return w.closeErr
}

func writeAll(ctx context.Context, writer io.Writer, data []byte) error {
	for len(data) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := writer.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func copyBuffer(ctx context.Context, dst io.Writer, src io.Reader, progress func(int64), buffer []byte, remaining int64) (int64, error) {
	if len(buffer) == 0 {
		buffer = make([]byte, 256<<10)
	}
	var transferred int64
	for remaining < 0 || transferred < remaining {
		if err := ctx.Err(); err != nil {
			return transferred, err
		}
		chunk := buffer
		if remaining >= 0 {
			left := remaining - transferred
			if left <= 0 {
				break
			}
			if int64(len(chunk)) > left {
				chunk = chunk[:left]
			}
		}
		n, readErr := src.Read(chunk)
		if n > 0 {
			written, writeErr := dst.Write(chunk[:n])
			transferred += int64(written)
			if writeErr != nil {
				return transferred, writeErr
			}
			if written != n {
				return transferred, io.ErrShortWrite
			}
			if progress != nil {
				progress(int64(written))
			}
		}
		if readErr == io.EOF {
			if remaining >= 0 && transferred < remaining {
				return transferred, io.ErrUnexpectedEOF
			}
			return transferred, nil
		}
		if readErr != nil {
			return transferred, readErr
		}
		if n == 0 {
			time.Sleep(time.Millisecond)
		}
	}
	return transferred, nil
}
