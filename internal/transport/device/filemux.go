package device

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/deviceproto"
	"github.com/Hello-CTF/NexTerm/internal/fs/remote"
)

// fileMuxRetry is how long file operations use a bridge each after the
// file-mux bridge failed, such as for an agent that predates it.
const fileMuxRetry = 5 * time.Minute

// fileDialer opens the connection for one file operation: a stream of the
// device's file-mux bridge, or a bridge of its own when the agent cannot
// multiplex.
type fileDialer struct {
	perOp func(ctx context.Context) (net.Conn, error)
	mux   *remote.Mux

	mu       sync.Mutex
	failedAt time.Time
}

func newFileDialer(dial DialFunc) *fileDialer {
	return &fileDialer{
		perOp: func(ctx context.Context) (net.Conn, error) { return dial(ctx, deviceproto.PurposeFile) },
		mux: remote.NewMux(func(ctx context.Context) (net.Conn, error) {
			return dial(ctx, deviceproto.PurposeFileMux)
		}),
	}
}

func (d *fileDialer) dial(ctx context.Context) (net.Conn, error) {
	d.mu.Lock()
	useMux := d.failedAt.IsZero() || time.Since(d.failedAt) >= fileMuxRetry
	d.mu.Unlock()
	if useMux {
		conn, err := d.mux.Open(ctx)
		if err == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		d.mu.Lock()
		d.failedAt = time.Now()
		d.mu.Unlock()
	}
	return d.perOp(ctx)
}

func (d *fileDialer) close() {
	_ = d.mux.Close()
}
