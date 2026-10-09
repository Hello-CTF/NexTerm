package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/guard"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
	"github.com/cloudwego/eino/schema"
)

type ruleSettings struct {
	mu     sync.Mutex
	values map[string]string
}

func (s *ruleSettings) SettingGet(_ context.Context, key string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[key]
	return value, ok, nil
}

func (s *ruleSettings) SettingSet(_ context.Context, key, value string) error {
	s.mu.Lock()
	s.values[key] = value
	s.mu.Unlock()
	return nil
}

type ruleTerminal struct {
	assetID    string
	assetIDErr bool
}

func (r ruleTerminal) Snapshot(context.Context, string) (tools.Screen, error) {
	return tools.Screen{Cols: 80, Rows: 24}, nil
}

func (r ruleTerminal) Write(context.Context, string, []byte) error { return nil }

func (r ruleTerminal) SessionAssetID(context.Context, string) (string, error) {
	if r.assetIDErr {
		return "", errors.New("asset resolution unavailable")
	}
	return r.assetID, nil
}

func newRuleGrants(t *testing.T) *guard.Grants {
	t.Helper()
	manager, err := guard.NewGrants(context.Background(), &ruleSettings{values: make(map[string]string)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func startScopedTestJob(t *testing.T, runner *Runner, stream *SliceStream) StartResponse {
	t.Helper()
	response, err := runner.Start(context.Background(), ChatArgs{Message: "go", Scope: tools.Scope{SessionID: "session", TabID: "tab-1"}}, StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func confirmEvents(events []Event) []Event {
	var out []Event
	for _, event := range events {
		if event.Type == "confirmRequired" {
			out = append(out, event)
		}
	}
	return out
}

func toolResultOf(t *testing.T, events []Event, callID string) Event {
	t.Helper()
	for _, event := range events {
		if event.Type == "toolResult" && event.ID == callID {
			return event
		}
	}
	t.Fatalf("toolResult %s not found", callID)
	return Event{}
}

func TestAllowPersistentWriteFileCreatesRuleAndCovers(t *testing.T) {
	transport, directory := localTransport(t)
	if err := os.MkdirAll(filepath.Join(directory, "logs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(directory, "other"), 0o700); err != nil {
		t.Fatal(err)
	}
	grants := newRuleGrants(t)
	deps := localDeps(transport)
	deps.Terminal = ruleTerminal{assetID: "asset-a"}
	deps.Grants = grants
	deps.TabSession = func(string) string { return "session" }

	inside := filepath.Join(directory, "logs", "app.log")
	sibling := filepath.Join(directory, "logs", "second.log")
	outside := filepath.Join(directory, "other", "app.log")
	encodedInside, _ := json.Marshal(inside)
	encodedSibling, _ := json.Marshal(sibling)
	encodedOutside, _ := json.Marshal(outside)
	chat := sequenceModel(
		toolCallMessage(namedToolCall("read", "read_file", `{"path":`+string(encodedInside)+`}`)),
		toolCallMessage(namedToolCall("write", "write_file", `{"path":`+string(encodedInside)+`,"content":"one"}`)),
		toolCallMessage(namedToolCall("read2", "read_file", `{"path":`+string(encodedSibling)+`}`)),
		toolCallMessage(namedToolCall("write2", "write_file", `{"path":`+string(encodedSibling)+`,"content":"two"}`)),
		toolCallMessage(namedToolCall("read3", "read_file", `{"path":`+string(encodedOutside)+`}`)),
		toolCallMessage(namedToolCall("write3", "write_file", `{"path":`+string(encodedOutside)+`,"content":"three"}`)),
		schema.AssistantMessage("done", nil),
	)
	runner, _ := testRunner(t, chat, deps, 0)
	stream := &SliceStream{}
	response := startScopedTestJob(t, runner, stream)

	first := waitEvent(t, stream, "confirmRequired")
	if first.Risk != "needs_confirm" {
		t.Fatalf("first confirm risk = %q", first.Risk)
	}
	if want := guard.DirPattern(inside); first.RulePattern != want {
		t.Fatalf("rulePattern = %q, want %q", first.RulePattern, want)
	}
	if err := runner.Confirm(Confirmation{JobID: response.JobID, CallID: first.ID, Nonce: first.Nonce, Decision: "allow_persistent"}); err != nil {
		t.Fatal(err)
	}
	second := waitEventIndex(t, stream, "confirmRequired", 1)
	if err := runner.Confirm(Confirmation{JobID: response.JobID, CallID: second.ID, Nonce: second.Nonce, Decision: "deny"}); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, stream)
	confirmations := confirmEvents(events)
	if len(confirmations) != 2 {
		t.Fatalf("confirmations = %d, want 2 (second write must be covered by the rule)", len(confirmations))
	}
	rules := grants.Rules()
	if len(rules) != 1 || rules[0].Action != "write_file" || rules[0].DeviceID != "asset-a" || rules[0].Path != guard.DirPattern(inside) {
		t.Fatalf("rules = %+v", rules)
	}
	content, err := os.ReadFile(inside)
	if err != nil || string(content) != "one" {
		t.Fatalf("inside content = %q err=%v", content, err)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("denied write still landed: %v", err)
	}
}

func TestAllowPersistentRejectedForDanger(t *testing.T) {
	grants := newRuleGrants(t)
	deps := tools.Dependencies{Terminal: ruleTerminal{assetID: "asset-a"}, Grants: grants, TabSession: func(string) string { return "session" }}
	chat := sequenceModel(
		toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"sudo reboot<enter>"}`)),
		schema.AssistantMessage("done", nil),
	)
	runner, _ := testRunner(t, chat, deps, 0)
	stream := &SliceStream{}
	response := startScopedTestJob(t, runner, stream)
	confirmation := waitEvent(t, stream, "confirmRequired")
	if confirmation.Risk != "danger" {
		t.Fatalf("confirm risk = %q", confirmation.Risk)
	}
	if err := runner.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow_persistent"}); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, stream)
	result := toolResultOf(t, events, "keys")
	if result.OK || !strings.Contains(result.Text, "不支持永久授权") {
		t.Fatalf("result = %+v", result)
	}
	if rules := grants.Rules(); len(rules) != 0 {
		t.Fatalf("danger ruling created a rule: %+v", rules)
	}
}

func TestAllowPersistentFailsClosedWithoutDeviceIdentity(t *testing.T) {
	transport, directory := localTransport(t)
	grants := newRuleGrants(t)
	deps := localDeps(transport)
	deps.Terminal = ruleTerminal{assetIDErr: true}
	deps.Grants = grants
	deps.TabSession = func(string) string { return "session" }

	path := filepath.Join(directory, "app.log")
	encodedPath, _ := json.Marshal(path)
	chat := sequenceModel(
		toolCallMessage(namedToolCall("read", "read_file", `{"path":`+string(encodedPath)+`}`)),
		toolCallMessage(namedToolCall("write", "write_file", `{"path":`+string(encodedPath)+`,"content":"one"}`)),
		schema.AssistantMessage("done", nil),
	)
	runner, _ := testRunner(t, chat, deps, 0)
	stream := &SliceStream{}
	response := startScopedTestJob(t, runner, stream)
	confirmation := waitEvent(t, stream, "confirmRequired")
	if err := runner.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow_persistent"}); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, stream)
	result := toolResultOf(t, events, "write")
	if result.OK || !strings.Contains(result.Text, "无法确定设备身份") {
		t.Fatalf("result = %+v", result)
	}
	if rules := grants.Rules(); len(rules) != 0 {
		t.Fatalf("rule created without device identity: %+v", rules)
	}
}

func TestAllowSessionWriteFileScopedToDirectory(t *testing.T) {
	transport, directory := localTransport(t)
	if err := os.MkdirAll(filepath.Join(directory, "logs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(directory, "other"), 0o700); err != nil {
		t.Fatal(err)
	}
	deps := localDeps(transport)
	deps.Terminal = ruleTerminal{assetID: "asset-a"}
	deps.TabSession = func(string) string { return "session" }

	first := filepath.Join(directory, "logs", "a.log")
	second := filepath.Join(directory, "logs", "b.log")
	outside := filepath.Join(directory, "other", "c.log")
	encodedFirst, _ := json.Marshal(first)
	encodedSecond, _ := json.Marshal(second)
	encodedOutside, _ := json.Marshal(outside)
	chat := sequenceModel(
		toolCallMessage(namedToolCall("read", "read_file", `{"path":`+string(encodedFirst)+`}`)),
		toolCallMessage(namedToolCall("write", "write_file", `{"path":`+string(encodedFirst)+`,"content":"one"}`)),
		toolCallMessage(namedToolCall("read2", "read_file", `{"path":`+string(encodedSecond)+`}`)),
		toolCallMessage(namedToolCall("write2", "write_file", `{"path":`+string(encodedSecond)+`,"content":"two"}`)),
		toolCallMessage(namedToolCall("read3", "read_file", `{"path":`+string(encodedOutside)+`}`)),
		toolCallMessage(namedToolCall("write3", "write_file", `{"path":`+string(encodedOutside)+`,"content":"three"}`)),
		schema.AssistantMessage("done", nil),
	)
	runner, _ := testRunner(t, chat, deps, 0)
	stream := &SliceStream{}
	response := startScopedTestJob(t, runner, stream)
	confirmation := waitEvent(t, stream, "confirmRequired")
	if err := runner.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow_session"}); err != nil {
		t.Fatal(err)
	}
	second_confirmation := waitEventIndex(t, stream, "confirmRequired", 1)
	if err := runner.Confirm(Confirmation{JobID: response.JobID, CallID: second_confirmation.ID, Nonce: second_confirmation.Nonce, Decision: "deny"}); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, stream)
	confirmations := confirmEvents(events)
	if len(confirmations) != 2 {
		t.Fatalf("confirmations = %d, want 2 (same-directory write must be covered by the session rule)", len(confirmations))
	}
	content, err := os.ReadFile(second)
	if err != nil || string(content) != "two" {
		t.Fatalf("second content = %q err=%v", content, err)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("denied write still landed: %v", err)
	}
}
