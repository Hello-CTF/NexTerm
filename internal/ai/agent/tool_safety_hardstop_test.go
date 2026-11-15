package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
)

func TestRepeatedUnknownToolCallsEndRunAsFailedWithPersistedResults(t *testing.T) {
	// The fake model repeats its last step forever, as a looping model would.
	chat := sequenceModel(toolCallMessage(namedToolCall("ghost", "no_such_tool", `{}`)))
	runner, storage := testRunner(t, chat, tools.Dependencies{}, 0)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	events := waitClosed(t, stream)
	if done, failed := terminalCounts(events); done != 0 || failed != 1 {
		t.Fatalf("terminal events done=%d error=%d events=%+v", done, failed, events)
	}
	last := events[len(events)-1]
	if last.Type != "error" || !strings.Contains(last.Message, "模型反复调用失败的工具 no_such_tool") {
		t.Fatalf("last=%+v", last)
	}
	rows, err := storage.MsgList(context.Background(), response.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	persisted := 0
	for _, row := range rows {
		if row.Role == historyTypeToolResult && strings.Contains(row.ContentJSON, `"ok":false`) {
			persisted++
		}
	}
	if persisted < 6 {
		t.Fatalf("persisted failed tool results=%d", persisted)
	}
}
