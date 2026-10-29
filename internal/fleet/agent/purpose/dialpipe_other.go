//go:build !windows

package purpose

import (
	"context"
	"errors"
	"net"
)

func dialNamedPipe(context.Context, string) (net.Conn, error) {
	return nil, errors.New("named pipes are only available on Windows")
}
