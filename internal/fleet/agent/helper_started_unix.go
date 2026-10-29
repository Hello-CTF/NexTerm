//go:build unix

package agent

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
)

// helperStartedAt reads the start time of a process through ps, which every supported Unix host has;
// empty when it cannot be read.
func helperStartedAt(ctx context.Context, pid int) string {
	if pid <= 0 {
		return ""
	}
	out, err := exec.CommandContext(ctx, "ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	return strings.Join(strings.Fields(string(out)), " ")
}
