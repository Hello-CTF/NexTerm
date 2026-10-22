package takeover

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/cloudwego/eino/components/model"
)

var persistentConfirmModes = []struct {
	name       string
	dispatcher bool
}{
	{name: "ConfirmContext"},
	{name: "Dispatcher", dispatcher: true},
}

func newPersistentGrantHarness(t *testing.T, chat model.BaseChatModel, assetID string) (*harness, *grantSettings) {
	t.Helper()
	settings := &grantSettings{values: make(map[string]string)}
	grants, err := guard.NewGrants(context.Background(), settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := newHarnessWith(t, chat, func(deps *Dependencies) {
		deps.Grants = grants
		deps.TabAsset = func(context.Context, string) (string, error) { return assetID, nil }
	})
	return h, settings
}

func beginPersistentConfirmation(t *testing.T, h *harness, ctx context.Context, useDispatcher bool) (agent.Event, *agent.SliceStream, func()) {
	t.Helper()
	var dispatcher *ipc.Dispatcher
	var run RunResponse
	stream := &agent.SliceStream{}
	if useDispatcher {
		dispatcher = ipc.NewDispatcher()
		if err := h.manager.RegisterCommands(dispatcher); err != nil {
			t.Fatal(err)
		}
		token := enterTakeoverIPC(t, dispatcher, ctx, "tab")
		run = runTakeoverIPC(t, dispatcher, ctx, RunArgs{TabID: "tab", Token: token, Instruction: "go"}, stream)
	} else {
		token, err := h.manager.Enter(ctx, "tab")
		if err != nil {
			t.Fatal(err)
		}
		run, err = h.manager.Run(ctx, RunArgs{TabID: "tab", Token: token, Instruction: "go"}, agent.StaticStream(stream))
		if err != nil {
			t.Fatal(err)
		}
	}
	confirmation := waitEvent(t, stream, "confirmRequired")
	return confirmation, stream, func() {
		t.Helper()
		args := agent.Confirmation{JobID: run.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow_persistent"}
		if useDispatcher {
			if response := dispatchTakeover(t, dispatcher, ctx, "ai_takeover_confirm", args, ipc.Environment{}); !response.OK {
				t.Fatalf("persistent confirmation failed: %+v", response)
			}
			return
		}
		if err := h.manager.ConfirmContext(ctx, args); err != nil {
			t.Fatal(err)
		}
	}
}

func mustPersistentRules(t *testing.T, h *harness, ctx context.Context) []guard.GrantRule {
	t.Helper()
	rules, err := h.manager.deps.Grants.Rules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return rules
}

func persistedRulesFor(t *testing.T, settings *grantSettings, ctx context.Context) ([]guard.GrantRule, bool) {
	t.Helper()
	settings.mu.Lock()
	raw, found := settings.values[guard.GrantRulesSettingKeyForUser(ctx)]
	settings.mu.Unlock()
	if !found {
		return nil, false
	}
	var rules []guard.GrantRule
	if err := json.Unmarshal([]byte(raw), &rules); err != nil {
		t.Fatal(err)
	}
	return rules, true
}

func TestTakeoverAllowPersistentConfirmContextAndDispatcher(t *testing.T) {
	for _, mode := range persistentConfirmModes {
		t.Run(mode.name, func(t *testing.T) {
			chat := sequence(
				toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"touch /tmp/x","enter":true}`)),
				toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"touch /tmp/x","enter":true}`)),
				toolCallMessage(doneCall("finished")),
			)
			h, settings := newPersistentGrantHarness(t, chat, "asset-a")
			alice := ipc.WithUserID(context.Background(), "account-a")
			bob := ipc.WithUserID(context.Background(), "account-b")
			confirmation, stream, confirm := beginPersistentConfirmation(t, h, alice, mode.dispatcher)
			if confirmation.Risk != "needs_confirm" {
				t.Fatalf("confirmation risk = %q", confirmation.Risk)
			}
			confirm()
			events := waitClosed(t, stream)
			if done, failed := counts(events); done != 1 || failed != 0 {
				t.Fatalf("events = %+v", events)
			}
			rules := mustPersistentRules(t, h, alice)
			if len(rules) != 1 || rules[0].DeviceID != "asset-a" || rules[0].Action != "send_keys" || rules[0].Path != "" {
				t.Fatalf("owner rules = %+v", rules)
			}
			if rules := mustPersistentRules(t, h, bob); len(rules) != 0 {
				t.Fatalf("other owner received rule: %+v", rules)
			}
			persisted, found := persistedRulesFor(t, settings, alice)
			if !found || len(persisted) != 1 || persisted[0].DeviceID != "asset-a" || persisted[0].Action != "send_keys" {
				t.Fatalf("persisted owner rules = %+v, found = %v", persisted, found)
			}
			if _, found := persistedRulesFor(t, settings, bob); found {
				t.Fatal("rule persisted under another owner")
			}
			settings.mu.Lock()
			_, globalFound := settings.values[guard.GrantRulesSettingKey]
			settings.mu.Unlock()
			if globalFound {
				t.Fatal("account rule persisted under global key")
			}
			h.mu.Lock()
			defer h.mu.Unlock()
			if len(h.aiWrites) != 1 || string(h.aiWrites[0]) != "touch /tmp/x\r" {
				t.Fatalf("terminal writes = %q", h.aiWrites)
			}
		})
	}
}

