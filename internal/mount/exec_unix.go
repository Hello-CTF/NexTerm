//go:build !windows

package mount

import "os/exec"

func configureCommand(*exec.Cmd) {}
