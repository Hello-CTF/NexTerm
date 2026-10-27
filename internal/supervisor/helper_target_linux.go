//go:build linux

package supervisor

import (
	"context"
	"errors"
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
