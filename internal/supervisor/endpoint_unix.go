//go:build unix

package supervisor

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
)

type endpointIdentity struct {
	path string
	file os.FileInfo
}

func prepareEndpoint(path string) (net.Listener, endpointIdentity, func(), error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, endpointIdentity{}, nil, fmt.Errorf("%w: socket path: %v", ErrInvalidInput, err)
	}
	if err := ensurePrivateDir(filepath.Dir(absolute)); err != nil {
		return nil, endpointIdentity{}, nil, fmt.Errorf("supervisor socket directory: %w", err)
	}
	unlock, err := lockSocket(absolute)
	if err != nil {
		return nil, endpointIdentity{}, nil, err
	}
	listener, err := listenSocket(absolute)
	if err != nil {
		unlock()
		return nil, endpointIdentity{}, nil, err
	}
	file, err := os.Lstat(absolute)
	if err != nil {
		unlock()
		_ = listener.Close()
		_ = os.Remove(absolute)
		return nil, endpointIdentity{}, nil, fmt.Errorf("inspect supervisor socket: %w", err)
	}
	return listener, endpointIdentity{path: absolute, file: file}, unlock, nil
}

func (e endpointIdentity) Path() string {
	return e.path
}

func (e endpointIdentity) Cleanup() error {
	if info, err := os.Lstat(e.path); err == nil && os.SameFile(e.file, info) {
		return os.Remove(e.path)
	}
	return nil
}
