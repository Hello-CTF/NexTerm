package aicontext

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Hello-CTF/NexTerm/internal/ai/provider"
	"github.com/cloudwego/eino/schema"
)

func TestRenderIncludesScreenAndStablePrompt(t *testing.T) {
	t.Parallel()
	bundle := Bundle{Session: &SessionBrief{Name: "web", Kind: "ssh"}, Screen: "visible", Tail: []string{"tail"}, Recon: "recon", Selection: "selection"}
	rendered := Render(bundle)
	for _, expected := range []string{"[Current Terminal Screen]", "visible", "[Recent Terminal Output]", "[Environment Recon Snapshot]", "[User-Selected Text]"} {
		if !strings.Contains(rendered, expected) {
			t.Errorf("missing %q in %q", expected, rendered)
		}
	}
	first, second := SystemPrompt(), SystemPrompt()
	if first != second || first == "" {
		t.Fatal("stable prompt changed or empty")
	}
}

func TestBudgetTrimmingDropsLowPriorityAndStaysValid(t *testing.T) {
	t.Parallel()
	bundle := Bundle{Screen: strings.Repeat("屏", 20000), Tail: []string{strings.Repeat("t", 30000)}, Recon: strings.Repeat("r", 30000), Selection: strings.Repeat("s", 30000)}
	TrimToBudget(&bundle)
	rendered := Render(bundle)
	if len(rendered) > BudgetBytes {
		t.Fatalf("rendered size = %d, budget = %d", len(rendered), BudgetBytes)
	}
	if !utf8.ValidString(rendered) {
		t.Fatal("invalid UTF-8 after trimming")
	}
	if bundle.Selection != "" {
		t.Fatal("selection should be dropped first")
	}
	if bundle.Screen == "" {
		t.Fatal("screen should have highest priority")
	}
}

func TestDisconnectedTriggerMatchesActualToolResult(t *testing.T) {
	prompt := SystemPrompt()
	if !strings.Contains(prompt, `if a result says "disconnected"`) {
		t.Fatalf("system prompt lacks the disconnected trigger: %s", prompt)
	}
	if !strings.Contains(prompt, "Answer in Chinese.") {
		t.Fatalf("system prompt no longer requires Chinese answers: %s", prompt)
	}
	message := schema.ToolMessage(`{"ok":false,"text":"连接已断开：数据库服务已关闭，请重新连接","exitCode":1,"truncated":false}`, "call-1")
	message.ToolName = "db_query"
	projected := provider.ProjectModelMessages([]*schema.Message{message})
	var envelope map[string]any
	if err := json.Unmarshal([]byte(projected[0].Content), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["text"] != "disconnected: database service is closed; reconnect" {
		t.Fatalf("actual disconnect projection = %v", envelope["text"])
	}
	if message.Content == projected[0].Content {
		t.Fatal("disconnect projection did not clone and change the model message")
	}
	if !strings.Contains(message.Content, "连接已断开：数据库服务已关闭，请重新连接") {
		t.Fatalf("source disconnect text changed: %s", message.Content)
	}
}
