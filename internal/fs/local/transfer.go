package local

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

const transferChunkSize = 256 * 1024

type Progress struct {
	TaskID      string `json:"taskId"`
	Transferred int64  `json:"transferred"`
	Total       int64  `json:"total"`
	Done        bool   `json:"done"`
	Error       string `json:"error,omitempty"`
}

type TransferOptions struct {
	Resume           bool
	TaskID           string
	ProgressInterval time.Duration
	Progress         func(Progress)
}

type localPathMapper interface {
	LocalPath(path string) string
}

type currentSizer interface {
	CurrentSize() (int64, error)
}

func Upload(ctx context.Context, localPath string, destination base.FileSystem, remotePath string, options TransferOptions) (transferred int64, err error) {
	var total int64
	defer func() {
		if err != nil && ctx.Err() == nil {
			reportTransferFailure(options, transferred, total, err)
		}
	}()
	if mapped, ok := destination.(localPathMapper); ok {
		if err := rejectSameFile(localPath, mapped.LocalPath(remotePath)); err != nil {
			return 0, err
		}
	}
	source, err := os.Open(localPath)
	if err != nil {
		return 0, err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("upload source %s is not a regular file", localPath)
	}
	total = info.Size()
	var offset int64
	targetExists := false
	if options.Resume {
		size, err := destination.Size(ctx, remotePath)
		switch {
		case err == nil:
			if size < 0 {
				return 0, fmt.Errorf("remote resume target has a negative size")
			}
			if size > total {
				return 0, fmt.Errorf("remote resume target is larger than source (%d > %d)", size, total)
			}
			offset = size
			targetExists = true
		case errors.Is(err, os.ErrNotExist):
		default:
			return 0, err
		}
	}
	var digest hash.Hash
	if options.Resume {
		digest = sha256.New()
		if targetExists && offset > 0 {
			existing, err := destination.OpenRead(ctx, remotePath)
			if err != nil {
				return offset, err
			}
			verifyErr := verifyResumePrefix(ctx, source, existing, offset, digest)
			closeErr := existing.Close()
			if verifyErr != nil {
				return offset, verifyErr
			}
			if closeErr != nil {
				return offset, closeErr
			}
		}
		if err := ctx.Err(); err != nil {
			return offset, err
		}
	}
	if err := ensureRemoteParent(ctx, destination, remotePath); err != nil {
		return offset, err
	}
	target, err := destination.OpenWrite(ctx, remotePath, offset > 0)
	if err != nil {
		return offset, err
	}
	if options.Resume {
		if err := verifyResumeOffset(target, offset); err != nil {
			_ = target.Close()
			return offset, err
		}
	}
	var reader io.Reader = source
	if digest != nil {
		reader = io.TeeReader(source, digest)
	}
	transferred, err = finishTransfer(ctx, reader, target, offset, total, options)
	if err != nil {
		return transferred, err
	}
	if !options.Resume {
		reportTransferProgress(options, transferred, total, true)
		return transferred, nil
	}
	current, err := source.Stat()
	if err != nil {
		return transferred, err
	}
	if !current.Mode().IsRegular() || current.Size() != total || !current.ModTime().Equal(info.ModTime()) {
		return transferred, errors.New("upload source changed during transfer")
	}
	size, err := destination.Size(ctx, remotePath)
	if err != nil {
		return transferred, err
	}
	if size != total {
		return transferred, fmt.Errorf("uploaded file size changed during transfer: got %d, want %d", size, total)
	}
	if options.Resume {
		if err := verifyFileChecksum(ctx, destination, remotePath, hex.EncodeToString(digest.Sum(nil))); err != nil {
			return transferred, err
		}
	}
	if err := ctx.Err(); err != nil {
		return transferred, err
	}
	reportTransferProgress(options, transferred, total, true)
	return transferred, nil
}

func ensureRemoteParent(ctx context.Context, destination base.FileSystem, remotePath string) error {
	_, err := ensureRemoteDirs(ctx, destination, parentDir(remotePath))
	return err
}

