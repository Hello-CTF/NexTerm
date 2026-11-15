package takeover

import (
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/cloudwego/eino/schema"
)

func TestMalformedAndUnknownToolCallsDoNotEndTakeover(t *testing.T) {
	chat := sequence(
		toolCallMessage(namedToolCall("bad", "send_keys", `{"keys":"ls`)),
		toolCallMessage(namedToolCall("ghost", "rm_everything", `{}`)),
		toolCallMessage(doneCall("recovered")),
		schema.AssistantMessage("end", nil),
	)
	h := newHarness(t, chat)
	stream := &agent.SliceStream{}
	h.run(t, stream, RunArgs{})
	events := waitClosed(t, stream)
	results := map[string]agent.Event{}
	for _, event := range events {
		if event.Type == "toolResult" {
			results[event.ID] = event
		}
		if event.Type == "error" {
			t.Fatalf("takeover ended with an error: %+v", event)
		}
	}
	if bad := results["bad"]; bad.OK || !strings.Contains(bad.Text, "JSON") {
		t.Fatalf("malformed result=%+v", bad)
	}
	ghost := results["ghost"]
	if ghost.OK || !strings.Contains(ghost.Text, "rm_everything") || !strings.Contains(ghost.Text, "send_keys") || strings.Contains(ghost.Text, "exec_commands") {
		t.Fatalf("unknown result=%+v", ghost)
	}
	if len(h.aiWrites) != 0 {
		t.Fatalf("rejected calls wrote to the terminal: %q", h.aiWrites)
	}
}
