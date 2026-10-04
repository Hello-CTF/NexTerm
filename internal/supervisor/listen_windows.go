//go:build windows

package supervisor

import (
	"fmt"
	"net"
	"strings"
)

func listenSocket(path string) (net.Listener, error) {
	if !strings.HasPrefix(path, pipePrefix) {
		return nil, fmt.Errorf("%w: local IPC transport on %s", ErrUnsupported, path)
	}
	return listenPipe(path)
}
