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

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/pkg/sftp"
)

type Executor interface {
	Exec(context.Context, string, base.ExecOptions) (base.ExecResult, error)
}

type FS struct {
	client                   *sftp.Client
	executor                 Executor
	conditionalLink          func(*sftp.Client, string, string) error
	conditionalPreVerifyHook func()
	conditionalPreCommitHook func()
}

// ClientOptions returns the SFTP client options NexTerm uses everywhere. Concurrent
// writes overlap round trips so one large Write is not limited to a single 32 KiB
// request per RTT; callers must not rely on the written range being contiguous
// until the Write has returned successfully (see Upload resume verification).
func ClientOptions() []sftp.ClientOption {
	return []sftp.ClientOption{
		sftp.UseConcurrentWrites(true),
		sftp.MaxConcurrentRequestsPerFile(maxConcurrentRequests),
	}
}

const (
	maxConcurrentRequests = 64
	// listLinkConcurrency bounds in-flight ReadLink requests while listing.
	listLinkConcurrency = 32
	// sftpPacketSize is the default pkg/sftp payload size; a Write of at most this
	// size is always a single request.
	sftpPacketSize = 32 << 10
)

func New(client *sftp.Client, executor Executor) *FS {
	return &FS{
		client:   client,
		executor: executor,
		conditionalLink: func(client *sftp.Client, oldname, newname string) error {
			return client.Link(oldname, newname)
		},
	}
}

func (f *FS) List(ctx context.Context, remotePath string) ([]base.FileEntry, error) {
	remotePath, err := f.real(ctx, remotePath)
	if err != nil {
		return nil, err
	}
	infos, err := f.client.ReadDirContext(ctx, remotePath)
	if err != nil {
		return nil, fmt.Errorf("SFTP list %s: %w", remotePath, err)
	}
	entries := make([]base.FileEntry, 0, len(infos))
	for _, info := range infos {
		if info.Name() == "." || info.Name() == ".." {
			continue
		}
		entries = append(entries, fileEntry(info, path.Join(remotePath, info.Name())))
	}
	f.resolveSymlinks(ctx, entries)
	sort.Slice(entries, func(i, j int) bool {
		if (entries[i].Kind == base.FileDirectory) != (entries[j].Kind == base.FileDirectory) {
			return entries[i].Kind == base.FileDirectory
		}
		return entries[i].Name < entries[j].Name
	})
	return entries, ctx.Err()
}

// resolveSymlinks fills SymlinkTarget for symlink entries. The SFTP client
// multiplexes requests, so overlapping the ReadLink calls turns N round trips
// into roughly N/listLinkConcurrency. Failed lookups leave the target empty.
func (f *FS) resolveSymlinks(ctx context.Context, entries []base.FileEntry) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, listLinkConcurrency)
	for i := range entries {
		if entries[i].Kind != base.FileSymlink {
			continue
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return
		}
		wg.Add(1)
		go func(entry *base.FileEntry) {
			defer wg.Done()
			defer func() { <-sem }()
			if target, err := f.client.ReadLink(entry.Path); err == nil {
				entry.SymlinkTarget = target
			}
		}(&entries[i])
	}
	wg.Wait()
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
		case errors.Is(err, fs.ErrNotExist):
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

func (f *FS) AtomicRename(ctx context.Context, from, to string) error {
	from, to, err := f.realPair(ctx, from, to)
	if err != nil {
		return err
	}
	_, statErr := f.client.Stat(to)
	switch {
	case errors.Is(statErr, fs.ErrNotExist):
		if err := f.client.Rename(from, to); err != nil {
			return fmt.Errorf("SFTP rename %s to %s: %w", from, to, err)
		}
		return ctx.Err()
	case statErr != nil:
		return fmt.Errorf("SFTP stat rename target %s: %w", to, statErr)
	}
	if err := f.client.PosixRename(from, to); err != nil {
		return fmt.Errorf("SFTP atomic rename %s to %s: %w", from, to, err)
	}
	return ctx.Err()
}

func (f *FS) Delete(ctx context.Context, remotePath string, directory bool) error {
	remotePath, err := f.real(ctx, remotePath)
	if err != nil {
		return err
	}
	if directory {
		var info os.FileInfo
		if info, err = f.client.Lstat(remotePath); err == nil {
			if info.Mode()&fs.ModeSymlink != 0 {
				err = f.client.Remove(remotePath)
			} else {
				err = f.removeTree(ctx, remotePath)
			}
		}
	} else {
		err = f.client.Remove(remotePath)
	}
	if err != nil {
		return fmt.Errorf("SFTP delete %s: %w", remotePath, err)
	}
	return ctx.Err()
}