// EnsureRemoteDir creates the full chain for a remote directory path with
// single-level Mkdir calls. An existing directory is a no-op; a file occupying
// the target or any parent component keeps its Mkdir failure.
func EnsureRemoteDir(ctx context.Context, destination base.FileSystem, dir string) error {
	if dir == "" {
		return fmt.Errorf("remote directory path is empty")
	}
	trimmed := strings.TrimRight(dir, `/\`)
	if trimmed == "" {
		return nil
	}
	existed, err := ensureRemoteDirs(ctx, destination, trimmed)
	if err != nil {
		return err
	}
	if !existed {
		return nil
	}
	return verifyRemoteDir(ctx, destination, trimmed)
}

func ensureRemoteDirs(ctx context.Context, destination base.FileSystem, dir string) (bool, error) {
	var missing []string
	for current := dir; current != ""; current = parentDir(current) {
		exists, err := destination.Exists(ctx, current)
		if err != nil {
			return false, err
		}
		if exists {
			break
		}
		missing = append(missing, current)
	}
	for index := len(missing) - 1; index >= 0; index-- {
		component := missing[index]
		if err := destination.Mkdir(ctx, component); err != nil {
			if exists, statErr := destination.Exists(ctx, component); statErr == nil && exists {
				continue
			}
			return false, err
		}
	}
	return dir != "" && len(missing) == 0, nil
}

func verifyRemoteDir(ctx context.Context, destination base.FileSystem, dir string) error {
	parent := parentDir(dir)
	if parent == "" {
		parent = "."
	}
	entries, err := destination.List(ctx, parent)
	if err != nil {
		return err
	}
	name := dir[strings.LastIndexAny(dir, `/\`)+1:]
	for _, entry := range entries {
		if entry.Name != name {
			continue
		}
		if entry.Kind == base.FileDirectory || entry.Kind == base.FileSymlink {
			return nil
		}
		return fmt.Errorf("remote path %s exists and is not a directory", dir)
	}
	return nil
}

func parentDir(remotePath string) string {
	trimmed := strings.TrimRight(remotePath, `/\`)
	if trimmed == "" {
		return ""
	}
	index := strings.LastIndexAny(trimmed, `/\`)
	if index < 0 {
		return ""
	}
	if index == 0 || trimmed[index-1] == ':' {
		return trimmed[:index+1]
	}
	return trimmed[:index]
}

func Download(ctx context.Context, source base.FileSystem, remotePath, localPath string, options TransferOptions) (transferred int64, err error) {
	var total int64
	defer func() {
		if err != nil && ctx.Err() == nil {
			reportTransferFailure(options, transferred, total, err)
		}
	}()
	destination := New()
	targetPath := destination.LocalPath(localPath)
	if mapped, ok := source.(localPathMapper); ok {
		if err := rejectSameFile(mapped.LocalPath(remotePath), targetPath); err != nil {
			return 0, err
		}
	}
	remote, err := source.OpenRead(ctx, remotePath)
	if err != nil {
		return 0, err
	}
	defer remote.Close()
	size := remote.Size()
	if size < 0 {
		return 0, fmt.Errorf("download source %s has a negative size", remotePath)
	}
	total = size
	var offset int64
	targetExists := false
	var initialSource base.FileEntry
	sourceVersioned := false
	if options.Resume {
		initialSource, sourceVersioned, err = remoteFileMetadata(ctx, source, remotePath, true)
		if err != nil {
			return 0, err
		}
		if sourceVersioned && initialSource.Size != total {
			return 0, errors.New("download source changed during transfer")
		}
		info, err := os.Stat(targetPath)
		switch {
		case err == nil:
			if !info.Mode().IsRegular() {
				return 0, fmt.Errorf("download resume target is not a regular file")
			}
			if info.Size() > total {
				return 0, fmt.Errorf("local resume target is larger than remote file (%d > %d)", info.Size(), total)
			}
			offset = info.Size()
			targetExists = true
		case errors.Is(err, os.ErrNotExist):
		default:
			return 0, err
		}
	}
	var digest hash.Hash
	if options.Resume {
		digest = sha256.New()
		if targetExists && offset > 0 {
			existing, err := os.Open(targetPath)
			if err != nil {
				return offset, err
			}
			verifyErr := verifyResumePrefix(ctx, remote, existing, offset, digest)
			closeErr := existing.Close()
			if verifyErr != nil {
				return offset, verifyErr
			}
			if closeErr != nil {
				return offset, closeErr
			}
		}
		if err := ctx.Err(); err != nil {
			return offset, err
		}
	}
	local, err := destination.OpenWrite(ctx, localPath, offset > 0)
	if err != nil {
		return offset, err
	}
	if options.Resume {
		if err := verifyResumeOffset(local, offset); err != nil {
			_ = local.Close()
			return offset, err
		}
	}
	var reader io.Reader = remote
	if digest != nil {
		reader = io.TeeReader(remote, digest)
	}
	transferred, err = finishTransfer(ctx, reader, local, offset, total, options)
	if err != nil {
		return transferred, err
	}
	if !options.Resume {
		reportTransferProgress(options, transferred, total, true)
		return transferred, nil
	}
	currentSize, err := source.Size(ctx, remotePath)
	if err != nil {
		return transferred, err
	}
	if currentSize != total {
		return transferred, errors.New("download source changed during transfer")
	}
	if sourceVersioned {
		current, available, err := remoteFileMetadata(ctx, source, remotePath, false)
		if err != nil {
			return transferred, err
		}
		if !available {
			return transferred, errors.New("download source metadata became unavailable during transfer")
		}
		if current.Size != total || !current.ModTime.Equal(initialSource.ModTime) {
			return transferred, errors.New("download source changed during transfer")
		}
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		return transferred, err
	}
	if !info.Mode().IsRegular() {
		return transferred, errors.New("downloaded file is not a regular file")
	}
	if info.Size() != total {
		return transferred, fmt.Errorf("downloaded file size changed during transfer: got %d, want %d", info.Size(), total)
	}
	if options.Resume {
		if err := verifyFileChecksum(ctx, destination, targetPath, hex.EncodeToString(digest.Sum(nil))); err != nil {
			return transferred, err
		}
	}
	if err := ctx.Err(); err != nil {
		return transferred, err
	}
	reportTransferProgress(options, transferred, total, true)
	return transferred, nil
}

func verifyResumeOffset(target base.RemoteWriter, offset int64) error {
	sizer, ok := target.(currentSizer)
	if !ok {
		return nil
	}
	size, err := sizer.CurrentSize()
	if err != nil {
		return err
	}
	if size != offset {
		return fmt.Errorf("resume offset changed: got %d bytes, expected %d", size, offset)
	}
	return nil
}

func remoteFileMetadata(ctx context.Context, filesystem base.FileSystem, remotePath string, allowUnavailable bool) (base.FileEntry, bool, error) {
	parent := parentDir(remotePath)
	if parent == "" {
		parent = "."
	}
	entries, err := filesystem.List(ctx, parent)
	if err != nil {
		if allowUnavailable {
			return base.FileEntry{}, false, nil
		}
		return base.FileEntry{}, false, err
	}
	name := remotePath[strings.LastIndexAny(remotePath, `/\`)+1:]
	for _, entry := range entries {
		if entry.Name != name {
			continue
		}
		if entry.Kind == base.FileSymlink {
			return entry, false, nil
		}
		if entry.Kind != base.FileFile {
			return base.FileEntry{}, false, fmt.Errorf("download source %s is not a regular file", remotePath)
		}
		return entry, true, nil
	}
	if allowUnavailable {
		return base.FileEntry{}, false, nil
	}
	return base.FileEntry{}, false, fmt.Errorf("download source %s is missing from its parent directory", remotePath)
}

func verifyResumePrefix(ctx context.Context, source, target io.Reader, offset int64, digest hash.Hash) error {
	sourceBuffer := make([]byte, transferChunkSize)
	targetBuffer := make([]byte, transferChunkSize)
	remaining := offset
	for remaining > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		chunkSize := int64(len(sourceBuffer))
		if chunkSize > remaining {
			chunkSize = remaining
		}
		sourceChunk := sourceBuffer[:chunkSize]
		targetChunk := targetBuffer[:chunkSize]
		if _, err := io.ReadFull(source, sourceChunk); err != nil {
			if errors.Is(err, io.EOF) {
				return io.ErrUnexpectedEOF
			}
			return err
		}
		if _, err := io.ReadFull(target, targetChunk); err != nil {
			if errors.Is(err, io.EOF) {
				return io.ErrUnexpectedEOF
			}
			return err
		}
		if !bytes.Equal(sourceChunk, targetChunk) {
			return errors.New("resume target content does not match source; restart without resume")
		}
		_, _ = digest.Write(sourceChunk)
		remaining -= chunkSize
	}
	return ctx.Err()
}

func verifyFileChecksum(ctx context.Context, filesystem base.FileSystem, path, expected string) error {
	actual, err := filesystem.Checksum(ctx, path, "sha256")
	if errors.Is(err, base.ErrUnsupported) {
		actual, err = sequentialFileChecksum(ctx, filesystem, path)
	}
	if err != nil {
		return err
	}
	if !strings.EqualFold(actual, expected) {
		return fmt.Errorf("final checksum mismatch: got %s, want %s", actual, expected)
	}
	return nil
}

func sequentialFileChecksum(ctx context.Context, filesystem base.FileSystem, path string) (sum string, err error) {
	reader, err := filesystem.OpenRead(ctx, path)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := reader.Close(); err == nil && closeErr != nil {
			sum = ""
			err = closeErr
		}
	}()
	digest := sha256.New()
	buffer := make([]byte, transferChunkSize)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, readErr := reader.Read(buffer)
		if n < 0 || n > len(buffer) {
			return "", fmt.Errorf("invalid read count %d", n)
		}
		if n > 0 {
			_, _ = digest.Write(buffer[:n])
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", readErr
		}
		if n == 0 {
			time.Sleep(time.Millisecond)
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func finishTransfer(ctx context.Context, source io.Reader, target base.RemoteWriter, offset, total int64, options TransferOptions) (transferred int64, err error) {
	closed := false
	defer func() {
		if !closed {
			_ = target.Close()
		}
	}()
	transferred = offset
	if offset < 0 || total < 0 || offset > total {
		return transferred, fmt.Errorf("invalid transfer bounds %d..%d", offset, total)
	}
	interval := options.ProgressInterval
	if interval == 0 {
		interval = 200 * time.Millisecond
	}
	lastReport := time.Now()
	buffer := make([]byte, transferChunkSize)
	for transferred < total {
		if err := ctx.Err(); err != nil {
			return transferred, err
		}
		remaining := total - transferred
		chunk := buffer
		if int64(len(chunk)) > remaining {
			chunk = chunk[:remaining]
		}
		n, readErr := source.Read(chunk)
		if n < 0 || n > len(chunk) {
			return transferred, fmt.Errorf("invalid read count %d", n)
		}
		if n > 0 {
			written, writeErr := writeAll(target, chunk[:n])
			transferred += written
			if writeErr != nil {
				return transferred, writeErr
			}
			if options.Progress != nil && interval > 0 && time.Since(lastReport) >= interval {
				reportTransferProgress(options, transferred, total, false)
				lastReport = time.Now()
			}
		}
		if errors.Is(readErr, io.EOF) {
			if transferred < total {
				return transferred, io.ErrUnexpectedEOF
			}
			break
		}
		if readErr != nil {
			return transferred, readErr
		}
		if n == 0 {
			time.Sleep(time.Millisecond)
		}
	}
	if err := target.Sync(); err != nil {
		return transferred, err
	}
	if err := target.Close(); err != nil {
		closed = true
		return transferred, err
	}
	closed = true
	return transferred, nil
}

func reportTransferProgress(options TransferOptions, transferred, total int64, done bool) {
	if options.Progress != nil {
		options.Progress(Progress{TaskID: options.TaskID, Transferred: transferred, Total: total, Done: done})
	}
}

func reportTransferFailure(options TransferOptions, transferred, total int64, err error) {
	if options.Progress != nil && err != nil {
		options.Progress(Progress{TaskID: options.TaskID, Transferred: transferred, Total: total, Done: true, Error: err.Error()})
	}
}

func writeAll(writer io.Writer, data []byte) (int64, error) {
	var written int64
	for len(data) > 0 {
		n, err := writer.Write(data)
		if n < 0 || n > len(data) {
			return written, fmt.Errorf("invalid write count %d", n)
		}
		if n > 0 {
			written += int64(n)
			data = data[n:]
		}
		if err != nil {
			return written, err
		}
		if n == 0 {
			return written, io.ErrShortWrite
		}
	}
	return written, nil
}

func rejectSameFile(sourcePath, targetPath string) error {
	source, err := os.Stat(sourcePath)
	if err != nil {
		return err
	}
	target, err := os.Stat(targetPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if os.SameFile(source, target) {
		return fmt.Errorf("source and destination refer to the same file")
	}
	return nil
}
