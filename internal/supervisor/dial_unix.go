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
	if err := verifyHelperSocketOwner(conn, path); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func verifyHelperSocketOwner(conn net.Conn, path string) error {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return verifyPeerUID(conn, expectedPeerUID())
	}
	uid, err := peerUID(unixConn)
	if err != nil {
		return err
	}
	if uid != expectedPeerUID() {
		return &UntrustedEndpointError{Kind: EndpointSocket, Path: path, OwnerUID: uid}
	}
	return nil
}
