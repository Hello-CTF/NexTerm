package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
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
	// MaxBytesPerSecond caps the upload rate so a background transfer cannot starve
	// interactive traffic sharing the same SSH connection. Zero means unlimited.
	MaxBytesPerSecond int64
}

// PaceWriter limits the sustained write rate of w. A non-positive rate returns w unchanged.
func PaceWriter(ctx context.Context, w io.Writer, bytesPerSecond int64) io.Writer {
	if bytesPerSecond <= 0 {
		return w
	}
	return &pacedWriter{ctx: ctx, w: w, rate: bytesPerSecond, start: time.Now()}
}

type pacedWriter struct {
	ctx   context.Context
	w     io.Writer
	rate  int64
	start time.Time
	sent  int64
}

func (p *pacedWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.sent += int64(n)
	// Sleep until the cumulative average falls back to the cap; bursts are repaid, not forgiven.
	due := p.start.Add(time.Duration(float64(p.sent) / float64(p.rate) * float64(time.Second)))
	if wait := time.Until(due); wait > 0 && err == nil {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-p.ctx.Done():
			return n, context.Cause(p.ctx)
		}
	}
	return n, err
}

func (o TransferOptions) normalized() (TransferOptions, error) {
	if o.ProgressInterval == 0 {
		o.ProgressInterval = 200 * time.Millisecond
	}
	if o.BufferSize == 0 {
		o.BufferSize = defaultTransferBufferSize
	}
	if o.ProgressInterval < 0 || o.BufferSize < 0 {
		return TransferOptions{}, fmt.Errorf("invalid transfer options")
	}
	return o, nil
}

// defaultTransferBufferSize is also the download read size: SFTP reads overlap
// round trips only within one large Read.
const defaultTransferBufferSize = 4 << 20

const (
	// uploadWriteSize is the largest single Write issued by Upload. Concurrent
	// chunk writes only overlap round trips within one Write, so it must be large;
	// it is also the bound on how much of the remote tail an interrupted upload can
	// leave with holes, which Resume verifies before trusting.
	uploadWriteSize = 4 << 20
	// pacedUploadWriteSize is the smallest Write of a rate-capped upload.
	pacedUploadWriteSize = 256 << 10
)

func uploadBufferSize(options TransferOptions) int {
	size := options.BufferSize
	switch {
	case size == 0 && options.MaxBytesPerSecond > 0:
		// About 125 ms of data per Write: large enough to keep the pipe full, small
		// enough that the cap is repaid quickly and cancellation stays prompt.
		size = int(min(max(options.MaxBytesPerSecond/8, pacedUploadWriteSize), uploadWriteSize))
	case size == 0:
		size = uploadWriteSize
	case size > uploadWriteSize:
		size = uploadWriteSize
	}
	return size
}

func (f *FS) Upload(ctx context.Context, localPath, remotePath string, options TransferOptions) (int64, error) {
	bufferSize := uploadBufferSize(options)
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
			// Concurrent writes can leave holes in the last window of an interrupted
			// upload, so the remote size alone is not a contiguity guarantee.
			offset, err = f.verifiedUploadPrefix(ctx, remotePath, local, size)
			if err != nil {
				return 0, err
			}
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
	if _, err := local.Seek(offset, io.SeekStart); err != nil {
		return offset, err
	}
	var remote base.RemoteWriter
	if offset > 0 {
		realPath, err := f.real(ctx, remotePath)
		if err != nil {
			return offset, err
		}
		remote, err = f.openWriteAt(ctx, realPath, offset)
		if err != nil {
			return offset, err
		}
	} else {
		remote, err = f.OpenWrite(ctx, remotePath, false)
		if err != nil {
			return offset, err
		}
	}
	defer remote.Close()
	stop := context.AfterFunc(ctx, func() { local.Close() })
	defer stop()
	reporter := newProgressReporter(options, offset, total)
	written, err := copyBuffer(ctx, PaceWriter(ctx, remote, options.MaxBytesPerSecond), local, reporter.advance, make([]byte, bufferSize), total-offset)
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

// verifiedUploadPrefix returns how many leading bytes of the remote file are
// known to equal the local source. Only the final uploadWriteSize bytes can be
// damaged by an interrupted concurrent write, so that window is compared with
// the source and the prefix ends at the first difference.
func (f *FS) verifiedUploadPrefix(ctx context.Context, remotePath string, local io.ReaderAt, size int64) (int64, error) {
	if size == 0 {
		return 0, nil
	}
	start := size - min(size, uploadWriteSize)
	remote, err := f.OpenRead(ctx, remotePath)
	if err != nil {
		return 0, err
	}
	defer remote.Close()
	seeker, ok := remote.(io.Seeker)
	if !ok {
		return 0, nil
	}
	if _, err := seeker.Seek(start, io.SeekStart); err != nil {
		return 0, fmt.Errorf("seek remote resume window: %w", err)
	}
	remoteTail := make([]byte, size-start)
	read, err := io.ReadFull(remote, remoteTail)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return 0, fmt.Errorf("read remote resume window: %w", err)
	}
	localTail := make([]byte, len(remoteTail))
	if _, err := local.ReadAt(localTail, start); err != nil && !errors.Is(err, io.EOF) {
		return 0, fmt.Errorf("read local resume window: %w", err)
	}
	matched := 0
	for matched < read && remoteTail[matched] == localTail[matched] {
		matched++
	}
	return start + int64(matched), nil
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
