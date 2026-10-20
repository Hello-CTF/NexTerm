package takeover

import (
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
)

func TestR1TakeoverResumeRejectsChangedCursor(t *testing.T) {
	chat := sequence(
		toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"<backspace><enter>"}`)),
		toolCallMessage(doneCall("finished")),
	)
	h := newHarness(t, chat)
	h.screen.Text = "$ touch /tmp/x"
	stream := &agent.SliceStream{}
	response := h.run(t, stream, RunArgs{})
	confirmation := waitEvent(t, stream, "confirmRequired")
	h.mu.Lock()
	h.screen.CursorCol = 14
	h.mu.Unlock()
	if err := h.manager.Confirm(agent.Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, stream)
	result := waitEvent(t, stream, "toolResult")
	if result.OK || !strings.Contains(result.Text, tools.ErrTerminalInputChanged.Error()) {
		t.Fatalf("cursor-only resume result = %+v, events = %+v", result, events)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.aiWrites) != 0 {
		t.Fatalf("changed cursor was written: %q", h.aiWrites)
	}
}
