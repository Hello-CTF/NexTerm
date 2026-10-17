//go:build unix && !darwin && !linux

package pty

import "errors"

func readUnixProcessIdentity(int) (string, error) {
	return "", errors.New("process start identity is unsupported on this platform")
}
