package applog

import (
	"errors"
	"fmt"
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
}

func openRotatingFile(path string, maxBytes int64, backups int) (*rotatingFile, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxFileBytes
	}
	if backups < 0 {
		backups = 0
	}
	r := &rotatingFile{path: path, maxBytes: maxBytes, backups: backups}
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
	if r.file == nil {
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	if r.size > 0 && r.size+int64(len(p)) > r.maxBytes {
		if err := r.rotate(); err != nil && r.file == nil {
			return 0, err
		}
	}
	n, err := r.file.Write(p)
	r.size += int64(n)
	return n, err
}

// rotate reopens path even when a rename fails, so a failed rotation keeps appending instead of losing records.
func (r *rotatingFile) rotate() error {
	if err := r.file.Close(); err != nil {
		r.file = nil
		return errors.Join(err, r.open())
	}
	r.file = nil
	var renameErr error
	if r.backups == 0 {
		renameErr = os.Remove(r.path)
	} else {
		if err := os.Remove(r.backupPath(r.backups)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			renameErr = err
		}
		for i := r.backups - 1; i >= 1 && renameErr == nil; i-- {
			if err := os.Rename(r.backupPath(i), r.backupPath(i+1)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				renameErr = err
			}
		}
		if renameErr == nil {
			renameErr = os.Rename(r.path, r.backupPath(1))
		}
	}
	return errors.Join(renameErr, r.open())
}

func (r *rotatingFile) backupPath(index int) string {
	return fmt.Sprintf("%s.%d", r.path, index)
}

func (r *rotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}
