package ssh

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type closingFS struct {
	base.FileSystem
	closed atomic.Bool
}

func (f *closingFS) Close() error {
	f.closed.Store(true)
	return nil
}

// A failed helper channel is not reopened on every file operation; a host
// that cannot have it keeps SFTP for the whole connection.
func TestFileChannelBacksOffAfterFailures(t *testing.T) {
	var opens atomic.Int32
	failure := errors.New("helper upload failed")
	files := &fileChannel{config: &FileChannelConfig{Open: func(context.Context) (FileChannelFS, error) {
		opens.Add(1)
		return nil, failure
	}}}
	ctx := context.Background()
	if _, err := files.wait(ctx); !errors.Is(err, failure) {
		t.Fatalf("wait = %v", err)
	}
	if files.current(ctx) != nil {
		t.Fatal("a failed channel serves files")
	}
	if _, err := files.wait(ctx); !errors.Is(err, failure) || opens.Load() != 1 {
		t.Fatalf("wait again = %v after %d opens, want the remembered failure", err, opens.Load())
	}
	files.mu.Lock()
	files.failedAt = time.Now().Add(-fileChannelRetry - time.Second)
	files.mu.Unlock()
	_, _ = files.wait(ctx)
	if opens.Load() != 2 {
		t.Fatalf("opens = %d, want a retry once the back-off passed", opens.Load())
	}
	files.mu.Lock()
	files.failure, files.permanent, files.failedAt = base.ErrUnsupported, true, time.Now().Add(-time.Hour)
	files.mu.Unlock()
	_, _ = files.wait(ctx)
	if opens.Load() != 2 {
		t.Fatal("a host that cannot use the channel was tried again")
	}
}

func TestFileChannelFollowsTheStoredSetting(t *testing.T) {
	var sftpOnly atomic.Bool
	ready := &closingFS{}
	files := &fileChannel{config: &FileChannelConfig{
		SFTPOnly: func(context.Context) bool { return sftpOnly.Load() },
		Open:     func(context.Context) (FileChannelFS, error) { return ready, nil },
	}}
	ctx := context.Background()
	if got, err := files.wait(ctx); err != nil || got != ready {
		t.Fatalf("wait = %v, %v", got, err)
	}
	sftpOnly.Store(true)
	if files.current(ctx) != nil {
		t.Fatal("the helper channel served files after the host switched to SFTP")
	}
	sftpOnly.Store(false)
	if files.current(ctx) != ready {
		t.Fatal("the helper channel did not come back after the host switched to auto")
	}
	files.close()
	if !ready.closed.Load() || files.current(ctx) != nil {
		t.Fatal("closing the connection kept the helper channel")
	}
}
