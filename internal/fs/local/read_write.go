package local

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

func (f *FileSystem) WriteFile(ctx context.Context, path string, data []byte, backup bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path = real(path)
	mode := os.FileMode(0o600)
	info, err := os.Stat(path)
	if err == nil {
		mode = info.Mode()
	} else if !os.IsNotExist(err) {
		return err
	}
	temporary, err := writeTemporary(path, mode, func(file *os.File) error {
		_, err := file.Write(data)
		return err
	})
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(temporary)
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	if backup && info != nil {
		if err := backupFile(path); err != nil {
			return fmt.Errorf("backup %s: %w", path, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := replaceFile(temporary, path); err != nil {
		return err
	}
	committed = true
	return nil
}

func backupFile(path string) error {
	source, err := os.Open(path)
	if err != nil {
		return err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return err
	}
	backupPath := path + BackupSuffix
	temporary, err := writeTemporary(backupPath, info.Mode(), func(file *os.File) error {
		_, err := io.Copy(file, source)
		return err
	})
	if err != nil {
		return err
	}
	if err := replaceFile(temporary, backupPath); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func writeTemporary(path string, mode os.FileMode, write func(*os.File) error) (temporary string, err error) {
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, "."+filepath.Base(path)+".nexterm-tmp-*")
	if err != nil {
		return "", err
	}
	temporary = file.Name()
	tempPath := temporary
	defer func() {
		if err != nil {
			_ = file.Close()
			_ = os.Remove(tempPath)
		}
	}()
	if err = write(file); err != nil {
		return "", err
	}
	if err = file.Chmod(mode); err != nil {
		return "", err
	}
	if err = file.Sync(); err != nil {
		return "", err
	}
	if err = file.Close(); err != nil {
		return "", err
	}
	return temporary, nil
}

func (f *FileSystem) OpenRead(ctx context.Context, path string) (base.RemoteReader, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.Open(real(path))
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return &reader{file: file, size: info.Size()}, nil
}

type reader struct {
	file *os.File
	size int64
}

func (r *reader) Read(p []byte) (int, error) {
	return r.file.Read(p)
}

func (r *reader) Seek(offset int64, whence int) (int64, error) {
	return r.file.Seek(offset, whence)
}

func (r *reader) Size() int64 {
	return r.size
}

func (r *reader) Close() error {
	return r.file.Close()
}

func (f *FileSystem) OpenWrite(ctx context.Context, path string, appendMode bool) (base.RemoteWriter, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	flags := os.O_CREATE | os.O_WRONLY
	if appendMode {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	file, err := os.OpenFile(real(path), flags, 0o666)
	if err != nil {
		return nil, err
	}
	return &writer{file: file}, nil
}

type writer struct {
	file *os.File
}

func (w *writer) Write(p []byte) (int, error) {
	return w.file.Write(p)
}

func (w *writer) CurrentSize() (int64, error) {
	info, err := w.file.Stat()
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func (w *writer) Sync() error {
	return w.file.Sync()
}

func (w *writer) Close() error {
	return w.file.Close()
}

var (
	_ base.RemoteReader = (*reader)(nil)
	_ io.Seeker         = (*reader)(nil)
	_ base.RemoteWriter = (*writer)(nil)
)
