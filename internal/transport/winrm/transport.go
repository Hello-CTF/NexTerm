package winrm

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	fswinrm "github.com/ProbiusOfficial/NexTerm/internal/fs/winrm"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
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
	return newWithRunner(normalized, runner)
}

func newWithRunner(config Config, runner powerShellRunner) (*Transport, error) {
	if runner == nil {
		return nil, fmt.Errorf("WinRM PowerShell runner is required")
	}
	closeCtx, closeCancel := context.WithCancel(context.Background())
	return &Transport{
		runner:      runner,
		generation:  nextGeneration.Add(1),
		cwd:         config.InitialCWD,
		closeCtx:    closeCtx,
		closeCancel: closeCancel,
		execGate:    make(chan struct{}, 1),
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
