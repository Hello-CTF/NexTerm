//go:build unix

package supervisor

import (
	"fmt"
	"net"
	"os"
)

func authorizePeer(conn net.Conn) error {
	return verifyPeerUID(conn, os.Geteuid())
}

func verifyPeerUID(conn net.Conn, want int) error {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return fmt.Errorf("%w: peer connection is not a unix socket", ErrProtocol)
	}
	uid, err := peerUID(unixConn)
	if err != nil {
		return err
	}
	if uid != want {
		return fmt.Errorf("%w: peer uid %d is not %d", ErrProtocol, uid, want)
	}
	return nil
}
