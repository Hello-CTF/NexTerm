//go:build unix

package supervisor

import (
	"context"
	"net"
)

func dialSocket(ctx context.Context, path string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "unix", path)
}
