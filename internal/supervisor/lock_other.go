//go:build unix && !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly

package supervisor

import "fmt"

func lockSocket(path string) (func(), error) {
	return nil, fmt.Errorf("%w: socket lock on %s", ErrUnsupported, path)
}
