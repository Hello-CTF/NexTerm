package local

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

const transferChunkSize = 256 * 1024

type Progress struct {
	TaskID      string `json:"taskId"`
	Transferred int64  `json:"transferred"`
	Total       int64  `json:"total"`
	Done        bool   `json:"done"`
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

func Upload(ctx context.Context, localPath string, destination base.FileSystem, remotePath string, options TransferOptions) (int64, error) {
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
	total := info.Size()
	var offset int64
	if options.Resume {
		size, err := destination.Size(ctx, remotePath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return 0, err
		}
		if err == nil && size > 0 && size < total {
			offset = size
		}
	}
	if _, err := source.Seek(offset, io.SeekStart); err != nil {
		return 0, err
	}
	target, err := destination.OpenWrite(ctx, remotePath, offset > 0)
	if err != nil {
		return 0, err
	}
	if err := verifyResumeOffset(target, offset); err != nil {
		_ = target.Close()
		return 0, err
	}
	return finishTransfer(ctx, source, target, offset, total, options)
}

func Download(ctx context.Context, source base.FileSystem, remotePath, localPath string, options TransferOptions) (int64, error) {
	destination := New()
	if mapped, ok := source.(localPathMapper); ok {
		if err := rejectSameFile(mapped.LocalPath(remotePath), localPath); err != nil {
			return 0, err
		}
	}
	remote, err := source.OpenRead(ctx, remotePath)
	if err != nil {
		return 0, err
	}
	defer remote.Close()
	total := remote.Size()
	if total < 0 {
		return 0, fmt.Errorf("download source %s has a negative size", remotePath)
	}
	var offset int64
	if options.Resume {
		size, err := destination.Size(ctx, localPath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return 0, err
		}
		if err == nil && size > 0 && size < total {
			offset = size
		}
	}
	if offset > 0 {
		if seeker, ok := remote.(io.Seeker); ok {
			if _, err := seeker.Seek(offset, io.SeekStart); err != nil {
				return 0, err
			}
		} else if _, err := io.CopyN(io.Discard, remote, offset); err != nil {
			return 0, err
		}
	}
	local, err := destination.OpenWrite(ctx, localPath, offset > 0)
	if err != nil {
		return 0, err
	}
	if err := verifyResumeOffset(local, offset); err != nil {
		_ = local.Close()
		return 0, err
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
	interval := options.ProgressInterval
	if interval == 0 {
		interval = 200 * time.Millisecond
	}
	lastReport := time.Now()
	buffer := make([]byte, transferChunkSize)
	for {
		if err := ctx.Err(); err != nil {
			return transferred, err
		}
		n, readErr := source.Read(buffer)
		if n > 0 {
			written, writeErr := writeAll(target, buffer[:n])
			transferred += written
			if writeErr != nil {
				return transferred, writeErr
			}
			if options.Progress != nil && interval > 0 && time.Since(lastReport) >= interval {
				options.Progress(Progress{TaskID: options.TaskID, Transferred: transferred, Total: total})
				lastReport = time.Now()
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return transferred, readErr
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
	if options.Progress != nil {
		options.Progress(Progress{TaskID: options.TaskID, Transferred: transferred, Total: total, Done: true})
	}
	return transferred, nil
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
