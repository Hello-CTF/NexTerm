package durable

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeRunner struct {
	mu      sync.Mutex
	calls   [][]string
	handler func(context.Context, ...string) ([]byte, error)
}

func (f *fakeRunner) run(ctx context.Context, args ...string) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string(nil), args...))
	handler := f.handler
	f.mu.Unlock()
	if handler != nil {
		return handler(ctx, args...)
	}
	return nil, nil
}

func (f *fakeRunner) recordedCalls() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([][]string, len(f.calls))
	for index := range f.calls {
		result[index] = append([]string(nil), f.calls[index]...)
	}
	return result
}

func newUnitBackend(t *testing.T) (*Backend, *fakeRunner) {
	t.Helper()
	root := t.TempDir()
	binary := filepath.Join(root, "tmux-test-bin")
	if err := os.WriteFile(binary, []byte(""), 0o700); err != nil {
		t.Fatal(err)
	}
	backend, err := New(Config{
		Binary:         binary,
		SocketPath:     filepath.Join(root, "run", "tmux.sock"),
		StateDir:       filepath.Join(root, "state"),
		Namespace:      "unit-durable",
		CommandTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	backend.runner = runner.run
	backend.pollInterval = 2 * time.Millisecond
	backend.statusInterval = 8 * time.Millisecond
	return backend, runner
}

func touchUnitSocket(t *testing.T, backend *Backend) {
	t.Helper()
	if err := os.WriteFile(backend.socketPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func unitRecord(backend *Backend, id string) record {
	return record{
		info: Info{
			ID:        id,
			SessionID: "$1",
			PaneID:    "%1",
			PID:       4321,
			CreatedAt: time.Unix(1700000000, 0),
		},
		name:          backend.sessionName(id),
		windowID:      "@1",
		owner:         id,
		namespace:     backend.namespace,
		paneOption:    "%1",
		windowOption:  "@1",
		deadStatus:    "0",
		recordingLive: true,
	}
}

func discoveryLine(backend *Backend, current record) string {
	dead := "0"
	if current.info.Dead {
		dead = "1"
	}
	pipe := "0"
	if current.recordingLive {
		pipe = "1"
	}
	status := current.deadStatus
	if status == "" {
		status = "0"
	}
	fields := []string{
		current.name,
		current.info.SessionID,
		current.windowID,
		strconv.FormatInt(current.info.CreatedAt.Unix(), 10),
		current.owner,
		current.namespace,
		current.paneOption,
		current.windowOption,
		current.info.PaneID,
		strconv.Itoa(current.info.PID),
		dead,
		status,
		pipe,
	}
	return strings.Join(fields, fieldSeparator) + "\n"
}

func installRecords(backend *Backend, runner *fakeRunner, records ...record) {
	runner.handler = func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "list-panes" {
			var output strings.Builder
			for _, current := range records {
				output.WriteString(discoveryLine(backend, current))
			}
			return []byte(output.String()), nil
		}
		return nil, nil
	}
}

func newUnitSession(t *testing.T, backend *Backend, current record, initial []byte) *Session {
	t.Helper()
	if err := os.WriteFile(backend.recordingPath(current.info.ID), initial, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := backend.openRecording(current.info.ID)
	if err != nil {
		t.Fatal(err)
	}
	session := newSession(backend, current, file)
	t.Cleanup(func() { _ = session.Detach() })
	return session
}
