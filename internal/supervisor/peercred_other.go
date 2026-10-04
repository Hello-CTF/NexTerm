//go:build unix && !linux && !darwin

package supervisor

import "fmt"

func peerUIDFD(_ uintptr) (int, error) {
	return 0, fmt.Errorf("%w: peer credentials", ErrUnsupported)
}