// removeTree deletes a directory bottom-up. ReadDir entries carry Lstat
// semantics, so a symlink to a directory is removed as a link, never
// recursed into.
func (f *FS) removeTree(ctx context.Context, remotePath string) error {
	infos, err := f.client.ReadDir(remotePath)
	if err != nil {
		return err
	}
	for _, info := range infos {
		if err := ctx.Err(); err != nil {
			return err
		}
		if info.Name() == "." || info.Name() == ".." {
			continue
		}
		child := path.Join(remotePath, info.Name())
		if info.IsDir() {
			if err := f.removeTree(ctx, child); err != nil {
				return err
			}
			continue
		}
		if err := f.client.Remove(child); err != nil {
			return err
		}
	}
	return f.client.RemoveDirectory(remotePath)
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
	if errors.Is(err, fs.ErrNotExist) {
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

func (f *FS) FileMode(ctx context.Context, remotePath string) (fs.FileMode, error) {
	remotePath, err := f.real(ctx, remotePath)
	if err != nil {
		return 0, err
	}
	info, err := f.client.Stat(remotePath)
	if err != nil {
		return 0, fmt.Errorf("SFTP stat %s: %w", remotePath, err)
	}
	return info.Mode(), ctx.Err()
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
	_, statErr := f.client.Stat(remotePath)
	newFile := errors.Is(statErr, fs.ErrNotExist)
	if statErr != nil && !newFile {
		return nil, fmt.Errorf("SFTP stat before write %s: %w", remotePath, statErr)
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if append {
		flags = os.O_WRONLY | os.O_CREATE | os.O_APPEND
	}
	file, err := f.client.OpenFile(remotePath, flags)
	if err != nil {
		return nil, fmt.Errorf("SFTP open for write %s: %w", remotePath, err)
	}
	if newFile {
		if err := file.Chmod(0o600); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("SFTP restrict new file %s: %w", remotePath, err)
		}
	}
	if append {
		if _, err := file.Seek(0, io.SeekEnd); err != nil {
			file.Close()
			return nil, fmt.Errorf("SFTP seek append %s: %w", remotePath, err)
		}
	}
	// O_APPEND servers ignore request offsets, so concurrent (reorderable) chunks
	// would corrupt the file; appenders always write one request at a time.
	writer := &remoteWriter{ctx: ctx, file: file, sequential: append}
	writer.stop = context.AfterFunc(ctx, func() { file.Close() })
	return writer, nil
}

// openWriteAt opens an existing remote file for writing at offset, discarding
// everything after it. It deliberately avoids O_APPEND so concurrent chunk
// writes land at their own offsets.
func (f *FS) openWriteAt(ctx context.Context, remotePath string, offset int64) (*remoteWriter, error) {
	// A non-zero offset only makes sense for a file that still exists; never recreate it behind the user's back.
	flags := os.O_WRONLY
	if offset == 0 {
		flags |= os.O_CREATE
	}
	file, err := f.client.OpenFile(remotePath, flags)
	if err != nil {
		return nil, fmt.Errorf("SFTP open for resume %s: %w", remotePath, err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("SFTP stat for resume %s: %w", remotePath, err)
	}
	switch size := info.Size(); {
	case size < offset:
		// The file shrank or was recreated after the resume offset was verified: not a safe resume.
		_ = file.Close()
		return nil, fmt.Errorf("SFTP resume %s: remote file changed (%d bytes, expected at least %d)", remotePath, size, offset)
	case size > offset:
		// Only a longer file needs truncating, so servers without setstat still resume a clean prefix.
		if err := file.Truncate(offset); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("SFTP truncate for resume %s: %w", remotePath, err)
		}
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("SFTP seek for resume %s: %w", remotePath, err)
	}
	writer := &remoteWriter{ctx: ctx, file: file, off: offset}
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

// sftpWriteFile is the subset of *sftp.File remoteWriter needs.
type sftpWriteFile interface {
	Write([]byte) (int, error)
	Seek(int64, int) (int64, error)
	Sync() error
	Close() error
}

type remoteWriter struct {
	ctx  context.Context
	file sftpWriteFile
	// off is the position of the next byte for non-append writers.
	off int64
	// sequential forces one request at a time: set for append writers and after
	// a concurrent Write was rejected by the server.
	sequential bool
	stop       func() bool
	closeOnce  sync.Once
	closeErr   error
}

func (w *remoteWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if w.sequential || len(p) <= sftpPacketSize {
		n, err := w.writeSequential(p)
		w.off += int64(n)
		return n, err
	}
	start := w.off
	n, err := w.file.Write(p)
	w.off += int64(n)
	if err == nil || w.ctx.Err() != nil {
		return n, err
	}
	// A server may refuse many outstanding requests or out-of-order offsets. Rewrite
	// the same range one request at a time; positional writes are idempotent.
	var status *sftp.StatusError
	if !errors.As(err, &status) {
		return n, err
	}
	w.sequential = true
	if _, seekErr := w.file.Seek(start, io.SeekStart); seekErr != nil {
		return n, err
	}
	w.off = start
	n, err = w.writeSequential(p)
	w.off += int64(n)
	return n, err
}

func (w *remoteWriter) writeSequential(p []byte) (int, error) {
	written := 0
	for written < len(p) {
		if err := w.ctx.Err(); err != nil {
			return written, err
		}
		end := written + sftpPacketSize
		if end > len(p) {
			end = len(p)
		}
		n, err := w.file.Write(p[written:end])
		written += n
		if err != nil {
			return written, err
		}
	}
	return written, nil
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
