//go:build linux

package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// pidfdTarget holds the helper through a pidfd: signals and waits address
// that process even after its pid is reused.
type pidfdTarget struct {
	fd int
}

func openHelperTarget(pid int, reverify helperReverify) (helperTarget, error) {
	fd, err := unix.PidfdOpen(pid, 0)
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EPERM) {
		// Kernels before 5.3 have no pidfd, and a seccomp or container
		// policy may forbid it.
		return &pidTarget{pid: pid, reverify: reverify}, nil
	}
	if err != nil {
		return nil, err
	}
	return &pidfdTarget{fd: fd}, nil
}

func (t *pidfdTarget) terminate() error {
	return ignoreGone(unix.PidfdSendSignal(t.fd, unix.SIGTERM, nil, 0))
}

func (t *pidfdTarget) wait(ctx context.Context, limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	for {
		// A pidfd becomes readable when its process exits.
		fds := []unix.PollFd{{Fd: int32(t.fd), Events: unix.POLLIN}}
		count, err := unix.Poll(fds, 50)
		if err == nil && count > 0 {
			return true
		}
		if err != nil && !errors.Is(err, unix.EINTR) {
			return false
		}
		if ctx.Err() != nil || time.Now().After(deadline) {
			return false
		}
	}
}

func (t *pidfdTarget) kill(context.Context) error {
	return ignoreGone(unix.PidfdSendSignal(t.fd, unix.SIGKILL, nil, 0))
}

func (t *pidfdTarget) close() {
	_ = unix.Close(t.fd)
}

// helperProcessExecutable reports the executable a helper process runs, for
// restarting it when its replacement fails. A replaced or deleted file is not
// reported: starting whatever now has that path could run another program.
func helperProcessExecutable(pid int) (string, error) {
	path, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
	if err != nil {
		return "", err
	}
	if strings.HasSuffix(path, " (deleted)") {
		return "", fmt.Errorf("%w: the executable of helper %d was deleted", ErrUnavailable, pid)
	}
	return path, nil
}

// helperProcessStartTime reads field 22 of /proc/<pid>/stat: the start time
// in clock ticks since boot, which tells a reused pid from the original.
func helperProcessStartTime(pid int) (uint64, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, err
	}
	// The command name (field 2) may contain spaces; fields after it follow
	// the last ')'.
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return 0, fmt.Errorf("%w: malformed /proc/%d/stat", ErrUnavailable, pid)
	}
	fields := strings.Fields(string(data[end+1:]))
	// fields[0] is field 3, so field 22 is fields[19].
	if len(fields) < 20 {
		return 0, fmt.Errorf("%w: short /proc/%d/stat", ErrUnavailable, pid)
	}
	return strconv.ParseUint(fields[19], 10, 64)
}
