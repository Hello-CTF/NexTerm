//go:build windows

package supervisor

import (
	"path/filepath"
	"strings"
)

const pipePrefix = `\\.\pipe\`

func helperEndpoint(stateDir string) (string, error) {
	return helperEndpointVersion(stateDir, ProtocolVersion)
}

// helperEndpointVersion names the pipe of a helper speaking protocol version;
// helpers of different versions never share an endpoint.
func helperEndpointVersion(stateDir string, version int) (string, error) {
	return pipeEndpointName(stateDir, version)
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
