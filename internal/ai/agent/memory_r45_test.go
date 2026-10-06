package agent

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/memory"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

var runnerMemoryScope = memory.Scope{Tenant: "tenant-a", Subject: "subject-a"}

type captureModel struct {
	*fakeModel
	mu       sync.Mutex
	captured [][]*schema.Message
}

func (c *captureModel) Stream(ctx context.Context, messages []*schema.Message, options ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	c.mu.Lock()
	captured := make([]*schema.Message, len(messages))
	copy(captured, messages)
	c.captured = append(c.captured, captured)
	c.mu.Unlock()
	return c.fakeModel.Stream(ctx, messages, options...)
}

func (c *captureModel) inputs() [][]*schema.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]*schema.Message(nil), c.captured...)
}

func openRunnerMemory(t *testing.T) *memory.Store {
	t.Helper()
	store, _ := openRunnerMemoryAt(t)
	return store
}

func openRunnerMemoryAt(t *testing.T) (*memory.Store, string) {
	t.Helper()
	path := t.TempDir() + "/memory.db"
	store, err := memory.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, path
}

func memoryRunner(t *testing.T, chat model.BaseChatModel, memoryStore *memory.Store) (*Runner, *store.Store) {
	t.Helper()
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(Config{
		Model:       func(context.Context) (model.BaseChatModel, uint64, error) { return chat, 32768, nil },
		Tools:       tools.NewRegistry(tools.Dependencies{}),
		Store:       storage,
		Memory:      memoryStore,
		MemoryScope: runnerMemoryScope,
	})
	t.Cleanup(func() {
		_ = runner.Close()
		_ = storage.Close()
	})
	return runner, storage
}

func persistedContents(t *testing.T, storage *store.Store, conversationID string) []string {
	t.Helper()
	rows, err := storage.MsgList(context.Background(), conversationID)
	if err != nil {
		t.Fatal(err)
	}
	contents := make([]string, 0, len(rows))
	for _, row := range rows {
		contents = append(contents, row.ContentJSON)
	}
	return contents
}

func TestMemoryInjectionStaysOffByDefaultAndNeverPollutesHistory(t *testing.T) {
	ctx := context.Background()
	memoryStore := openRunnerMemory(t)
	if _, err := memoryStore.Create(ctx, runnerMemoryScope, memory.CreateInput{Topic: "operations", Content: "restart at 02:00"}); err != nil {
		t.Fatal(err)
	}
	chat := &captureModel{fakeModel: sequenceModel(schema.AssistantMessage("ok", nil))}
	runner, storage := memoryRunner(t, chat, memoryStore)

	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "first question")
	waitClosed(t, stream)

	inputs := chat.inputs()
	if len(inputs) == 0 {
		t.Fatal("model saw no input")
	}
	for _, message := range inputs[0] {
		if strings.Contains(message.Content, "restart at 02:00") {
			t.Fatalf("default-off injection leaked memory into the model input: %+v", message)
		}
	}
	contents := persistedContents(t, storage, response.ConversationID)
	if len(contents) != 2 || strings.Contains(strings.Join(contents, ""), "restart at 02:00") {
		t.Fatalf("persisted rows = %v", contents)
	}

	if _, err := memoryStore.SetInjectionEnabled(ctx, runnerMemoryScope, true, 0); err != nil {
		t.Fatal(err)
	}
	chat.mu.Lock()
	chat.captured = nil
	chat.mu.Unlock()
	stream = &SliceStream{}
	response = startTestJob(t, runner, stream, "second question")
	waitClosed(t, stream)

	inputs = chat.inputs()
	if len(inputs) == 0 {
		t.Fatal("model saw no input after enabling")
	}
	var ephemeral *schema.Message
	position := -1
	for index, message := range inputs[0] {
		if strings.Contains(message.Content, "restart at 02:00") {
			ephemeral = message
			position = index
		}
	}
	if ephemeral == nil {
		t.Fatalf("enabled injection produced no memory message: %+v", inputs[0])
	}
	if ephemeral.Role != schema.System || !strings.Contains(ephemeral.Content, "restart at 02:00") {
		t.Fatalf("ephemeral message = %+v", ephemeral)
	}

	for index := 0; index < position; index++ {
		if inputs[0][index].Role != schema.System {
			t.Fatalf("memory message at %d follows non-system message: %+v", position, inputs[0])
		}
	}
	if position+1 >= len(inputs[0]) || inputs[0][position+1].Role != schema.User || inputs[0][position+1].Content != "second question" {
		t.Fatalf("memory message must precede the conversation: %+v", inputs[0])
	}
	joined := strings.Join(persistedContents(t, storage, response.ConversationID), "")
	if strings.Contains(joined, "restart at 02:00") || strings.Contains(joined, "Long-term operational memory") {
		t.Fatalf("memory leaked into the persisted conversation: %s", joined)
	}
}

