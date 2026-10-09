package local

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
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
	if targetExists && offset == total {
		reportTransferProgress(options, offset, total, true)
		return offset, nil
	}
	if _, err := source.Seek(offset, io.SeekStart); err != nil {
		return offset, err
	}
	target, err := destination.OpenWrite(ctx, remotePath, offset > 0)
	if err != nil {
		return offset, err
	}
	if err := verifyResumeOffset(target, offset); err != nil {
		_ = target.Close()
		return offset, err
	}
	return finishTransfer(ctx, source, target, offset, total, options)
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
	if options.Resume {
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
	if targetExists && offset == total {
		reportTransferProgress(options, offset, total, true)
		return offset, nil
	}
	if offset > 0 {
		if seeker, ok := remote.(io.Seeker); ok {
			if _, err := seeker.Seek(offset, io.SeekStart); err != nil {
				return offset, err
			}
		} else {
			if _, err := io.CopyN(io.Discard, remote, offset); err != nil {
				if errors.Is(err, io.EOF) {
					return offset, io.ErrUnexpectedEOF
				}
				return offset, err
			}
		}
	}
	local, err := destination.OpenWrite(ctx, localPath, offset > 0)
	if err != nil {
		return offset, err
	}
	if err := verifyResumeOffset(local, offset); err != nil {
		_ = local.Close()
		return offset, err
	}
	return finishTransfer(ctx, remote, local, offset, total, options)
}

func verifyResumeOffset(target base.RemoteWriter, offset int64) error {
	if offset == 0 {
		return nil
	}
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
	reportTransferProgress(options, transferred, total, true)
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
