package supervisor

import (
	"context"
	"os"
	"os/exec"
	"sync"
)

// helperProcess is a detached helper this process started. Its exit is
// observed (and, on Unix, reaped) by a waiter goroutine; observing it never
// signals or stops the helper, which keeps running after this process exits.
type helperProcess struct {
	pid     int
	process *os.Process
	done    chan struct{}
	// jobBound is set on Windows when the job object this process runs in
	// refused breakaway, so the helper may end when the job closes.
	jobBound bool
	mu       sync.Mutex
	err      error
}

func startHelperProcess(cmd *exec.Cmd, logFile *os.File) (*helperProcess, error) {
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return nil, err
	}
	_ = logFile.Close()
	process := &helperProcess{pid: cmd.Process.Pid, process: cmd.Process, done: make(chan struct{})}
	go func() {
		err := cmd.Wait()
		process.mu.Lock()
		process.err = err
		process.mu.Unlock()
		close(process.done)
	}()
	return process, nil
}

// kill ends a child that never became ready. The child is not reaped yet,
// so its pid still names it; once done is closed it is gone and nothing is
// signalled.
func (p *helperProcess) kill() {
	select {
	case <-p.done:
		return
	default:
	}
	_ = p.process.Kill()
}

// exitErr reports how the helper ended once done is closed.
func (p *helperProcess) exitErr() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

// serves reports whether this child is the process serving endpoint. The
// serving process is read from the connection's peer; when the platform
// cannot tell, a child that is still running is taken to be the server.
func (p *helperProcess) serves(ctx context.Context, endpoint string) bool {
	probeCtx, cancel := context.WithTimeout(ctx, helperProbeTimeout)
	defer cancel()
	conn, err := dialSocket(probeCtx, endpoint)
	if err == nil {
		pid, peerErr := verifiedHelperPeer(conn)
		_ = conn.Close()
		if peerErr == nil {
			return pid == p.pid
		}
	}
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}
