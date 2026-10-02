package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"
)

type Progress struct {
	Transferred int64
	Total       int64
	Done        bool
}

type TransferOptions struct {
	Resume           bool
	ProgressInterval time.Duration
	BufferSize       int
	Progress         func(Progress)
}

func (o TransferOptions) normalized() (TransferOptions, error) {
	if o.ProgressInterval == 0 {
		o.ProgressInterval = 200 * time.Millisecond
	}
	if o.BufferSize == 0 {
		o.BufferSize = 256 << 10
	}
	if o.ProgressInterval < 0 || o.BufferSize < 0 {
		return TransferOptions{}, fmt.Errorf("invalid transfer options")
	}
	return o, nil
}

func (f *FS) Upload(ctx context.Context, localPath, remotePath string, options TransferOptions) (int64, error) {
	options, err := options.normalized()
	if err != nil {
		return 0, err
	}
	local, err := os.Open(localPath)
	if err != nil {
		return 0, err
	}
	defer local.Close()
	info, err := local.Stat()
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("upload source is not a regular file")
	}
	total := info.Size()
	var offset int64
	targetExists := false
	if options.Resume {
		size, err := f.Size(ctx, remotePath)
		switch {
		case err == nil:
			if size > total {
				return 0, fmt.Errorf("remote resume target is larger than source (%d > %d)", size, total)
			}
			offset = size
			targetExists = true
		case errors.Is(err, fs.ErrNotExist) || os.IsNotExist(err):
		default:
			return 0, err
		}
	}
	if targetExists && offset == total {
		reportProgress(options, offset, total, true)
		return offset, nil
	}
	if _, err := local.Seek(offset, io.SeekStart); err != nil {
		return offset, err
	}
	remote, err := f.OpenWrite(ctx, remotePath, offset > 0)
	if err != nil {
		return offset, err
	}
	defer remote.Close()
	stop := context.AfterFunc(ctx, func() { local.Close() })
	defer stop()
	reporter := newProgressReporter(options, offset, total)
	written, err := copyBuffer(ctx, remote, local, reporter.advance, make([]byte, options.BufferSize), total-offset)
	transferred := offset + written
	if err != nil {
		return transferred, fmt.Errorf("upload %s: %w", remotePath, err)
	}
	if err := remote.Sync(); err != nil {
		return transferred, fmt.Errorf("sync uploaded file: %w", err)
	}
	if err := remote.Close(); err != nil {
		return transferred, fmt.Errorf("close uploaded file: %w", err)
	}
	reporter.finish()
	return transferred, nil
}

func (f *FS) Download(ctx context.Context, remotePath, localPath string, options TransferOptions) (int64, error) {
	options, err := options.normalized()
	if err != nil {
		return 0, err
	}
	remote, err := f.OpenRead(ctx, remotePath)
	if err != nil {
		return 0, err
	}
	defer remote.Close()
	total := remote.Size()
	var offset int64
	targetExists := false
	if options.Resume {
		info, err := os.Stat(localPath)
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
		case errors.Is(err, fs.ErrNotExist):
		default:
			return 0, err
		}
	}
	if targetExists && offset == total {
		reportProgress(options, offset, total, true)
		return offset, nil
	}
	seeker, ok := remote.(io.Seeker)
	if !ok {
		return offset, fmt.Errorf("SFTP reader does not support resume seeking")
	}
	if _, err := seeker.Seek(offset, io.SeekStart); err != nil {
		return offset, fmt.Errorf("seek remote download: %w", err)
	}
	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if offset > 0 {
		flags = os.O_CREATE | os.O_WRONLY
	}
	local, err := os.OpenFile(localPath, flags, 0o600)
	if err != nil {
		return offset, err
	}
	defer local.Close()
	if offset > 0 {
		if _, err := local.Seek(offset, io.SeekStart); err != nil {
			return offset, err
		}
	}
	reporter := newProgressReporter(options, offset, total)
	written, err := copyBuffer(ctx, local, remote, reporter.advance, make([]byte, options.BufferSize), total-offset)
	transferred := offset + written
	if err != nil {
		return transferred, fmt.Errorf("download %s: %w", remotePath, err)
	}
	if err := local.Sync(); err != nil {
		return transferred, fmt.Errorf("sync downloaded file: %w", err)
	}
	if err := local.Close(); err != nil {
		return transferred, fmt.Errorf("close downloaded file: %w", err)
	}
	reporter.finish()
	return transferred, nil
}

type progressReporter struct {
	options     TransferOptions
	transferred int64
	total       int64
	last        time.Time
}

func newProgressReporter(options TransferOptions, transferred, total int64) *progressReporter {
	reporter := &progressReporter{options: options, transferred: transferred, total: total, last: time.Now()}
	if transferred > 0 {
		reportProgress(options, transferred, total, false)
	}
	return reporter
}

func (r *progressReporter) advance(amount int64) {
	r.transferred += amount
	if r.options.Progress != nil && time.Since(r.last) >= r.options.ProgressInterval {
		r.options.Progress(Progress{Transferred: r.transferred, Total: r.total})
		r.last = time.Now()
	}
}

func (r *progressReporter) finish() {
	reportProgress(r.options, r.transferred, r.total, true)
}

func reportProgress(options TransferOptions, transferred, total int64, done bool) {
	if options.Progress != nil {
		options.Progress(Progress{Transferred: transferred, Total: total, Done: done})
	}
}
