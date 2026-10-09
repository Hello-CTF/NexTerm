package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type chatGrantSettings struct {
	mu     sync.Mutex
	values map[string]string
}

func (s *chatGrantSettings) SettingGet(_ context.Context, key string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[key]
	return value, ok, nil
}

func (s *chatGrantSettings) SettingSet(_ context.Context, key, value string) error {
	s.mu.Lock()
	s.values[key] = value
	s.mu.Unlock()
	return nil
}

type chatGrantRecorder struct {
	mu     sync.Mutex
	events []guard.GrantAuditEvent
}

func (r *chatGrantRecorder) record(_ context.Context, event guard.GrantAuditEvent) error {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
	return nil
}

func (r *chatGrantRecorder) find(action, deviceID string) *guard.GrantAuditEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.events {
		if r.events[i].Action == action && r.events[i].DeviceID == deviceID {
			return &r.events[i]
		}
	}
	return nil
}

type chatGrantTerminal struct {
	fakeTerminal
	assetID    string
	assetIDErr error
}

func (c *chatGrantTerminal) SessionAssetID(context.Context, string) (string, error) {
	if c.assetIDErr != nil {
		return "", c.assetIDErr
	}
	return c.assetID, nil
}

type chatGrantAuditSink struct {
	mu      sync.Mutex
	entries []AuditEntry
}

func (s *chatGrantAuditSink) record(_ context.Context, entry AuditEntry) error {
	s.mu.Lock()
	s.entries = append(s.entries, entry)
	s.mu.Unlock()
	return nil
}

func (s *chatGrantAuditSink) payloadText(t *testing.T) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	var builder strings.Builder
	for _, entry := range s.entries {
		encoded, err := json.Marshal(entry.Payload)
		if err != nil {
			t.Fatal(err)
		}
		builder.Write(encoded)
		builder.WriteByte('\n')
	}
	return builder.String()
}

type chatGrantHarness struct {
	execution  *Execution
	terminal   *chatGrantTerminal
	transport  *fakeTransport
	recorder   *chatGrantRecorder
	audits     *chatGrantAuditSink
	grants     *guard.Grants
	dockerRuns int
	dockerActs int
}

func newChatGrantManager(t *testing.T, recorder *chatGrantRecorder) *guard.Grants {
	t.Helper()
	manager, err := guard.NewGrants(context.Background(), &chatGrantSettings{values: make(map[string]string)}, recorder.record)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func newChatGrantHarness(permission guard.Config, assetID string, grants *guard.Grants, recorder *chatGrantRecorder) *chatGrantHarness {
	terminal := &chatGrantTerminal{assetID: assetID}
	transport := &fakeTransport{files: newFakeFS(), result: base.ExecResult{Stdout: "ok"}}
	audits := &chatGrantAuditSink{}
	harness := &chatGrantHarness{terminal: terminal, transport: transport, recorder: recorder, audits: audits, grants: grants}
	deps := Dependencies{
		Transport:  func(context.Context, string) (base.Transport, error) { return transport, nil },
		Terminal:   terminal,
		TabSession: func(tabID string) string { return "session-1" },
		Audit:      audits.record,
		Grants:     grants,
		DockerExec: func(context.Context, string, string, string) (ExecResult, error) {
			harness.dockerRuns++
			return ExecResult{}, nil
		},
		DockerAct: func(context.Context, string, string, string) error {
			harness.dockerActs++
			return nil
		},
	}
	harness.execution = &Execution{
		JobID:      "job-grant-1",
		Registry:   NewRegistry(deps),
		Scope:      Scope{SessionID: "session-1", TabID: "tab-1"},
		Permission: permission,
		Memory:     guard.NewMemory(),
	}
	return harness
}

// invoke drives the real chat tool path: the eino tools node built from
// Execution.Tools() executes the tool call exactly as the chat agent does.
func (h *chatGrantHarness) invoke(callID, name, args string) ([]*schema.Message, error) {
	einoTools, err := h.execution.Tools()
	if err != nil {
		return nil, err
	}
	node, err := compose.NewToolNode(context.Background(), &compose.ToolsNodeConfig{Tools: einoTools, ExecuteSequentially: true})
	if err != nil {
		return nil, err
	}
	message := schema.AssistantMessage("", []schema.ToolCall{{ID: callID, Function: schema.FunctionCall{Name: name, Arguments: args}}})
	return node.Invoke(context.Background(), message)
}

func requireChatGrantInterrupt(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a confirmation interrupt")
	}
	if _, ok := compose.IsInterruptRerunError(err); !ok {
		t.Fatalf("error is not an interrupt: %v", err)
	}
}

