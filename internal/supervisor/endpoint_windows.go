//go:build windows

package supervisor

import "net"

type endpointIdentity struct {
	path string
}

func prepareEndpoint(path string) (net.Listener, endpointIdentity, func(), error) {
	unlock, err := lockSocket(path)
	if err != nil {
		return nil, endpointIdentity{}, nil, err
	}
	listener, err := listenSocket(path)
	if err != nil {
		unlock()
		return nil, endpointIdentity{}, nil, err
	}
	return listener, endpointIdentity{path: path}, unlock, nil
}

func (e endpointIdentity) Path() string {
	return e.path
}

func (e endpointIdentity) Cleanup() error {
	return nil
}
