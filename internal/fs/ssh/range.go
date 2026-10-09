package ssh

import (
	"context"
	"fmt"
	"io"
)

func (f *FS) ReadRange(ctx context.Context, remotePath string, offset, maxBytes int64) ([]byte, int64, error) {
	if offset < 0 {
		return nil, 0, fmt.Errorf("read offset must not be negative")
	}
	if maxBytes <= 0 {
		return nil, 0, fmt.Errorf("maximum read size must be positive")
	}
	remotePath, err := f.real(ctx, remotePath)
	if err != nil {
		return nil, 0, err
	}
	file, err := f.client.Open(remotePath)
	if err != nil {
		return nil, 0, fmt.Errorf("SFTP open %s: %w", remotePath, err)
	}
	stop := context.AfterFunc(ctx, func() { file.Close() })
	defer stop()
	defer file.Close()
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, 0, fmt.Errorf("SFTP seek %s: %w", remotePath, err)
	}
	info, err := file.Stat()
	if err != nil {
		return nil, 0, fmt.Errorf("SFTP stat open file %s: %w", remotePath, err)
	}
	if offset >= info.Size() {
		return []byte{}, info.Size(), nil
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes))
	if err != nil {
		return nil, 0, fmt.Errorf("SFTP range read %s: %w", remotePath, err)
	}
	return data, info.Size(), ctx.Err()
}