func requireChatGrantResult(t *testing.T, messages []*schema.Message, err error, marker string) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected interrupt: %v", err)
	}
	if len(messages) != 1 || !strings.Contains(messages[0].Content, marker) {
		t.Fatalf("tool result = %+v", messages)
	}
}

func (h *chatGrantHarness) writeCount() int {
	h.terminal.mu.Lock()
	defer h.terminal.mu.Unlock()
	return len(h.terminal.writes)
}

func TestChatGrantDefaultOffRequiresConfirm(t *testing.T) {
	t.Run("nil grants manager", func(t *testing.T) {
		recorder := &chatGrantRecorder{}
		h := newChatGrantHarness(guard.Config{Mode: guard.ReadWrite}, "asset-a", nil, recorder)
		_, err := h.invoke("call-1", "send_keys", `{"keys":"touch /tmp/x","enter":true}`)
		requireChatGrantInterrupt(t, err)
		if h.writeCount() != 0 {
			t.Fatal("write happened without grant")
		}
		if recorder.find("use", "asset-a") != nil {
			t.Fatal("use audited without grant")
		}
	})
	t.Run("empty grants manager", func(t *testing.T) {
		recorder := &chatGrantRecorder{}
		h := newChatGrantHarness(guard.Config{Mode: guard.ReadWrite}, "asset-a", newChatGrantManager(t, recorder), recorder)
		_, err := h.invoke("call-1", "send_keys", `{"keys":"touch /tmp/x","enter":true}`)
		requireChatGrantInterrupt(t, err)
		if h.writeCount() != 0 {
			t.Fatal("write happened without grant")
		}
		if recorder.find("use", "asset-a") != nil {
			t.Fatal("use audited without grant")
		}
	})
}

func TestChatGrantAllowsTerminalWriteAndAuditsUse(t *testing.T) {
	recorder := &chatGrantRecorder{}
	grants := newChatGrantManager(t, recorder)
	if _, err := grants.Grant(context.Background(), "asset-a", []guard.GrantKind{guard.GrantKindTerminalWrite}); err != nil {
		t.Fatal(err)
	}
	h := newChatGrantHarness(guard.Config{Mode: guard.ReadWrite}, "asset-a", grants, recorder)
	messages, err := h.invoke("call-1", "send_keys", `{"keys":"touch /tmp/x","enter":true}`)
	requireChatGrantResult(t, messages, err, "已发送")
	if h.writeCount() != 1 {
		t.Fatalf("writes = %d, want 1", h.writeCount())
	}
	use := recorder.find("use", "asset-a")
	if use == nil || use.Operation != "send_keys" || use.RunID != "job-grant-1" {
		t.Fatalf("use event = %+v", use)
	}
	if payload := h.audits.payloadText(t); strings.Contains(payload, "touch") {
		t.Fatalf("audit payload leaks keys: %s", payload)
	}
}

func TestChatGrantAllowsSessionExec(t *testing.T) {
	recorder := &chatGrantRecorder{}
	grants := newChatGrantManager(t, recorder)
	if _, err := grants.Grant(context.Background(), "asset-a", []guard.GrantKind{guard.GrantKindSessionExec}); err != nil {
		t.Fatal(err)
	}
	h := newChatGrantHarness(guard.Config{Mode: guard.ReadWrite}, "asset-a", grants, recorder)
	messages, err := h.invoke("call-1", "exec_commands", `{"commands":["touch /tmp/x"]}`)
	requireChatGrantResult(t, messages, err, "touch /tmp/x")
	if h.transport.calls() != 1 {
		t.Fatalf("exec runs = %d, want 1", h.transport.calls())
	}
	use := recorder.find("use", "asset-a")
	if use == nil || use.Operation != "exec_commands" || use.RunID != "job-grant-1" {
		t.Fatalf("use event = %+v", use)
	}
}

func TestChatGrantCrossDeviceIsolation(t *testing.T) {
	recorder := &chatGrantRecorder{}
	grants := newChatGrantManager(t, recorder)
	if _, err := grants.Grant(context.Background(), "asset-b", []guard.GrantKind{guard.GrantKindTerminalWrite}); err != nil {
		t.Fatal(err)
	}
	h := newChatGrantHarness(guard.Config{Mode: guard.ReadWrite}, "asset-a", grants, recorder)
	_, err := h.invoke("call-1", "send_keys", `{"keys":"touch /tmp/x","enter":true}`)
	requireChatGrantInterrupt(t, err)
	if h.writeCount() != 0 {
		t.Fatal("grant on another device covered this tab")
	}
	if recorder.find("use", "asset-a") != nil {
		t.Fatal("use audited for the ungranted device")
	}
}

