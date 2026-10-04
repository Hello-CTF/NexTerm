//go:build unix && !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly

package supervisor

import (
	"fmt"
	"path/filepath"
)

func lockHelperState(stateDir string) (func(), error) {
	return nil, fmt.Errorf("%w: helper state lock on %s", ErrUnsupported, filepath.Join(stateDir, "helper.lock"))
}
