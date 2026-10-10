//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/Hello-CTF/NexTerm/internal/supervisor"
)

func localStateDigest(stateDir string) (string, error) {
	absolute, err := filepath.Abs(stateDir)
	if err != nil {
		return "", err
	}
	return supervisor.StateDigest(absolute, strconv.Itoa(os.Geteuid()))
}

func terminateProcess(process *os.Process) error {
	return process.Signal(syscall.SIGTERM)
}

func terminatePID(pid int) {
	if process, err := os.FindProcess(pid); err == nil {
		terminateProcess(process)
	}
}