func TestMemoryToolsOptInAndRoundTrip(t *testing.T) {
	ctx := context.Background()
	memoryStore := openRunnerMemory(t)

	off := &captureModel{fakeModel: sequenceModel(schema.AssistantMessage("ok", nil))}
	runner, _ := memoryRunner(t, off, memoryStore)
	stream := &SliceStream{}
	startTestJob(t, runner, stream, "hello")
	waitClosed(t, stream)
	if entries, err := memoryStore.Index(ctx, runnerMemoryScope); err != nil || len(entries) != 0 {
		t.Fatalf("default-off tools wrote memory: %v err=%v", entries, err)
	}

	enabled := true
	if _, err := memoryStore.UpdateSettings(ctx, runnerMemoryScope, memory.SettingsInput{ToolsEnabled: &enabled}, 0); err != nil {
		t.Fatal(err)
	}
	chat := &captureModel{fakeModel: sequenceModel(
		toolCallMessage(namedToolCall("call-save-1", memorySaveTool, `{"topic":"operations","content":"restart at 02:00"}`)),
		schema.AssistantMessage("saved", nil),
	)}
	runner, _ = memoryRunner(t, chat, memoryStore)
	stream = &SliceStream{}
	startTestJob(t, runner, stream, "remember this")
	events := waitClosed(t, stream)
	sawSave := false
	for _, event := range events {
		if event.Type == "toolResult" && event.ID == "call-save-1" {
			sawSave = true
			if !event.OK {
				t.Fatalf("memory_save failed: %+v", event)
			}
		}
	}
	if !sawSave {
		t.Fatalf("no memory_save tool result in %+v", events)
	}
	index, err := memoryStore.Index(ctx, runnerMemoryScope)
	if err != nil {
		t.Fatal(err)
	}
	if len(index) != 1 || index[0].Topic != "operations" || len(index[0].Entries) != 1 {
		t.Fatalf("memory index = %+v", index)
	}
	entry, err := memoryStore.Get(ctx, runnerMemoryScope, index[0].Entries[0].ID)
	if err != nil || entry.Content != "restart at 02:00" {
		t.Fatalf("saved entry = %+v err=%v", entry, err)
	}

	secret := &captureModel{fakeModel: sequenceModel(
		toolCallMessage(namedToolCall("call-save-2", memorySaveTool, `{"topic":"operations","content":"password=hunter2"}`)),
		schema.AssistantMessage("done", nil),
	)}
	runner, _ = memoryRunner(t, secret, memoryStore)
	stream = &SliceStream{}
	startTestJob(t, runner, stream, "save a secret")
	events = waitClosed(t, stream)
	failed := false
	for _, event := range events {
		if event.Type == "toolResult" && event.ID == "call-save-2" {
			failed = !event.OK
		}
	}
	if !failed {
		t.Fatal("memory_save accepted a possible secret")
	}
	if index, _ := memoryStore.Index(ctx, runnerMemoryScope); len(index) != 1 || len(index[0].Entries) != 1 {
		t.Fatalf("rejected secret changed the store: %+v", index)
	}
}

func TestMemoryToolsAvailability(t *testing.T) {
	ctx := context.Background()
	runner := NewRunner(Config{Store: nil})
	if result, err := runner.memoryTools(ctx, false); err != nil || result != nil {
		t.Fatalf("nil store = %v err=%v", result, err)
	}
	memoryStore := openRunnerMemory(t)
	runner = NewRunner(Config{Memory: memoryStore, MemoryScope: runnerMemoryScope})
	if result, err := runner.memoryTools(ctx, false); err != nil || result != nil {
		t.Fatalf("default-off = %v err=%v", result, err)
	}
	if result, err := runner.memoryTools(ctx, true); err != nil || result != nil {
		t.Fatalf("plan mode = %v err=%v", result, err)
	}
	enabled := true
	if _, err := memoryStore.UpdateSettings(ctx, runnerMemoryScope, memory.SettingsInput{ToolsEnabled: &enabled}, 0); err != nil {
		t.Fatal(err)
	}
	result, err := runner.memoryTools(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(result))
	for _, candidate := range result {
		info, err := candidate.Info(ctx)
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, info.Name)
	}
	want := []string{memorySaveTool, memoryListTool, memoryRecallTool, memoryForgetTool}
	if len(names) != len(want) {
		t.Fatalf("tool names = %v", names)
	}
	for index := range want {
		if names[index] != want[index] {
			t.Fatalf("tool names = %v, want %v", names, want)
		}
	}
}

