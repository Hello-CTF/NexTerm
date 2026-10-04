//go:build windows

package supervisor

import (
	"context"
	"net"
)

func dialSocket(ctx context.Context, path string) (net.Conn, error) {
	return dialPipe(ctx, path)
}
