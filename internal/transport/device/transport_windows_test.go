//go:build windows

package device

import (
	"context"
	"net"

	"github.com/Microsoft/go-winio"
)

func dialSupervisorEndpoint(ctx context.Context, endpoint string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, endpoint)
}
