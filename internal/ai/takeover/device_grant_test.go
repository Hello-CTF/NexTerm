package takeover

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/cloudwego/eino/components/model"
)

type grantSettings struct {
	mu     sync.Mutex
	values map[string]string
}

func (s *grantSettings) SettingGet(_ context.Context, key string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[key]
	return value, ok, nil
}

func (s *grantSettings) SettingSet(_ context.Context, key, value string) error {
	s.mu.Lock()
	s.values[key] = value
	s.mu.Unlock()
	return nil
}

type grantRecorder struct {
	mu     sync.Mutex
	events []guard.GrantAuditEvent
}

func (r *grantRecorder) record(_ context.Context, event guard.GrantAuditEvent) error {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
	return nil
}

func (r *grantRecorder) find(action, deviceID string) *guard.GrantAuditEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.events {
		if r.events[i].Action == action && r.events[i].DeviceID == deviceID {
			return &r.events[i]
		}
	}
	return nil
}

func newGrantHarness(t *testing.T, chat model.BaseChatModel, assetID string, grant func(context.Context, *guard.Grants)) (*harness, *grantRecorder) {
	t.Helper()
	recorder := &grantRecorder{}
	grants, err := guard.NewGrants(context.Background(), &grantSettings{values: make(map[string]string)}, recorder.record)
	if err != nil {
		t.Fatal(err)
	}
	if grant != nil {
		grant(context.Background(), grants)
	}
	h := newHarnessWith(t, chat, func(deps *Dependencies) {
		deps.Grants = grants
		deps.TabAsset = func(context.Context, string) (string, error) { return assetID, nil }
	})
	return h, recorder
}

func TestDeviceGrantDefaultOffRequiresConfirm(t *testing.T) {
	chat := sequence(
		toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"touch /tmp/x","enter":true}`)),
		toolCallMessage(doneCall("stopped")),
	)
	h, recorder := newGrantHarness(t, chat, "asset-a", nil)
	stream := &agent.SliceStream{}
	h.run(t, stream, RunArgs{})
	waitEvent(t, stream, "confirmRequired")
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.aiWrites) != 0 {
		t.Fatal("write happened without grant")
	}
	if recorder.find("use", "asset-a") != nil {
		t.Fatal("use audited without grant")
	}
}

func TestDeviceGrantAllowsTerminalWriteAndAuditsUse(t *testing.T) {
	chat := sequence(
		toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"touch /tmp/x","enter":true}`)),
		toolCallMessage(doneCall("finished")),
	)
	h, recorder := newGrantHarness(t, chat, "asset-a", func(ctx context.Context, grants *guard.Grants) {
		if _, err := grants.Grant(ctx, "asset-a", []guard.GrantKind{guard.GrantKindTerminalWrite}); err != nil {
			t.Fatal(err)
		}
	})
	stream := &agent.SliceStream{}
	response := h.run(t, stream, RunArgs{})
	events := waitClosed(t, stream)
	for _, event := range events {
		if event.Type == "confirmRequired" {
			t.Fatal("granted send_keys still required confirmation")
		}
	}
	if done, failed := counts(events); done != 1 || failed != 0 {
		t.Fatalf("events=%+v", events)
	}
	h.mu.Lock()
	if len(h.aiWrites) != 1 || string(h.aiWrites[0]) != "touch /tmp/x\r" {
		t.Fatalf("writes=%q", h.aiWrites)
	}
	audits := append([]tools.AuditEntry(nil), h.audits...)
	h.mu.Unlock()
	use := recorder.find("use", "asset-a")
	if use == nil || use.Operation != "send_keys" || use.RunID != response.JobID {
		t.Fatalf("use event = %+v", use)
	}
	if len(audits) != 1 {
		t.Fatalf("write audits = %+v", audits)
	}
	payload, err := json.Marshal(audits[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	if strings.Contains(text, "touch") || strings.Contains(text, "why") {
		t.Fatalf("audit payload leaks keys: %s", text)
	}
	fields, ok := audits[0].Payload.(map[string]any)
	if !ok || fields["keys"] != "<redacted:13 bytes>" || fields["replay"] != false {
		t.Fatalf("audit payload = %+v", audits[0].Payload)
	}
	if audits[0].AssetID != "asset-a" || audits[0].Kind != "takeover" {
		t.Fatalf("audit entry = %+v", audits[0])
	}
}

func TestDeviceGrantCrossDeviceIsolation(t *testing.T) {
	chat := sequence(
		toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"touch /tmp/x","enter":true}`)),
		toolCallMessage(doneCall("stopped")),
	)
	h, _ := newGrantHarness(t, chat, "asset-a", func(ctx context.Context, grants *guard.Grants) {
		if _, err := grants.Grant(ctx, "asset-b", []guard.GrantKind{guard.GrantKindTerminalWrite}); err != nil {
			t.Fatal(err)
		}
	})
	stream := &agent.SliceStream{}
	h.run(t, stream, RunArgs{})
	waitEvent(t, stream, "confirmRequired")
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.aiWrites) != 0 {
		t.Fatal("grant on another device covered this tab")
	}
}

func TestDeviceGrantRevokeRestoresConfirm(t *testing.T) {
	chat := sequence(
		toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"touch /tmp/x","enter":true}`)),
		toolCallMessage(doneCall("stopped")),
	)
	h, recorder := newGrantHarness(t, chat, "asset-a", func(ctx context.Context, grants *guard.Grants) {
		if _, err := grants.Grant(ctx, "asset-a", []guard.GrantKind{guard.GrantKindTerminalWrite}); err != nil {
			t.Fatal(err)
		}
		if err := grants.Revoke(ctx, "asset-a"); err != nil {
			t.Fatal(err)
		}
	})
	stream := &agent.SliceStream{}
	h.run(t, stream, RunArgs{})
	waitEvent(t, stream, "confirmRequired")
	if recorder.find("revoke", "asset-a") == nil {
		t.Fatal("revoke not audited")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.aiWrites) != 0 {
		t.Fatal("write happened after revoke")
	}
}

func TestDeviceGrantNeverCoversDanger(t *testing.T) {
	chat := sequence(
		toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"sudo reboot<enter>"}`)),
		toolCallMessage(doneCall("stopped")),
	)
	h, _ := newGrantHarness(t, chat, "asset-a", func(ctx context.Context, grants *guard.Grants) {
		if _, err := grants.Grant(ctx, "asset-a", []guard.GrantKind{guard.GrantKindTerminalWrite}); err != nil {
			t.Fatal(err)
		}
	})
	stream := &agent.SliceStream{}
	h.run(t, stream, RunArgs{})
	confirmation := waitEvent(t, stream, "confirmRequired")
	if confirmation.Risk != "danger" {
		t.Fatalf("risk=%s", confirmation.Risk)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.aiWrites) != 0 {
		t.Fatal("dangerous keys wrote under grant")
	}
}
