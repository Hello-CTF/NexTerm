package supervisor

import (
	"os"
	"os/exec"
	"sync"
)

// helperProcess is a detached helper this process started. Its exit is
// observed (and, on Unix, reaped) by a waiter goroutine; observing it never
// signals or stops the helper, which keeps running after this process exits.
type helperProcess struct {
	pid  int
	done chan struct{}
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
	process := &helperProcess{pid: cmd.Process.Pid, done: make(chan struct{})}
	go func() {
		err := cmd.Wait()
		process.mu.Lock()
		process.err = err
		process.mu.Unlock()
		close(process.done)
	}()
	return process, nil
}

// exitErr reports how the helper ended once done is closed.
func (p *helperProcess) exitErr() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}