func TestMemoryToolOutputsRespectScopeAndCAS(t *testing.T) {
	ctx := context.Background()
	memoryStore := openRunnerMemory(t)
	entry, err := memoryStore.Create(ctx, runnerMemoryScope, memory.CreateInput{Topic: "operations", Content: "restart at 02:00"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := memoryStore.Create(ctx, memory.Scope{Tenant: "tenant-a", Subject: "subject-b"}, memory.CreateInput{Topic: "private", Content: "subject-b only"})
	if err != nil {
		t.Fatal(err)
	}

	if output := memoryRecallOutput(ctx, memoryStore, runnerMemoryScope, memoryRecallArgs{IDs: []string{entry.ID}}); !output.OK || !strings.Contains(output.Text, "restart at 02:00") {
		t.Fatalf("recall = %+v", output)
	}
	if output := memoryRecallOutput(ctx, memoryStore, runnerMemoryScope, memoryRecallArgs{IDs: []string{other.ID}}); output.OK {
		t.Fatalf("cross-scope recall = %+v", output)
	}
	if output := memoryListOutput(ctx, memoryStore, runnerMemoryScope, memoryListArgs{}); !output.OK || strings.Contains(output.Text, other.ID) {
		t.Fatalf("list = %+v", output)
	}
	if output := memoryForgetOutput(ctx, memoryStore, runnerMemoryScope, memoryForgetArgs{ID: entry.ID, ExpectedVersion: 99}); output.OK {
		t.Fatalf("stale forget = %+v", output)
	}
	if output := memoryForgetOutput(ctx, memoryStore, runnerMemoryScope, memoryForgetArgs{ID: entry.ID, ExpectedVersion: 1}); !output.OK {
		t.Fatalf("forget = %+v", output)
	}
	if _, err := memoryStore.Get(ctx, runnerMemoryScope, entry.ID); err == nil {
		t.Fatal("forget did not delete")
	}
}

func TestRunnerCloseClosesMemoryStore(t *testing.T) {
	memoryStore := openRunnerMemory(t)
	runner := NewRunner(Config{Memory: memoryStore, MemoryScope: runnerMemoryScope})
	if err := runner.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := memoryStore.Settings(context.Background(), runnerMemoryScope); err == nil {
		t.Fatal("runner.Close did not close the memory store")
	}
}

func TestMemoryRecallReRedactsPersistedSecrets(t *testing.T) {
	ctx := context.Background()
	memoryStore, path := openRunnerMemoryAt(t)
	entry, err := memoryStore.Create(ctx, runnerMemoryScope, memory.CreateInput{Topic: "operations", Content: "clean"})
	if err != nil {
		t.Fatal(err)
	}
	const secret = "persisted-raw-secret-42"
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.ExecContext(ctx, "UPDATE memory_entry SET content = ? WHERE id = ?", "api_key="+secret, entry.ID); err != nil {
		t.Fatal(err)
	}
	output := memoryRecallOutput(ctx, memoryStore, runnerMemoryScope, memoryRecallArgs{IDs: []string{entry.ID}})
	if !output.OK {
		t.Fatalf("recall = %+v", output)
	}
	if strings.Contains(output.Text, secret) {
		t.Fatalf("recall leaked a persisted secret: %q", output.Text)
	}
	if !strings.Contains(output.Text, "[REDACTED]") || !strings.Contains(output.Text, "[已脱敏]") || !strings.Contains(output.Text, "1 条内容含已脱敏") {
		t.Fatalf("recall redaction metadata = %q", output.Text)
	}
	listed := memoryListOutput(ctx, memoryStore, runnerMemoryScope, memoryListArgs{})
	if !listed.OK || !strings.Contains(listed.Text, entry.ID) || strings.Contains(listed.Text, secret) {
		t.Fatalf("list = %+v", listed)
	}
}

func TestMemoryListBudgetKeepsDeterministicWholeLines(t *testing.T) {
	ctx := context.Background()

	t.Run("entry cap", func(t *testing.T) {
		memoryStore := openRunnerMemory(t)
		total := memoryListMaxEntries + 6
		for i := 0; i < total; i++ {
			if _, err := memoryStore.Create(ctx, runnerMemoryScope, memory.CreateInput{
				Topic: "budget", Content: fmt.Sprintf("entry-%03d", i),
			}); err != nil {
				t.Fatal(err)
			}
		}
		output := memoryListOutput(ctx, memoryStore, runnerMemoryScope, memoryListArgs{})
		if !output.OK {
			t.Fatalf("list = %+v", output)
		}
		lines := completeLines(t, output.Text)
		if len(lines) != memoryListMaxEntries {
			t.Fatalf("listed lines = %d, want %d", len(lines), memoryListMaxEntries)
		}
		if !strings.Contains(output.Text, "[已省略 6 条]") {
			t.Fatalf("missing omitted marker: %q", output.Text)
		}

		index, err := memoryStore.Index(ctx, runnerMemoryScope)
		if err != nil {
			t.Fatal(err)
		}
		kept := make(map[string]bool, len(lines))
		for _, line := range lines {
			kept[strings.Split(line, "\t")[1]] = true
		}
		for position, entry := range index[0].Entries {
			if position < memoryListMaxEntries && !kept[entry.ID] {
				t.Fatalf("entry %d (%s) dropped before the cap", position, entry.ID)
			}
			if position >= memoryListMaxEntries && kept[entry.ID] {
				t.Fatalf("entry %d (%s) kept past the cap", position, entry.ID)
			}
		}
	})

	t.Run("byte cap with multi-byte topics", func(t *testing.T) {
		memoryStore := openRunnerMemory(t)
		longTopic := strings.Repeat("主题", 40)
		for i := 0; i < 40; i++ {
			if _, err := memoryStore.Create(ctx, runnerMemoryScope, memory.CreateInput{
				Topic: longTopic, Content: fmt.Sprintf("entry-%03d", i),
			}); err != nil {
				t.Fatal(err)
			}
		}
		output := memoryListOutput(ctx, memoryStore, runnerMemoryScope, memoryListArgs{})
		if !output.OK {
			t.Fatalf("list = %+v", output)
		}
		if len(output.Text) > memoryListMaxBytes+len("[已省略 999 条]\n") {
			t.Fatalf("list exceeds the byte budget: %d bytes", len(output.Text))
		}
		lines := completeLines(t, output.Text)
		if len(lines) == 0 || len(lines) >= 40 {
			t.Fatalf("byte-capped listing kept %d of 40 entries", len(lines))
		}
		for _, line := range lines {
			if !strings.HasPrefix(line, longTopic) {
				t.Fatalf("line is not a whole entry: %q", line)
			}
		}
	})

	t.Run("small store lists everything without markers", func(t *testing.T) {
		memoryStore := openRunnerMemory(t)
		first, err := memoryStore.Create(ctx, runnerMemoryScope, memory.CreateInput{Topic: "a", Content: "one"})
		if err != nil {
			t.Fatal(err)
		}
		second, err := memoryStore.Create(ctx, runnerMemoryScope, memory.CreateInput{Topic: "b", Content: "two"})
		if err != nil {
			t.Fatal(err)
		}
		output := memoryListOutput(ctx, memoryStore, runnerMemoryScope, memoryListArgs{})
		if !output.OK || strings.Contains(output.Text, "已省略") {
			t.Fatalf("small list = %+v", output)
		}
		for _, id := range []string{first.ID, second.ID} {
			if !strings.Contains(output.Text, id) {
				t.Fatalf("small list missing %s: %q", id, output.Text)
			}
		}
		filtered := memoryListOutput(ctx, memoryStore, runnerMemoryScope, memoryListArgs{Topic: "a"})
		if !filtered.OK || strings.Contains(filtered.Text, second.ID) || strings.Contains(filtered.Text, "已省略") {
			t.Fatalf("filtered list = %+v", filtered)
		}
		missing := memoryListOutput(ctx, memoryStore, runnerMemoryScope, memoryListArgs{Topic: "zzz"})
		if !missing.OK || !strings.Contains(missing.Text, "该主题下没有记忆") {
			t.Fatalf("empty filter list = %+v", missing)
		}
	})
}

func completeLines(t *testing.T, text string) []string {
	t.Helper()
	var lines []string
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		if strings.HasPrefix(line, "[已省略 ") {
			continue
		}
		if !utf8.ValidString(line) || len(strings.Split(line, "\t")) != 3 {
			t.Fatalf("partial or malformed line %q", line)
		}
		lines = append(lines, line)
	}
	return lines
}
