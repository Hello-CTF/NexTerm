package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/cloudwego/eino/schema"
)

func TestR1ResumeRejectsChangedCursor(t *testing.T) {
	terminal := &bufferTerminal{screen: tools.Screen{Text: "$ touch /tmp/x"}}
	chat := &fakeModel{steps: []fakeStep{
		{message: toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"<backspace><enter>"}`))},
		{message: schema.AssistantMessage("done", nil)},
	}}
	runner, _ := testRunner(t, chat, tools.Dependencies{Terminal: terminal}, 0)
	stream := &SliceStream{}
	response, err := runner.Start(context.Background(), ChatArgs{Message: "go", Scope: tools.Scope{SessionID: "session", TabID: "tab"}}, StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	confirmation := waitEvent(t, stream, "confirmRequired")
	terminal.mu.Lock()
	terminal.screen.CursorCol = 14
	terminal.mu.Unlock()
	if err := runner.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, stream)
	result := waitEvent(t, stream, "toolResult")
	if result.OK || !strings.Contains(result.Text, tools.ErrTerminalInputChanged.Error()) {
		t.Fatalf("cursor-only resume result = %+v, events = %+v", result, events)
	}
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	if terminal.writes != 0 {
		t.Fatalf("changed cursor was written %d times", terminal.writes)
	}
}
