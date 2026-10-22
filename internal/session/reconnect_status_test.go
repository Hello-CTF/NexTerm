package session

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/ssh"
)

type reconnectStatusHistory struct {
	mu     sync.Mutex
	events []StatusEvent
}

func (h *reconnectStatusHistory) EmitSessionEvent(_ context.Context, event Event) error {
	if event.Topic != TopicSessionStatus {
		return nil
	}
	h.mu.Lock()
	h.events = append(h.events, event.Payload.(StatusEvent))
	h.mu.Unlock()
	return nil
}

func (h *reconnectStatusHistory) snapshot() []StatusEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]StatusEvent(nil), h.events...)
}

func TestReconnectStopsImmediatelyForNonRetryableErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code ipc.Code
	}{
		{name: "auth", err: &ssh.ConnectError{Kind: ssh.ErrorKindAuth, Err: errors.New("authentication failed")}, code: ipc.Code("auth")},
		{name: "host key pending", err: &ssh.HostKeyError{Pending: true}, code: ipc.CodeHostKeyPending},
		{name: "host key changed", err: &ssh.HostKeyError{}, code: ipc.Code(ssh.ErrorKindHostKeyChanged)},
		{name: "ssh config", err: &ssh.ConnectError{Kind: ssh.ErrorKindConfig, Err: errors.New("invalid config")}, code: ipc.CodeBadParam},
		{name: "ipc config", err: ipc.BadParam(errors.New("invalid asset")), code: ipc.CodeBadParam},
		{name: "vault locked", err: ipc.NewError(ipc.CodeVaultLocked, "locked"), code: ipc.CodeVaultLocked},
		{name: "vault not initialized", err: ipc.NewError(ipc.CodeVaultNotInit, "not initialized"), code: ipc.CodeVaultNotInit},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			connector := newFakeConnector()
			history := &reconnectStatusHistory{}
			manager := NewManager(Config{
				Connector: connector, Terminals: newFakeTerminalFactory(), Emitter: history,
				ReconnectMax: 10, ReconnectBackoff: []time.Duration{0},
			})
			t.Cleanup(func() { _ = manager.Close() })
			connected, err := manager.Connect(context.Background(), Asset{ID: "asset", Kind: KindSSH})
			if err != nil {
				t.Fatal(err)
			}
			initialCalls := connector.callCount()
			connector.mu.Lock()
			connector.connect = func(context.Context, Asset, uint64, int) (base.Transport, error) {
				return nil, test.err
			}
			connector.mu.Unlock()

			if err := manager.Reconnect(context.Background(), connected.ID); !errors.Is(err, test.err) {
				t.Fatalf("reconnect error = %v, want %v", err, test.err)
			}
			if got := connector.callCount(); got != initialCalls+1 {
				t.Fatalf("connector calls = %d, want one reconnect attempt (%d)", got, initialCalls+1)
			}
			info := connected.Info()
			if info.Status != StatusFailed || info.ErrorCode != test.code || info.Attempt != 1 || info.NextRetryAt != nil || info.Error == "" {
				t.Fatalf("failed session info = %+v, want code %s attempt 1 without another retry", info, test.code)
			}
			events := history.snapshot()
			failed := events[len(events)-1]
			if failed.Status != StatusFailed || failed.ErrorCode != test.code || failed.Attempt != 1 || failed.NextRetryAt != nil {
				t.Fatalf("failed status event = %+v, want code %s attempt 1", failed, test.code)
			}
			encoded, err := json.Marshal(failed)
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]any
			if err := json.Unmarshal(encoded, &payload); err != nil {
				t.Fatal(err)
			}
			if payload["errorCode"] != string(test.code) || payload["attempt"] != float64(1) {
				t.Fatalf("status JSON = %s, want typed error and attempt", encoded)
			}
		})
	}
}

func TestReconnectNetworkErrorsKeepExistingBackoffBudget(t *testing.T) {
	tests := []struct {
		name string
		kind ssh.ErrorKind
		code ipc.Code
	}{
		{name: "network", kind: ssh.ErrorKindNetwork, code: ipc.CodeSSH},
		{name: "timeout", kind: ssh.ErrorKindTimeout, code: ipc.CodeTimeout},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			connector := newFakeConnector()
			history := &reconnectStatusHistory{}
			manager := NewManager(Config{
				Connector: connector, Terminals: newFakeTerminalFactory(), Emitter: history,
				ReconnectMax: 10, ReconnectBackoff: []time.Duration{0},
			})
			t.Cleanup(func() { _ = manager.Close() })
			connected, err := manager.Connect(context.Background(), Asset{ID: "asset", Kind: KindSSH})
			if err != nil {
				t.Fatal(err)
			}
			initialCalls := connector.callCount()
			connectErr := &ssh.ConnectError{Kind: test.kind, Err: errors.New("temporary failure")}
			connector.mu.Lock()
			connector.connect = func(context.Context, Asset, uint64, int) (base.Transport, error) {
				return nil, connectErr
			}
			connector.mu.Unlock()

			if err := manager.Reconnect(context.Background(), connected.ID); !errors.Is(err, connectErr) {
				t.Fatalf("reconnect error = %v, want %v", err, connectErr)
			}
			if got := connector.callCount(); got != initialCalls+10 {
				t.Fatalf("connector calls = %d, want all 10 reconnect attempts (%d)", got, initialCalls+10)
			}
			info := connected.Info()
			if info.Status != StatusFailed || info.ErrorCode != test.code || info.Attempt != 10 || info.NextRetryAt != nil {
				t.Fatalf("exhausted session info = %+v, want code %s attempt 10", info, test.code)
			}
			var retries []StatusEvent
			for _, event := range history.snapshot() {
				if event.Status == StatusReconnecting && event.ErrorCode != "" {
					retries = append(retries, event)
				}
			}
			if len(retries) != 9 {
				t.Fatalf("scheduled retry events = %d, want 9: %+v", len(retries), retries)
			}
			for index, event := range retries {
				if event.Attempt != index+1 || event.ErrorCode != test.code || event.NextRetryAt == nil {
					t.Errorf("retry event %d = %+v, want attempt and nextRetryAt", index, event)
				}
			}
		})
	}
}
