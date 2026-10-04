//go:build linux

package supervisor

import "golang.org/x/sys/unix"

func peerUIDFD(fd uintptr) (int, error) {
	cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return 0, err
	}
	return int(cred.Uid), nil
}
