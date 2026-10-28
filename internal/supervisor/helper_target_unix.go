//go:build unix

package supervisor

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"time"
)

// pidTarget stops a helper by pid where no process handle exists. SIGTERM
// is sent while the verified connection is open, so the pid still names the
// helper; SIGKILL is sent only after the endpoint answers again with the same
// pid, again while that connection is open.
type pidTarget struct {
	pid      int
	reverify helperReverify
}

func (t *pidTarget) terminate() error {
	return ignoreGone(syscall.Kill(t.pid, syscall.SIGTERM))
}

func (t *pidTarget) wait(ctx context.Context, limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	for {
		if err := syscall.Kill(t.pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (t *pidTarget) kill(ctx context.Context) error {
	pid, conn, err := t.reverify(ctx)
	if err != nil {
		return fmt.Errorf("%w: the helper can no longer be verified, so it is not force-killed: %v", ErrUnavailable, err)
	}
	defer func() { _ = conn.Close() }()
	if pid != t.pid {
		return fmt.Errorf("%w: pid %d now serves the endpoint, so pid %d is not force-killed", ErrHelperChanged, pid, t.pid)
	}
	return ignoreGone(syscall.Kill(t.pid, syscall.SIGKILL))
}

func (t *pidTarget) close() {}

func ignoreGone(err error) error {
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
