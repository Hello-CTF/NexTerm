//go:build windows

package supervisor

import (
	"fmt"
	"net"
)

func listenSocket(path string) (net.Listener, error) {
	return nil, fmt.Errorf("%w: local IPC transport on %s", ErrUnsupported, path)
}

func peerUID(_ *net.UnixConn) (int, error) {
	return 0, ErrUnsupported
}
