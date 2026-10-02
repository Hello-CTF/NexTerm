package agent

import (
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestFitMessageBudgetKeepsNewestFourToolResults(t *testing.T) {
	t.Parallel()
	messages := []*schema.Message{schema.SystemMessage("system"), schema.ToolMessage(strings.Repeat("x", 1000), "old")}
	for i := 0; i < 4; i++ {
		messages = append(messages, schema.ToolMessage("x", "recent"))
	}
	messages = append(messages, schema.UserMessage("current"))
	fitted, compacted, err := fitMessageBudget(messages, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !compacted || fitted[1].Content != "[较早的工具输出已省略]" {
		t.Fatalf("oldest tool result was not compacted: %+v", fitted)
	}
	for i := 2; i < len(fitted)-1; i++ {
		if fitted[i].Content == "[较早的工具输出已省略]" {
			t.Fatalf("newest four tool results were compacted: %+v", fitted)
		}
	}
	_, _, err = fitMessageBudget([]*schema.Message{schema.SystemMessage("system"), schema.UserMessage(strings.Repeat("u", 1000))}, 100)
	if err == nil {
		t.Fatal("oversized current input was sent to provider")
	}
}

func TestBudgetCompactionNeverOrphansToolResponses(t *testing.T) {
	t.Parallel()
	messages := []*schema.Message{
		schema.SystemMessage("system"),
		schema.UserMessage("user"),
		schema.AssistantMessage("", []schema.ToolCall{namedToolCall("c", "exec_commands", strings.Repeat("x", 1000))}),
		schema.ToolMessage(strings.Repeat("y", 1000), "c"),
	}
	fitted, _, err := fitMessageBudget(messages, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !toolMessagesPaired(fitted) {
		t.Fatalf("orphaned tool response: %+v", fitted)
	}
	if len(fitted) < 2 || fitted[1].Content != "user" {
		t.Fatalf("user request was lost during compaction: %+v", fitted)
	}
}
