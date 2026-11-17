package winrm

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	fswinrm "github.com/Hello-CTF/NexTerm/internal/fs/winrm"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

var nextGeneration atomic.Uint64

type Transport struct {
	runner      powerShellRunner
	generation  uint64
	cwdMu       sync.RWMutex
	cwd         string
	closeCtx    context.Context
	closeCancel context.CancelFunc
	closeOnce   sync.Once
	closeErr    error
	execGate    chan struct{}
	// newShellRunner builds the SOAP runners of an interactive shell; each
	// shell gets its own connections. Nil when the transport has no endpoint.
	newShellRunner func() (shellRunner, error)
	shellTimings   shellTimings
	shellsMu       sync.Mutex
	shells         map[*psShell]struct{}
	shellsClosed   bool
}

func New(config Config) (*Transport, error) {
	normalized, err := config.normalized()
	if err != nil {
		return nil, err
	}
	runner, err := newLibraryRunner(normalized)
	if err != nil {
		return nil, err
	}
	transport, err := newWithRunner(normalized, runner)
	if err != nil {
		return nil, err
	}
	transport.newShellRunner = func() (shellRunner, error) {
		runner, err := newLibraryRunner(normalized)
		if err != nil {
			return nil, err
		}
		return runner, nil
	}
	return transport, nil
}

func newWithRunner(config Config, runner powerShellRunner) (*Transport, error) {
	if runner == nil {
		return nil, fmt.Errorf("WinRM PowerShell runner is required")
	}
	closeCtx, closeCancel := context.WithCancel(context.Background())
	return &Transport{
		runner:       runner,
		generation:   nextGeneration.Add(1),
		cwd:          config.InitialCWD,
		closeCtx:     closeCtx,
		closeCancel:  closeCancel,
		execGate:     make(chan struct{}, 1),
		shellTimings: defaultShellTimings(config.RequestTimeout),
		shells:       make(map[*psShell]struct{}),
	}, nil
}

func (t *Transport) Kind() string {
	return "winrm"
}

func (t *Transport) Generation() uint64 {
	return t.generation
}

func (t *Transport) IsAlive() bool {
	return t.closeCtx.Err() == nil
}

func (t *Transport) Close() error {
	t.closeOnce.Do(func() {
		t.closeCancel()
		t.closeShells()
		if closer, ok := t.runner.(interface{ Close() error }); ok {
			t.closeErr = closer.Close()
		}
	})
	return t.closeErr
}

func (t *Transport) CWD() string {
	t.cwdMu.RLock()
	defer t.cwdMu.RUnlock()
	return t.cwd
}

func (t *Transport) SetCWD(cwd string) {
	if cwd == "" {
		return
	}
	t.cwdMu.Lock()
	t.cwd = cwd
	t.cwdMu.Unlock()
}

func (t *Transport) FileSystem(ctx context.Context) (base.FileSystem, error) {
	if err := t.check(ctx, 0); err != nil {
		return nil, err
	}
	return fswinrm.New(t), nil
}

func (t *Transport) Ping(ctx context.Context) (time.Duration, error) {
	result, err := t.Exec(ctx, "Write-Output 'ok'", base.ExecOptions{Timeout: 10 * time.Second})
	if err != nil {
		return result.Duration, err
	}
	if result.ExitCode == nil || *result.ExitCode != 0 {
		if result.Stderr != "" {
			return result.Duration, fmt.Errorf("WinRM ping failed: %s", result.Stderr)
		}
		return result.Duration, fmt.Errorf("WinRM ping returned no successful exit status")
	}
	return result.Duration, nil
}

func (t *Transport) check(ctx context.Context, generation uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if generation != 0 && generation != t.generation {
		return base.ErrStaleGeneration
	}
	if !t.IsAlive() {
		return base.ErrClosed
	}
	return nil
}

var (
	_ base.Transport     = (*Transport)(nil)
	_ base.FileTransport = (*Transport)(nil)
)

// OpenShell starts a long-lived PowerShell for an interactive terminal. It is
// separate from Exec, which keeps running every command in a fresh shell.
func (t *Transport) OpenShell(ctx context.Context, options ShellOptions) (InteractiveShell, error) {
	if err := t.check(ctx, 0); err != nil {
		return nil, err
	}
	if t.newShellRunner == nil {
		return nil, ErrShellUnsupported
	}
	control, err := t.newShellRunner()
	if err != nil {
		return nil, err
	}
	poll, err := t.newShellRunner()
	if err != nil {
		_ = control.Close()
		return nil, err
	}
	shell := newPSShell(control, poll, options, t.SetCWD, t.shellTimings)
	t.shellsMu.Lock()
	if t.shellsClosed {
		t.shellsMu.Unlock()
		_ = control.Close()
		_ = poll.Close()
		return nil, base.ErrClosed
	}
	t.shells[shell] = struct{}{}
	t.shellsMu.Unlock()
	shell.release = func() {
		t.shellsMu.Lock()
		delete(t.shells, shell)
		t.shellsMu.Unlock()
	}
	startCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopClose := context.AfterFunc(t.closeCtx, cancel)
	defer stopClose()
	if err := shell.start(startCtx, t.CWD()); err != nil {
		closeErr := shell.Close()
		if t.closeCtx.Err() != nil {
			return nil, base.ErrClosed
		}
		return nil, errors.Join(err, closeErr)
	}
	return shell, nil
}

// closeShells deletes every interactive shell still open. The deletes run in
// the background, each bounded by its close timeout, so closing a transport
// whose host stopped answering does not stall the caller.
func (t *Transport) closeShells() {
	t.shellsMu.Lock()
	t.shellsClosed = true
	shells := make([]*psShell, 0, len(t.shells))
	for shell := range t.shells {
		shells = append(shells, shell)
	}
	t.shellsMu.Unlock()
	for _, shell := range shells {
		go func() { _ = shell.Close() }()
	}
}
