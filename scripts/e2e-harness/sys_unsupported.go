//go:build !unix

package main

import (
	"fmt"
	"os"
)

func localStateDigest(string) (string, error) {
	return "", fmt.Errorf("state digest unsupported on this platform")
}

func terminateProcess(process *os.Process) error {
	return process.Kill()
}

func terminatePID(int) {
}
