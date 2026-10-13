package device

import (
	"context"
	"errors"
	"io/fs"
	"net"

	"github.com/Hello-CTF/NexTerm/internal/deviceproto"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// fileSystem serves the base filesystem surface through fresh file-purpose
// bridge dials per operation, mirroring how the supervisor client dials per
// call; streaming operations hold their bridge until the stream closes.
type fileSystem struct {
	dial func(ctx context.Context) (net.Conn, error)
}

// FileSystem returns the device filesystem surface. It does not probe: each
// operation dials its own bridge, so a missing capability or an offline
// device surfaces at operation time as the bridge dial error.
func (t *Transport) FileSystem(ctx context.Context) (base.FileSystem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &fileSystem{dial: func(ctx context.Context) (net.Conn, error) {
		return t.dial(ctx, deviceproto.PurposeFile)
	}}, nil
}

// client dials one operation bridge; done releases it.
func (f *fileSystem) client(ctx context.Context) (*deviceproto.Client, func(), error) {
	conn, err := f.dial(ctx)
	if err != nil {
		return nil, nil, err
	}
	return deviceproto.NewClient(conn), func() { _ = conn.Close() }, nil
}

func (f *fileSystem) List(ctx context.Context, path string) ([]base.FileEntry, error) {
	client, done, err := f.client(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	return client.List(ctx, path)
}

func (f *fileSystem) ReadFile(ctx context.Context, path string, maxBytes int64) ([]byte, error) {
	client, done, err := f.client(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	return client.ReadFile(ctx, path, maxBytes)
}

// ReadRange mirrors the SSH filesystem's range probe used by the file editor.
func (f *fileSystem) ReadRange(ctx context.Context, path string, offset, maxBytes int64) ([]byte, int64, error) {
	client, done, err := f.client(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer done()
	return client.ReadRange(ctx, path, offset, maxBytes)
}

func (f *fileSystem) WriteFile(ctx context.Context, path string, data []byte, backup bool) error {
	client, done, err := f.client(ctx)
	if err != nil {
		return err
	}
	defer done()
	return client.WriteFile(ctx, path, data, backup)
}

func (f *fileSystem) Mkdir(ctx context.Context, path string) error {
	client, done, err := f.client(ctx)
	if err != nil {
		return err
	}
	defer done()
	return client.Mkdir(ctx, path)
}

func (f *fileSystem) Rename(ctx context.Context, from, to string) error {
	client, done, err := f.client(ctx)
	if err != nil {
		return err
	}
	defer done()
	return client.Rename(ctx, from, to)
}

func (f *fileSystem) Delete(ctx context.Context, path string, isDir bool) error {
	client, done, err := f.client(ctx)
	if err != nil {
		return err
	}
	defer done()
	return client.Delete(ctx, path, isDir)
}

func (f *fileSystem) Chmod(ctx context.Context, path string, mode fs.FileMode) error {
	client, done, err := f.client(ctx)
	if err != nil {
		return err
	}
	defer done()
	return client.Chmod(ctx, path, mode)
}

func (f *fileSystem) Checksum(ctx context.Context, path, algorithm string) (string, error) {
	client, done, err := f.client(ctx)
	if err != nil {
		return "", err
	}
	defer done()
	return client.Checksum(ctx, path, algorithm)
}

func (f *fileSystem) Exists(ctx context.Context, path string) (bool, error) {
	client, done, err := f.client(ctx)
	if err != nil {
		return false, err
	}
	defer done()
	if _, err := client.Stat(ctx, path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (f *fileSystem) Size(ctx context.Context, path string) (int64, error) {
	client, done, err := f.client(ctx)
	if err != nil {
		return 0, err
	}
	defer done()
	return client.Stat(ctx, path)
}

func (f *fileSystem) OpenRead(ctx context.Context, path string) (base.RemoteReader, error) {
	conn, err := f.dial(ctx)
	if err != nil {
		return nil, err
	}
	return deviceproto.NewClient(conn).OpenRead(ctx, path)
}

func (f *fileSystem) OpenWrite(ctx context.Context, path string, appendMode bool) (base.RemoteWriter, error) {
	conn, err := f.dial(ctx)
	if err != nil {
		return nil, err
	}
	return deviceproto.NewClient(conn).OpenWrite(ctx, path, appendMode)
}

var _ base.FileSystem = (*fileSystem)(nil)
