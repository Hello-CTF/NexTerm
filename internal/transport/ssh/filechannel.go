package ssh

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// FileChannelFS is a file system served by a channel other than SFTP.
type FileChannelFS interface {
	base.FileSystem
	Close() error
}

// FileChannelConfig lets a client serve files through the NexTerm helper,
// keeping SFTP as the fallback.
type FileChannelConfig struct {
	// SFTPOnly reports, from the stored host settings, that files use SFTP.
	SFTPOnly func(ctx context.Context) bool
	// Open opens the helper file channel. It can take long when the helper
	// must be installed first, so it runs in the background.
	Open func(ctx context.Context) (FileChannelFS, error)
	// Changed runs after the channel serving files changed.
	Changed func()
}

const (
	// fileChannelRetry is how long a failed helper channel stays on SFTP
	// before the next file operation tries the helper again.
	fileChannelRetry       = 5 * time.Minute
	fileChannelOpenTimeout = 3 * time.Minute
)

type fileChannel struct {
	mu       sync.Mutex
	config   *FileChannelConfig
	ready    FileChannelFS
	opening  chan struct{}
	failure  error
	failedAt time.Time
	// permanent keeps a failure for the life of the connection.
	permanent bool
	closed    bool
}

// SetFileChannel lets the client serve files through the helper file channel.
func (c *Client) SetFileChannel(config FileChannelConfig) {
	c.files.mu.Lock()
	defer c.files.mu.Unlock()
	c.files.config = &config
}

// FileChannelStatus names the channel file operations use now and, when it is
// SFTP although the helper channel is configured, why.
func (c *Client) FileChannelStatus(ctx context.Context) (channel, reason string) {
	f := &c.files
	f.mu.Lock()
	config, ready, opening, failure := f.config, f.ready, f.opening != nil, f.failure
	f.mu.Unlock()
	switch {
	case config == nil || (config.SFTPOnly != nil && config.SFTPOnly(ctx)):
		return base.FileChannelSFTP, ""
	case ready != nil:
		return base.FileChannelHelper, ""
	case opening:
		return base.FileChannelSFTP, "正在准备 NexTerm 文件通道"
	case failure != nil:
		return base.FileChannelSFTP, failure.Error()
	default:
		return base.FileChannelSFTP, ""
	}
}

func (c *Client) FileSystem(ctx context.Context) (base.FileSystem, error) {
	if files := c.files.current(ctx); files != nil {
		return files, nil
	}
	sftp, err := c.sftpFilesystem(ctx)
	if err == nil {
		return sftp, nil
	}
	// Without SFTP the helper channel is the only way to the files.
	if files, waitErr := c.files.wait(ctx); waitErr == nil {
		return files, nil
	} else if !errors.Is(waitErr, errNoFileChannel) {
		return nil, errors.Join(err, waitErr)
	}
	return nil, err
}

var errNoFileChannel = errors.New("no file channel")

// current returns the helper file system when it is ready and wanted, and
// starts opening it in the background when it is wanted but not ready.
func (f *fileChannel) current(ctx context.Context) base.FileSystem {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.config == nil || f.closed || (f.config.SFTPOnly != nil && f.config.SFTPOnly(ctx)) {
		return nil
	}
	if f.ready != nil {
		return f.ready
	}
	f.startLocked(ctx)
	return nil
}

func (f *fileChannel) startLocked(ctx context.Context) {
	if f.opening != nil || f.config.Open == nil {
		return
	}
	if f.failure != nil && (f.permanent || time.Since(f.failedAt) < fileChannelRetry) {
		return
	}
	opening := make(chan struct{})
	f.opening = opening
	config := f.config
	openCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fileChannelOpenTimeout)
	go func() {
		defer cancel()
		files, err := config.Open(openCtx)
		f.mu.Lock()
		f.opening = nil
		if err == nil && f.closed {
			_ = files.Close()
			err = errors.New("connection closed")
		}
		if err != nil {
			f.failure, f.failedAt = err, time.Now()
			f.permanent = errors.Is(err, base.ErrUnsupported)
		} else {
			f.ready, f.failure = files, nil
		}
		f.mu.Unlock()
		close(opening)
		if err == nil && config.Changed != nil {
			config.Changed()
		}
	}()
}

// wait starts opening the helper file system when needed and waits for it.
func (f *fileChannel) wait(ctx context.Context) (base.FileSystem, error) {
	f.mu.Lock()
	if f.config == nil || f.closed || (f.config.SFTPOnly != nil && f.config.SFTPOnly(ctx)) {
		f.mu.Unlock()
		return nil, errNoFileChannel
	}
	if f.ready != nil {
		ready := f.ready
		f.mu.Unlock()
		return ready, nil
	}
	f.startLocked(ctx)
	opening, failure := f.opening, f.failure
	f.mu.Unlock()
	if opening == nil {
		if failure != nil {
			return nil, failure
		}
		return nil, errNoFileChannel
	}
	select {
	case <-opening:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ready != nil {
		return f.ready, nil
	}
	return nil, f.failure
}

func (f *fileChannel) close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	if f.ready != nil {
		_ = f.ready.Close()
		f.ready = nil
	}
}
