//go:build darwin

package supervisor

import "golang.org/x/sys/unix"

func peerUIDFD(fd uintptr) (int, error) {
	cred, err := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if err != nil {
		return 0, err
	}
	return int(cred.Uid), nil
}
