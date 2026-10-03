package takeover

import (
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/agent"
)

func TestR1TakeoverRejectsBufferedDangerousSubmission(t *testing.T) {
	chat := sequence(
		toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"; ls","enter":true}`)),
		toolCallMessage(doneCall("stopped")),
	)
	h := newHarness(t, chat)
	h.screen.Text = "$ rm -rf /home/user"
	h.screen.CursorCol = len(h.screen.Text)
	stream := &agent.SliceStream{}
	h.run(t, stream, RunArgs{})
	events := waitClosed(t, stream)
	result := waitEvent(t, stream, "toolResult")
	if result.OK {
		t.Fatalf("dangerous buffered submission succeeded: %+v", events)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.aiWrites) != 0 {
		t.Fatalf("dangerous buffered submission wrote %q", h.aiWrites)
	}
}
