//go:build !windows

package sshconfig

func readPuTTYRegistry() ([]PuTTYSession, error) {
	return nil, ErrPuTTYRegistryUnsupported
}