func TestTakeoverAllowPersistentFailsClosed(t *testing.T) {
	tests := []struct {
		name       string
		command    string
		risk       string
		assetID    string
		withGrants bool
		want       string
	}{
		{name: "danger", command: "sudo reboot", risk: "danger", assetID: "asset-a", withGrants: true, want: "不支持永久授权"},
		{name: "unknowable", command: "echo $x", risk: "unknowable", assetID: "asset-a", withGrants: true, want: "不支持永久授权"},
		{name: "missing asset", command: "touch /tmp/x", risk: "needs_confirm", withGrants: true, want: "无法确定设备身份"},
		{name: "missing grants", command: "touch /tmp/x", risk: "needs_confirm", assetID: "asset-a", want: "永久授权未配置"},
	}
	for _, mode := range persistentConfirmModes {
		for _, test := range tests {
			t.Run(mode.name+"/"+test.name, func(t *testing.T) {
				chat := sequence(
					toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":`+strconv.Quote(test.command)+`,"enter":true}`)),
					toolCallMessage(doneCall("finished")),
				)
				var h *harness
				if test.withGrants {
					h, _ = newPersistentGrantHarness(t, chat, test.assetID)
				} else {
					h = newHarnessWith(t, chat, func(deps *Dependencies) {
						deps.TabAsset = func(context.Context, string) (string, error) { return test.assetID, nil }
					})
				}
				alice := ipc.WithUserID(context.Background(), "account-a")
				confirmation, stream, confirm := beginPersistentConfirmation(t, h, alice, mode.dispatcher)
				if confirmation.Risk != test.risk {
					t.Fatalf("confirmation risk = %q, want %q", confirmation.Risk, test.risk)
				}
				confirm()
				_ = waitClosed(t, stream)
				result := waitEvent(t, stream, "toolResult")
				if result.OK || !strings.Contains(result.Text, test.want) {
					t.Fatalf("tool result = %+v, want failure containing %q", result, test.want)
				}
				if test.withGrants {
					if rules := mustPersistentRules(t, h, alice); len(rules) != 0 {
						t.Fatalf("failed permanent confirmation created rules: %+v", rules)
					}
				}
				h.mu.Lock()
				defer h.mu.Unlock()
				if len(h.aiWrites) != 0 {
					t.Fatalf("failed permanent confirmation wrote terminal: %q", h.aiWrites)
				}
			})
		}
	}
}
