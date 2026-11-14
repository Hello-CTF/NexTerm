package device

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/fs/remote"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// FileSystem returns the device filesystem surface. It does not probe: each
// operation opens a stream of the shared file-mux bridge, or its own file
// bridge from an agent that cannot multiplex, so an offline device surfaces at
// operation time as the dial error.
func (t *Transport) FileSystem(ctx context.Context) (base.FileSystem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return remote.NewFileSystem(remote.Options{
		Dial:     t.files.dial,
		Exec:     t.Exec,
		Platform: t.platform,
		Channel:  base.FileChannelDevice,
	}), nil
}
