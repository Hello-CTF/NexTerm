package mount

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/store"
)

type fakeRun struct {
	result commandResult
	err    error
}

type fakeRunner struct {
	mu       sync.Mutex
	lookups  map[string]string
	commands []command
	runs     []fakeRun
	run      func(context.Context, command) (commandResult, error)
}

func (r *fakeRunner) LookPath(file string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if path, ok := r.lookups[file]; ok {
		return path, nil
	}
	return "", errors.New("not found: " + file)
}

func (r *fakeRunner) Run(ctx context.Context, input command) (commandResult, error) {
	r.mu.Lock()
	input.args = append([]string(nil), input.args...)
	input.stdin = append([]byte(nil), input.stdin...)
	r.commands = append(r.commands, input)
	var next fakeRun
	if len(r.runs) > 0 {
		next = r.runs[0]
		r.runs = r.runs[1:]
	}
	run := r.run
	r.mu.Unlock()
	if run != nil {
		return run(ctx, input)
	}
	return next.result, next.err
}

func (r *fakeRunner) allCommands() []command {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]command(nil), r.commands...)
}

type fakeAuditor struct {
	mu     sync.Mutex
	inputs []store.AuditInput
	err    error
}

func (a *fakeAuditor) AuditInsert(_ context.Context, input store.AuditInput) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.inputs = append(a.inputs, input)
	return a.err
}

func (a *fakeAuditor) all() []store.AuditInput {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]store.AuditInput(nil), a.inputs...)
}

func newTestService(goos string, runner *fakeRunner, auditor Auditor) *Service {
	service := NewService(Config{
		Auditor: auditor,
		NewID:   func() string { return "mount-id" },
		Now:     func() time.Time { return time.UnixMilli(123456) },
	})
	service.goos = goos
	service.runner = runner
	return service
}

func requireCommandCount(t *testing.T, runner *fakeRunner, want int) []command {
	t.Helper()
	commands := runner.allCommands()
	if len(commands) != want {
		t.Fatalf("command count = %d, want %d: %+v", len(commands), want, commands)
	}
	return commands
}