func TestChatGrantRevokeRestoresConfirmImmediately(t *testing.T) {
	recorder := &chatGrantRecorder{}
	grants := newChatGrantManager(t, recorder)
	if _, err := grants.Grant(context.Background(), "asset-a", []guard.GrantKind{guard.GrantKindTerminalWrite}); err != nil {
		t.Fatal(err)
	}
	h := newChatGrantHarness(guard.Config{Mode: guard.ReadWrite}, "asset-a", grants, recorder)
	messages, err := h.invoke("call-1", "send_keys", `{"keys":"touch /tmp/x","enter":true}`)
	requireChatGrantResult(t, messages, err, "已发送")
	if err := grants.Revoke(context.Background(), "asset-a"); err != nil {
		t.Fatal(err)
	}
	_, err = h.invoke("call-2", "send_keys", `{"keys":"touch /tmp/x","enter":true}`)
	requireChatGrantInterrupt(t, err)
	if h.writeCount() != 1 {
		t.Fatalf("writes after revoke = %d, want 1", h.writeCount())
	}
	if recorder.find("revoke", "asset-a") == nil {
		t.Fatal("revoke not audited")
	}
}

func TestChatGrantNeverCoversNonEnumOperations(t *testing.T) {
	recorder := &chatGrantRecorder{}
	grants := newChatGrantManager(t, recorder)
	if _, err := grants.Grant(context.Background(), "asset-a", []guard.GrantKind{guard.GrantKindTerminalWrite, guard.GrantKindSessionExec}); err != nil {
		t.Fatal(err)
	}
	h := newChatGrantHarness(guard.Config{Mode: guard.ReadWrite}, "asset-a", grants, recorder)
	_, err := h.invoke("call-1", "docker_exec", `{"container_id":"web","cmd":"touch /tmp/x"}`)
	requireChatGrantInterrupt(t, err)
	if h.dockerRuns != 0 {
		t.Fatal("docker_exec ran under a terminal/session grant")
	}
	_, err = h.invoke("call-2", "docker_control", `{"container_id":"web","action":"start"}`)
	requireChatGrantInterrupt(t, err)
	if h.dockerActs != 0 {
		t.Fatal("docker_control ran under a terminal/session grant")
	}
	if _, err = h.invoke("call-3", "read_file", `{"path":"/tmp/x"}`); err != nil {
		t.Fatalf("read_file setup: %v", err)
	}
	_, err = h.invoke("call-4", "write_file", `{"path":"/tmp/x","content":"hi"}`)
	requireChatGrantInterrupt(t, err)
	h.transport.files.mu.Lock()
	writes := h.transport.files.writeCalls
	h.transport.files.mu.Unlock()
	if writes != 0 {
		t.Fatal("write_file ran under a terminal/session grant")
	}
}

func TestChatGrantNeverCoversDanger(t *testing.T) {
	recorder := &chatGrantRecorder{}
	grants := newChatGrantManager(t, recorder)
	if _, err := grants.Grant(context.Background(), "asset-a", []guard.GrantKind{guard.GrantKindTerminalWrite}); err != nil {
		t.Fatal(err)
	}
	h := newChatGrantHarness(guard.Config{Mode: guard.ReadWrite}, "asset-a", grants, recorder)
	_, err := h.invoke("call-1", "send_keys", `{"keys":"sudo reboot<enter>"}`)
	requireChatGrantInterrupt(t, err)
	if h.writeCount() != 0 {
		t.Fatal("dangerous keys wrote under grant")
	}
}

