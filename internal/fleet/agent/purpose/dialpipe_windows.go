//go:build windows

package purpose

import (
	"context"
	"net"

	"github.com/Microsoft/go-winio"
)

func dialNamedPipe(ctx context.Context, address string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, address)
}
