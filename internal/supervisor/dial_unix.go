//go:build unix

package supervisor

import (
	"context"
	"net"
	"os"
)

// expectedPeerUID is the uid a helper socket and its directory must belong to. Tests replace it.
var expectedPeerUID = os.Geteuid

// dialSocket refuses a listener owned by another user, so a socket pre-created in a shared
// directory cannot impersonate the helper.
func dialSocket(ctx context.Context, path string) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	if err := verifyPeerUID(conn, expectedPeerUID()); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}
