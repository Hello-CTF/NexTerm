package takeover

import (
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
)

func TestRepeatedUnknownToolCallsEndTakeover(t *testing.T) {
	h := newHarness(t, sequence(toolCallMessage(namedToolCall("ghost", "no_such_tool", `{}`))))
	stream := &agent.SliceStream{}
	h.run(t, stream, RunArgs{})
	events := waitClosed(t, stream)
	done, failed := counts(events)
	if done != 0 || failed != 1 {
		t.Fatalf("events=%+v", events)
	}
	if last := events[len(events)-1]; !strings.Contains(last.Message, "模型反复调用失败的工具 no_such_tool") {
		t.Fatalf("last=%+v", last)
	}
}
