package takeover

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
)

func TestLegacyToolSchemasDoNotAdvertiseGenericExtensions(t *testing.T) {
	available, err := (&actionExecution{}).tools()
	if err != nil {
		t.Fatal(err)
	}
	properties := map[string]map[string]any{}
	for _, candidate := range available {
		info, err := candidate.Info(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		converted, err := info.ParamsOneOf.ToJSONSchema()
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(converted)
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Properties map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(encoded, &schema); err != nil {
			t.Fatal(err)
		}
		properties[info.Name] = schema.Properties
	}
	for _, name := range []string{"paste", "quiet_ms"} {
		if _, found := properties["send_keys"][name]; found {
			t.Fatalf("send_keys advertises unsupported %s", name)
		}
		if _, found := properties["wait_for"][name]; found {
			t.Fatalf("wait_for advertises unsupported %s", name)
		}
	}
	if _, found := properties["wait_for"]["tab_id"]; found {
		t.Fatal("wait_for advertises unsupported tab_id")
	}
	if len(properties["send_keys"]) != 3 || len(properties["wait_for"]) != 2 {
		t.Fatalf("legacy schema contains unsupported fields: send_keys=%v wait_for=%v", properties["send_keys"], properties["wait_for"])
	}
	for _, name := range []string{"keys", "enter", "tab_id"} {
		if _, found := properties["send_keys"][name]; !found {
			t.Fatalf("legacy send_keys field %s is missing", name)
		}
	}
	for _, name := range []string{"pattern", "timeout_ms"} {
		if _, found := properties["wait_for"][name]; !found {
			t.Fatalf("legacy wait_for field %s is missing", name)
		}
	}
}

func TestLegacyAdvertisedFieldsDriveToolsNodeBehavior(t *testing.T) {
	t.Run("keys and enter", func(t *testing.T) {
		chat := sequence(toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"ls","enter":true}`)), toolCallMessage(doneCall("done")))
		h := newHarness(t, chat)
		stream := &agent.SliceStream{}
		h.run(t, stream, RunArgs{})
		result := waitEvent(t, stream, "toolResult")
		if !result.OK {
			t.Fatalf("send_keys = %+v", result)
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		if len(h.aiWrites) != 1 || string(h.aiWrites[0]) != "ls\r" {
			t.Fatalf("writes = %q", h.aiWrites)
		}
	})

	t.Run("pattern match", func(t *testing.T) {
		chat := sequence(toolCallMessage(namedToolCall("wait", "wait_for", `{"pattern":"READY"}`)), toolCallMessage(doneCall("done")))
		h := newHarness(t, chat)
		h.screen.Text = "READY"
		h.screen.Tail = []string{"READY"}
		stream := &agent.SliceStream{}
		h.run(t, stream, RunArgs{})
		if result := waitEvent(t, stream, "toolResult"); !result.OK {
			t.Fatalf("wait_for = %+v", result)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		chat := sequence(toolCallMessage(namedToolCall("wait", "wait_for", `{"pattern":"never","timeout_ms":1}`)), toolCallMessage(doneCall("done")))
		h := newHarness(t, chat)
		stream := &agent.SliceStream{}
		h.run(t, stream, RunArgs{})
		result := waitEvent(t, stream, "toolResult")
		if result.OK || !strings.Contains(result.Text, "等待超时") {
			t.Fatalf("timeout result = %+v", result)
		}
	})
}
