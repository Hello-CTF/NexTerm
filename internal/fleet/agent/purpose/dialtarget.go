package purpose

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// dialTarget connects to a requested target from the device. Unix socket paths may start with "~/" or
// "$XDG_RUNTIME_DIR" so the server can name per-user Docker sockets without knowing the agent's home directory;
// "npipe" reaches Windows named pipes such as the Docker Engine pipe.
func dialTarget(ctx context.Context, network, address string, timeout time.Duration) (net.Conn, error) {
	switch network {
	case "npipe":
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return dialNamedPipe(ctx, address)
	case "unix":
		address = expandSocketPath(address)
	}
	return (&net.Dialer{Timeout: timeout}).DialContext(ctx, network, address)
}

func expandSocketPath(address string) string {
	if strings.HasPrefix(address, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, address[2:])
		}
	}
	if strings.HasPrefix(address, "$XDG_RUNTIME_DIR/") {
		if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
			return filepath.Join(dir, address[len("$XDG_RUNTIME_DIR/"):])
		}
	}
	return address
}
