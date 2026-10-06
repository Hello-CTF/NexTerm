//go:build !darwin

package termiusdb

import (
	"fmt"
	"runtime"
)

// PlatformKeySource reports that automatic Termius key retrieval is only
// implemented for macOS; other platforms must inject a KeySource.
func PlatformKeySource() (KeySource, error) {
	return nil, fmt.Errorf("termiusdb: automatic key retrieval is not supported on %s; Termius import requires an explicit key source", runtime.GOOS)
}
