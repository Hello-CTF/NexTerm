package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
	"github.com/cloudwego/eino/schema"
)

type bufferTerminal struct {
	mu     sync.Mutex
	screen tools.Screen
	writes int
}

func (f *bufferTerminal) Snapshot(context.Context, string) (tools.Screen, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.screen, nil
}

func (f *bufferTerminal) Write(context.Context, string, []byte) error {
	f.mu.Lock()
	f.writes++
	f.mu.Unlock()
	return nil
}

func TestR1ResumeRejectsChangedTerminalInput(t *testing.T) {
	terminal := &bufferTerminal{screen: tools.Screen{Text: "$ touch /tmp/x"}}
	chat := &fakeModel{steps: []fakeStep{
		{message: toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"<enter>"}`))},
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
	terminal.screen.Text = "$ rm -rf /home/user"
	terminal.mu.Unlock()
	if err := runner.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, stream)
	result := waitEvent(t, stream, "toolResult")
	if result.OK || !strings.Contains(result.Text, tools.ErrTerminalInputChanged.Error()) {
		t.Fatalf("resume result = %+v, events = %+v", result, events)
	}
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	if terminal.writes != 0 {
		t.Fatalf("changed input was written %d times", terminal.writes)
	}
}
