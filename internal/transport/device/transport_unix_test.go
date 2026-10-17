//go:build unix

package device

import (
	"context"
	"net"
)

func dialSupervisorEndpoint(ctx context.Context, endpoint string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "unix", endpoint)
}
