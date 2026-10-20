package takeover

import (
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
)

func TestR1TakeoverResumeRejectsChangedTerminalInput(t *testing.T) {
	chat := sequence(
		toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"<enter>"}`)),
		toolCallMessage(doneCall("finished")),
	)
	h := newHarness(t, chat)
	h.screen.Text = "$ touch /tmp/x"
	stream := &agent.SliceStream{}
	response := h.run(t, stream, RunArgs{})
	confirmation := waitEvent(t, stream, "confirmRequired")
	h.mu.Lock()
	h.screen.Text = "$ rm -rf /home/user"
	h.mu.Unlock()
	if err := h.manager.Confirm(agent.Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, stream)
	result := waitEvent(t, stream, "toolResult")
	if result.OK || !strings.Contains(result.Text, tools.ErrTerminalInputChanged.Error()) {
		t.Fatalf("resume result = %+v, events = %+v", result, events)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.aiWrites) != 0 {
		t.Fatalf("changed input was written: %q", h.aiWrites)
	}
}
