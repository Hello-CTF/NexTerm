package device

import (
	"context"
	"net"

	"github.com/Hello-CTF/NexTerm/internal/deviceproto"
	"github.com/Hello-CTF/NexTerm/internal/fs/remote"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// FileSystem returns the device filesystem surface. It does not probe: each
// operation dials its own file-purpose bridge, so a missing capability or an
// offline device surfaces at operation time as the bridge dial error.
func (t *Transport) FileSystem(ctx context.Context) (base.FileSystem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return remote.NewFileSystem(remote.Options{
		Dial: func(ctx context.Context) (net.Conn, error) {
			return t.dial(ctx, deviceproto.PurposeFile)
		},
		Exec:     t.Exec,
		Platform: t.platform,
		Channel:  base.FileChannelDevice,
	}), nil
}
