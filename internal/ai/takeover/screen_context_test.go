package takeover

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestModelInputKeepsOnlyLatestScreen(t *testing.T) {
	var mu sync.Mutex
	var inputs [][]*schema.Message
	calls := 0
	chat := &fakeModel{}
	chat.stream = func(_ context.Context, messages []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		mu.Lock()
		defer mu.Unlock()
		inputs = append(inputs, append([]*schema.Message(nil), messages...))
		calls++
		reply := toolCallMessage(namedToolCall(fmt.Sprintf("scr%d", calls), "read_screen", `{}`))
		if calls == 4 {
			reply = toolCallMessage(doneCall("finished"))
		}
		return schema.StreamReaderFromArray([]*schema.Message{reply}), nil
	}
	h := newHarness(t, chat)
	snapshots := 0
	h.snapshot = func(context.Context, string) (tools.Screen, error) {
		mu.Lock()
		defer mu.Unlock()
		snapshots++
		return tools.Screen{Text: fmt.Sprintf("screen-%d", snapshots), IdleMS: 400}, nil
	}
	stream := &agent.SliceStream{}
	h.run(t, stream, RunArgs{MaxSteps: 8})
	waitClosed(t, stream)

	mu.Lock()
	defer mu.Unlock()
	if len(inputs) != 4 {
		t.Fatalf("model calls = %d, want 4", len(inputs))
	}
	for index, input := range inputs {
		var screens []string
		for _, message := range input {
			if strings.HasPrefix(message.Content, "[Current Terminal]") {
				screens = append(screens, message.Content)
			}
		}
		if len(screens) != 1 {
			t.Fatalf("call %d carries %d screen messages, want 1", index+1, len(screens))
		}
		if last := input[len(input)-1]; last.Content != screens[0] {
			t.Fatalf("call %d: latest message is not the screen: %q", index+1, last.Content)
		}
	}
	first := inputs[0][len(inputs[0])-1].Content
	latest := inputs[3][len(inputs[3])-1].Content
	if first == latest {
		t.Fatalf("screen did not advance between calls: %q", latest)
	}
}
