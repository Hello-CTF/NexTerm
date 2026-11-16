package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/cloudwego/eino/schema"
)

func TestSkillCatalogPromptNamesEverySkillAndTheSlashTrigger(t *testing.T) {
	prompt := skillCatalogPrompt([]tools.SkillSummary{
		{Name: "nginx-check", Description: "检查 nginx 配置并重载"},
		{Name: "disk-report", Description: "整理磁盘占用"},
	})
	for _, fragment := range []string{"[Available Skills]", "load_skill", "/<skill name>", "- nginx-check: 检查 nginx 配置并重载", "- disk-report: 整理磁盘占用"} {
		if !strings.Contains(prompt, fragment) {
			t.Fatalf("prompt missing %q:\n%s", fragment, prompt)
		}
	}
}

type catalogSkills struct {
	mu      sync.Mutex
	catalog []tools.SkillSummary
}

func (s *catalogSkills) LoadSkillSchema() tools.Schema {
	return tools.Schema{Name: "load_skill", Description: "load a skill", Parameters: map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string", "description": "skill name"}}}}
}

func (s *catalogSkills) LoadSkill(context.Context, json.RawMessage) tools.Output {
	return tools.OK("skill body")
}

func (s *catalogSkills) SkillCatalog(context.Context) ([]tools.SkillSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]tools.SkillSummary(nil), s.catalog...), nil
}

func (s *catalogSkills) set(catalog ...tools.SkillSummary) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.catalog = catalog
}

func catalogsIn(input []*schema.Message) []string {
	var catalogs []string
	for _, message := range input {
		if message.Role == schema.User && strings.HasPrefix(message.Content, skillCatalogHeader) {
			catalogs = append(catalogs, message.Content)
		}
	}
	return catalogs
}

func TestSkillCatalogIsSentAgainOnlyWhenTheSkillsChange(t *testing.T) {
	chat := &recordingChat{step: func(int, []*schema.Message) *schema.Message { return schema.AssistantMessage("done", nil) }}
	skills := &catalogSkills{}
	skills.set(tools.SkillSummary{Name: "nginx-check", Description: "检查 nginx"})
	runner, storage := testRunner(t, chat, tools.Dependencies{Skills: skills}, 0)
	conversation, err := storage.ConvCreate(context.Background(), "skills", map[string]any{"scope": tools.Scope{SessionID: "session"}})
	if err != nil {
		t.Fatal(err)
	}
	run := func(message string) []string {
		t.Helper()
		stream := &SliceStream{}
		if _, err := runner.Start(context.Background(), ChatArgs{ConversationID: conversation.ID, Message: message, Scope: tools.Scope{SessionID: "session"}}, StaticStream(stream)); err != nil {
			t.Fatal(err)
		}
		if done, failed := terminalCounts(waitClosed(t, stream)); done != 1 || failed != 0 {
			t.Fatalf("terminal counts done=%d error=%d", done, failed)
		}
		chat.mu.Lock()
		defer chat.mu.Unlock()
		input := chat.inputs[len(chat.inputs)-1]
		if last := input[len(input)-1]; last.Role != schema.User || last.Content != message {
			t.Fatalf("the run's own message must come last, got %+v", last)
		}
		return catalogsIn(input)
	}

	first := run("one")
	if len(first) != 1 || !strings.Contains(first[0], "- nginx-check: 检查 nginx") {
		t.Fatalf("first run must send the catalog once, got %q", first)
	}
	if again := run("two"); len(again) != 1 || again[0] != first[0] {
		t.Fatalf("an unchanged catalog must only appear as the saved copy, got %q", again)
	}
	skills.set(tools.SkillSummary{Name: "nginx-check", Description: "检查 nginx"}, tools.SkillSummary{Name: "disk-report", Description: "整理磁盘"})
	changed := run("three")
	if len(changed) != 2 || changed[0] != first[0] || !strings.Contains(changed[1], "- disk-report: 整理磁盘") {
		t.Fatalf("a changed catalog must be sent after the saved one, got %q", changed)
	}
	if after := run("four"); len(after) != 2 || after[1] != changed[1] {
		t.Fatalf("the changed catalog must not be sent again, got %q", after)
	}

	messages, err := runner.Messages(context.Background(), conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	saved := 0
	for _, message := range messages {
		if message.Role == historyTypeSkillCatalog {
			saved++
		}
	}
	if saved != 2 {
		t.Fatalf("saved catalogs = %d, want 2", saved)
	}
}

func TestSkillCatalogIsGoneAfterASummary(t *testing.T) {
	encode := func(row map[string]any) store.MessageRow {
		encoded, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		return store.MessageRow{ConversationID: "conversation", ContentJSON: string(encoded)}
	}
	catalog := skillCatalogPrompt([]tools.SkillSummary{{Name: "nginx-check", Description: "检查 nginx"}})
	rows := []store.MessageRow{
		encode(map[string]any{"role": "user", "content": "检查 nginx", "jobId": "job-1"}),
		encode(map[string]any{"type": historyTypeSkillCatalog, "content": catalog, "jobId": "job-1"}),
		encode(map[string]any{"role": "assistant", "content": "nginx 配置正常", "jobId": "job-1"}),
	}
	if got := latestSkillCatalog(historyMessages(rows, "job-2")); got != catalog {
		t.Fatalf("a saved catalog must be rebuilt into the history, got %q", got)
	}
	rows = append(rows, encode(map[string]any{"type": historyTypeSummary, "summary": "nginx checked"}))
	if got := latestSkillCatalog(historyMessages(rows, "job-2")); got != "" {
		t.Fatalf("a summary removes the catalog, so the next run sends it again, got %q", got)
	}
}
