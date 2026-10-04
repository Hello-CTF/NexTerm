//go:build windows

package supervisor

import "net"

func authorizePeer(net.Conn) error {
	return nil
}
