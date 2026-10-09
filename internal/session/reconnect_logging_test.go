package session

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/ssh"
)

type statusRecorder struct {
	mu       sync.Mutex
	statuses []StatusEvent
}

func (r *statusRecorder) EmitSessionEvent(_ context.Context, event Event) error {
	if event.Topic != TopicSessionStatus {
		return nil
	}
	r.mu.Lock()
	r.statuses = append(r.statuses, event.Payload.(StatusEvent))
	r.mu.Unlock()
	return nil
}

func (r *statusRecorder) last() (StatusEvent, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.statuses) == 0 {
		return StatusEvent{}, false
	}
	return r.statuses[len(r.statuses)-1], true
}

func TestReconnectFailureLogsOriginalCause(t *testing.T) {
	var logs bytes.Buffer
	recorder := &statusRecorder{}
	connectErr := &ssh.ConnectError{Kind: ssh.ErrorKindRefused, Op: "dial", Host: "203.0.113.10", Port: 22, Err: errors.New("dial tcp 203.0.113.10:22: connect: connection refused")}
	connector := newFakeConnector()
	manager := NewManager(Config{
		Connector: connector, Terminals: newFakeTerminalFactory(), Emitter: recorder,
		Logger:           slog.New(slog.NewTextHandler(&logs, nil)),
		ReconnectMax:     1,
		ReconnectBackoff: []time.Duration{0},
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(context.Background(), Asset{ID: "asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	connector.mu.Lock()
	connector.connect = func(context.Context, Asset, uint64, int) (base.Transport, error) { return nil, connectErr }
	connector.mu.Unlock()

	if err := manager.Reconnect(context.Background(), connected.ID); !errors.Is(err, connectErr) {
		t.Fatalf("reconnect = %v, want the connect error", err)
	}

	output := logs.String()
	if !strings.Contains(output, "session reconnect failed") || !strings.Contains(output, "connection refused") {
		t.Fatalf("original cause missing from reconnect log: %q", output)
	}
	failed, ok := recorder.last()
	if !ok || failed.Status != StatusFailed {
		t.Fatalf("last status event = %+v, want failed", failed)
	}
	if !strings.Contains(failed.Error, "拒绝连接") {
		t.Fatalf("status event error = %q, want actionable Chinese message", failed.Error)
	}
}

func TestReconnectCallerCancelDoesNotWarn(t *testing.T) {
	var logs bytes.Buffer
	connector := newFakeConnector()
	manager := NewManager(Config{
		Connector: connector, Terminals: newFakeTerminalFactory(),
		Logger:           slog.New(slog.NewTextHandler(&logs, nil)),
		ReconnectMax:     1,
		ReconnectBackoff: []time.Duration{time.Hour},
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(context.Background(), Asset{ID: "asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := manager.Reconnect(ctx, connected.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("reconnect = %v, want context.Canceled", err)
	}
	if logs.Len() != 0 {
		t.Fatalf("canceled reconnect produced warning log: %q", logs.String())
	}
}
