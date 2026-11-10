package applog

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sync"
)

const (
	// DefaultMaxFileBytes and DefaultMaxBackups bound <data-dir>/logs/nexterm.log to about 60 MB.
	DefaultMaxFileBytes int64 = 10 << 20
	DefaultMaxBackups         = 5
)

// rotatingFile appends to path and, before a write would push it past maxBytes, renames it to path.1
// (shifting older backups up to path.<backups> and dropping the oldest) and starts a new file.
type rotatingFile struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	backups  int
	file     *os.File
	size     int64
	closed   bool

	// limit is the size that triggers the next rotation: maxBytes, or after a failed rotation the size at
	// that failure plus maxBytes, so a file that cannot be moved is retried rarely instead of on every write.
	limit int64
	// errOut receives the first rotation failure of a failing streak; nil discards it.
	errOut   io.Writer
	reported bool
	rename   func(oldPath, newPath string) error
	remove   func(path string) error
}

func openRotatingFile(path string, maxBytes int64, backups int, errOut io.Writer) (*rotatingFile, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxFileBytes
	}
	if backups < 0 {
		backups = 0
	}
	r := &rotatingFile{
		path: path, maxBytes: maxBytes, backups: backups, limit: maxBytes,
		errOut: errOut, rename: os.Rename, remove: os.Remove,
	}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *rotatingFile) open() error {
	file, err := os.OpenFile(r.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return err
	}
	r.file, r.size = file, info.Size()
	return nil
}

func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, os.ErrClosed
	}
	if r.file == nil {
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	if r.size > 0 && r.size+int64(len(p)) > r.limit {
		if err := r.rotate(); err != nil {
			r.limit = r.size + r.maxBytes
			if !r.reported && r.errOut != nil {
				_, _ = fmt.Fprintf(r.errOut, "nexterm: log rotation failed; appending to %s until the next attempt: %s\n", r.path, RedactText(err.Error()))
			}
			r.reported = true
			if r.file == nil {
				return 0, err
			}
		} else {
			r.limit, r.reported = r.maxBytes, false
		}
	}
	n, err := r.file.Write(p)
	r.size += int64(n)
	return n, err
}

// rotate first moves the live file aside, so when it cannot be moved (another process holds it open on
// Windows) no backup has been touched yet. The path is always reopened, so writes continue after a failure.
func (r *rotatingFile) rotate() error {
	if err := r.file.Close(); err != nil {
		r.file = nil
		return errors.Join(err, r.open())
	}
	r.file = nil
	if r.backups == 0 {
		return errors.Join(r.remove(r.path), r.open())
	}
	staging := r.path + ".rotating"
	if err := r.rename(r.path, staging); err != nil {
		return errors.Join(err, r.open())
	}
	var errs []error
	if err := r.remove(r.backupPath(r.backups)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, err)
	}
	for i := r.backups - 1; i >= 1; i-- {
		if err := r.rename(r.backupPath(i), r.backupPath(i+1)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	if err := r.rename(staging, r.backupPath(1)); err != nil {
		errs = append(errs, err)
	}
	errs = append(errs, r.open())
	return errors.Join(errs...)
}

func (r *rotatingFile) backupPath(index int) string {
	return fmt.Sprintf("%s.%d", r.path, index)
}

func (r *rotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}
