//go:build unix

package supervisor

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
)

func helperEndpoint(stateDir string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", os.Geteuid(), stateDir)))
	return filepath.Join(os.TempDir(), fmt.Sprintf("nexterm-supervisor-%x", sum[:4]), fmt.Sprintf("supervisor-v%d.sock", ProtocolVersion))
}

func endpointPath(path string) string {
	if absolute, err := filepath.Abs(path); err == nil {
		return absolute
	}
	return path
}
