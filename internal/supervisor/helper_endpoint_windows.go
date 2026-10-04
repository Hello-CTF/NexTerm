//go:build windows

package supervisor

import (
	"path/filepath"
	"strings"
)

const pipePrefix = `\\.\pipe\`

func helperEndpoint(stateDir string) string {
	return pipeEndpointName(stateDir)
}

func endpointPath(path string) string {
	if strings.HasPrefix(path, pipePrefix) {
		return path
	}
	if absolute, err := filepath.Abs(path); err == nil {
		return absolute
	}
	return path
}