func TestChatGrantOverridesGlobalModeDeny(t *testing.T) {
	recorder := &chatGrantRecorder{}
	h := newChatGrantHarness(guard.Config{Mode: guard.ReadOnly}, "asset-a", nil, recorder)
	messages, err := h.invoke("call-1", "exec_commands", `{"commands":["touch /tmp/x"]}`)
	if err != nil {
		t.Fatalf("read-only deny must surface as a failed tool result, not an interrupt: %v", err)
	}
	if len(messages) != 1 || !strings.Contains(messages[0].Content, "权限策略已拒绝") {
		t.Fatalf("tool result = %+v", messages)
	}
	if h.transport.calls() != 0 {
		t.Fatal("read-only mode executed the command")
	}

	grants := newChatGrantManager(t, recorder)
	if _, err := grants.Grant(context.Background(), "asset-a", []guard.GrantKind{guard.GrantKindSessionExec}); err != nil {
		t.Fatal(err)
	}
	granted := newChatGrantHarness(guard.Config{Mode: guard.ReadOnly}, "asset-a", grants, recorder)
	messages, err = granted.invoke("call-1", "exec_commands", `{"commands":["touch /tmp/x"]}`)
	requireChatGrantResult(t, messages, err, "touch /tmp/x")
	if granted.transport.calls() != 1 {
		t.Fatal("device grant did not take precedence over the global read-only mode")
	}
}

func TestChatGrantIgnoresClientSuppliedAssetID(t *testing.T) {
	recorder := &chatGrantRecorder{}
	grants := newChatGrantManager(t, recorder)
	if _, err := grants.Grant(context.Background(), "asset-a", []guard.GrantKind{guard.GrantKindTerminalWrite}); err != nil {
		t.Fatal(err)
	}
	t.Run("resolver error fails closed", func(t *testing.T) {
		h := newChatGrantHarness(guard.Config{Mode: guard.ReadWrite}, "asset-a", grants, recorder)
		h.terminal.assetIDErr = errors.New("asset resolution unavailable")
		_, err := h.invoke("call-1", "send_keys", `{"keys":"touch /tmp/x","enter":true}`)
		requireChatGrantInterrupt(t, err)
		if h.writeCount() != 0 {
			t.Fatal("write happened with unresolvable server identity")
		}
	})
	t.Run("missing server resolver ignores scope asset id", func(t *testing.T) {
		h := newChatGrantHarness(guard.Config{Mode: guard.ReadWrite}, "asset-a", grants, recorder)
		h.execution.Scope.AssetID = "asset-a"
		plain := &fakeTerminal{}
		h.execution.Registry = NewRegistry(Dependencies{
			Transport:  func(context.Context, string) (base.Transport, error) { return h.transport, nil },
			Terminal:   plain,
			TabSession: func(tabID string) string { return "session-1" },
			Grants:     grants,
		})
		_, err := h.invoke("call-1", "send_keys", `{"keys":"touch /tmp/x","enter":true}`)
		requireChatGrantInterrupt(t, err)
		if len(plain.writes) != 0 {
			t.Fatal("client-supplied asset id satisfied the device grant")
		}
	})
}

func TestChatGrantResolvesTabSessionIdentity(t *testing.T) {
	recorder := &chatGrantRecorder{}
	grants := newChatGrantManager(t, recorder)
	if _, err := grants.Grant(context.Background(), "asset-a", []guard.GrantKind{guard.GrantKindTerminalWrite}); err != nil {
		t.Fatal(err)
	}
	h := newChatGrantHarness(guard.Config{Mode: guard.ReadWrite}, "asset-a", grants, recorder)
	h.execution.Scope.SessionID = ""
	messages, err := h.invoke("call-1", "send_keys", `{"keys":"touch /tmp/x","enter":true}`)
	requireChatGrantResult(t, messages, err, "已发送")
	if h.writeCount() != 1 {
		t.Fatal("tab-derived session identity did not satisfy the grant")
	}
}

func TestChatGrantConcurrentUseAndRevoke(t *testing.T) {
	recorder := &chatGrantRecorder{}
	grants := newChatGrantManager(t, recorder)
	if _, err := grants.Grant(context.Background(), "asset-a", []guard.GrantKind{guard.GrantKindTerminalWrite}); err != nil {
		t.Fatal(err)
	}
	h := newChatGrantHarness(guard.Config{Mode: guard.ReadWrite}, "asset-a", grants, recorder)
	const workers = 8
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, _ = h.invoke(fmt.Sprintf("call-%d", index), "send_keys", `{"keys":"touch /tmp/x","enter":true}`)
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for round := 0; round < 20; round++ {
			_ = grants.Revoke(context.Background(), "asset-a")
			if _, err := grants.Grant(context.Background(), "asset-a", []guard.GrantKind{guard.GrantKindTerminalWrite}); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	wg.Wait()
	if h.writeCount() > workers {
		t.Fatalf("writes = %d, want at most %d", h.writeCount(), workers)
	}
}
