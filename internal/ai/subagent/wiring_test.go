package subagent_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/subagent"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type wireModel struct {
	mu     sync.Mutex
	inputs [][]*schema.Message
	step   func(context.Context, []*schema.Message) (*schema.Message, error)
}

func (m *wireModel) Generate(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.mu.Lock()
	m.inputs = append(m.inputs, cloneWireMessages(input))
	m.mu.Unlock()
	return m.step(ctx, input)
}

func (m *wireModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func (m *wireModel) Inputs() [][]*schema.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([][]*schema.Message, len(m.inputs))
	for i := range m.inputs {
		result[i] = cloneWireMessages(m.inputs[i])
	}
	return result
}

func cloneWireMessages(messages []*schema.Message) []*schema.Message {
	result := make([]*schema.Message, 0, len(messages))
	for _, message := range messages {
		if message == nil {
			continue
		}
		copied := *message
		result = append(result, &copied)
	}
	return result
}

func lastUserText(input []*schema.Message) string {
	for i := len(input) - 1; i >= 0; i-- {
		if input[i].Role == schema.User {
			return input[i].Content
		}
	}
	return ""
}

func toolResultText(input []*schema.Message) string {
	var builder strings.Builder
	for _, message := range input {
		if message.Role == schema.Tool {
			builder.WriteString(message.Content)
			builder.WriteString("\n")
		}
	}
	return builder.String()
}

func wireToolCall(id, name, args string) *schema.Message {
	return schema.AssistantMessage("", []schema.ToolCall{{ID: id, Type: "function", Function: schema.FunctionCall{Name: name, Arguments: args}}})
}

type wireRun struct {
	answer      string
	err         error
	interrupted bool
}

func runWireAgent(ctx context.Context, t *testing.T, execution *tools.Execution, chat model.BaseChatModel, task string) wireRun {
	t.Helper()
	einoTools, err := execution.Tools()
	if err != nil {
		t.Fatal(err)
	}
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name: "wire-parent", Description: "dispatcher test", Instruction: "test", Model: chat, MaxIterations: 8,
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: einoTools, ExecuteSequentially: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent, EnableStreaming: true})
	iterator := runner.Run(ctx, []*schema.Message{schema.UserMessage(task)})
	var result wireRun
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		if event == nil {
			continue
		}
		if event.Err != nil && result.err == nil {
			result.err = event.Err
		}
		if event.Action != nil && event.Action.Interrupted != nil {
			result.interrupted = true
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		message, err := event.Output.MessageOutput.GetMessage()
		if err != nil || message == nil {
			continue
		}
		if event.Output.MessageOutput.Role == schema.Assistant && len(message.ToolCalls) == 0 {
			result.answer = message.Content
		}
	}
	return result
}

func wireExecution(chat *wireModel, allowed []string, deps tools.Dependencies) *tools.Execution {
	return &tools.Execution{
		JobID:      "wire-job",
		Registry:   tools.NewRegistry(deps),
		Scope:      tools.Scope{},
		Permission: guard.Config{Mode: guard.Silent},
		Memory:     guard.NewMemory(),
		Subagents: &tools.SubagentConfig{
			Model:        func(context.Context) (model.BaseChatModel, error) { return chat, nil },
			AllowedTools: allowed,
		},
	}
}

func assetDeps(calls *atomic.Int64) tools.Dependencies {
	return tools.Dependencies{ListAssets: func(context.Context) ([]tools.Asset, error) {
		calls.Add(1)
		return []tools.Asset{{ID: "asset-1", Name: "server"}}, nil
	}}
}

func childInputs(chat *wireModel, task string) [][]*schema.Message {
	var result [][]*schema.Message
	for _, input := range chat.Inputs() {
		if lastUserText(input) == task {
			result = append(result, input)
		}
	}
	return result
}

func TestSpawnToolDispatchSpawnWaitAndIsolation(t *testing.T) {
	var assetCalls atomic.Int64
	chat := &wireModel{}
	chat.step = func(_ context.Context, input []*schema.Message) (*schema.Message, error) {
		switch lastUserText(input) {
		case "parent task":
			if toolResultText(input) == "" {
				return wireToolCall("sp-1", subagent.SpawnToolName, `{"task":"child task","persona":"respond tersely"}`), nil
			}
			if !strings.Contains(toolResultText(input), "child done") {
				return nil, errors.New("spawn result missing child output: " + toolResultText(input))
			}
			return schema.AssistantMessage("parent done: child done", nil), nil
		case "child task":
			if toolResultText(input) == "" {
				return wireToolCall("ls-1", "list_assets", `{}`), nil
			}
			if !strings.Contains(toolResultText(input), "server") {
				return nil, errors.New("list_assets result missing asset: " + toolResultText(input))
			}
			return schema.AssistantMessage("child done", nil), nil
		default:
			return nil, errors.New("unexpected caller: " + lastUserText(input))
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result := runWireAgent(ctx, t, wireExecution(chat, nil, assetDeps(&assetCalls)), chat, "parent task")
	if result.err != nil || result.interrupted {
		t.Fatalf("parent run failed: err=%v interrupted=%v", result.err, result.interrupted)
	}
	if result.answer != "parent done: child done" {
		t.Fatalf("unexpected parent answer %q", result.answer)
	}
	if assetCalls.Load() != 1 {
		t.Fatalf("list_assets calls = %d, want 1", assetCalls.Load())
	}
	inputs := childInputs(chat, "child task")
	if len(inputs) < 2 {
		t.Fatalf("child model calls = %d, want at least 2", len(inputs))
	}
	nonSystem := 0
	for _, message := range inputs[0] {
		if message.Role != schema.System {
			nonSystem++
			if message.Role != schema.User || message.Content != "child task" {
				t.Fatalf("child did not start isolated: %#v", message)
			}
		}
		if strings.Contains(message.Content, "parent task") {
			t.Fatal("parent conversation leaked into the child model input")
		}
	}
	if nonSystem != 1 {
		t.Fatalf("child first input has %d non-system messages, want 1", nonSystem)
	}
	personaSeen := false
	for _, message := range inputs[0] {
		if message.Role == schema.System && strings.Contains(message.Content, "respond tersely") {
			personaSeen = true
		}
	}
	if !personaSeen {
		t.Fatal("persona was not appended to the child instruction")
	}
}

func TestSpawnToolDispatchNestedSpawnDeniedByDefaultScope(t *testing.T) {
	var assetCalls atomic.Int64
	chat := &wireModel{}
	chat.step = func(_ context.Context, input []*schema.Message) (*schema.Message, error) {
		switch lastUserText(input) {
		case "parent task":
			if toolResultText(input) == "" {
				return wireToolCall("sp-1", subagent.SpawnToolName, `{"task":"child task"}`), nil
			}
			if !strings.Contains(toolResultText(input), "nested rejected") {
				return nil, errors.New("spawn result missing child output: " + toolResultText(input))
			}
			return schema.AssistantMessage("parent done", nil), nil
		case "child task":
			if toolResultText(input) == "" {
				return wireToolCall("sp-2", subagent.SpawnToolName, `{"task":"grandchild task"}`), nil
			}
			if !strings.Contains(toolResultText(input), "permission denied: "+subagent.SpawnToolName+" is outside the subagent scope") {
				return nil, errors.New("nested spawn was not denied: " + toolResultText(input))
			}
			return schema.AssistantMessage("nested rejected", nil), nil
		default:
			return nil, errors.New("unexpected caller: " + lastUserText(input))
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result := runWireAgent(ctx, t, wireExecution(chat, nil, assetDeps(&assetCalls)), chat, "parent task")
	if result.err != nil || result.interrupted {
		t.Fatalf("parent run failed: err=%v interrupted=%v", result.err, result.interrupted)
	}
	if result.answer != "parent done" {
		t.Fatalf("unexpected parent answer %q", result.answer)
	}
	if got := childInputs(chat, "grandchild task"); len(got) != 0 {
		t.Fatalf("nested task escaped rejection: %d grandchild runs", len(got))
	}
}

func TestSpawnToolDispatchNestedSpawnRejectedByManager(t *testing.T) {
	var assetCalls atomic.Int64
	chat := &wireModel{}
	chat.step = func(_ context.Context, input []*schema.Message) (*schema.Message, error) {
		switch lastUserText(input) {
		case "parent task":
			if toolResultText(input) == "" {
				return wireToolCall("sp-1", subagent.SpawnToolName, `{"task":"child task"}`), nil
			}
			if !strings.Contains(toolResultText(input), "nested rejected") {
				return nil, errors.New("spawn result missing child output: " + toolResultText(input))
			}
			return schema.AssistantMessage("parent done", nil), nil
		case "child task":
			if toolResultText(input) == "" {
				return wireToolCall("sp-2", subagent.SpawnToolName, `{"task":"grandchild task"}`), nil
			}
			if !strings.Contains(toolResultText(input), subagent.ErrNestedSpawn.Error()) {
				return nil, errors.New("nested spawn was not rejected by the manager: " + toolResultText(input))
			}
			return schema.AssistantMessage("nested rejected", nil), nil
		default:
			return nil, errors.New("unexpected caller: " + lastUserText(input))
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	execution := wireExecution(chat, []string{subagent.SpawnToolName}, assetDeps(&assetCalls))
	result := runWireAgent(ctx, t, execution, chat, "parent task")
	if result.err != nil || result.interrupted {
		t.Fatalf("parent run failed: err=%v interrupted=%v", result.err, result.interrupted)
	}
	if result.answer != "parent done" {
		t.Fatalf("unexpected parent answer %q", result.answer)
	}
	if got := childInputs(chat, "grandchild task"); len(got) != 0 {
		t.Fatalf("nested task escaped rejection: %d grandchild runs", len(got))
	}
	if assetCalls.Load() != 0 {
		t.Fatalf("unexpected scoped tool calls: %d", assetCalls.Load())
	}
}

func TestSpawnToolDispatchDeniedTools(t *testing.T) {
	var assetCalls atomic.Int64
	chat := &wireModel{}
	chat.step = func(_ context.Context, input []*schema.Message) (*schema.Message, error) {
		switch lastUserText(input) {
		case "parent task":
			if toolResultText(input) == "" {
				return wireToolCall("sp-1", subagent.SpawnToolName, `{"task":"child task"}`), nil
			}
			if !strings.Contains(toolResultText(input), "denied ok") {
				return nil, errors.New("spawn result missing child output: " + toolResultText(input))
			}
			return schema.AssistantMessage("parent done", nil), nil
		case "child task":
			if toolResultText(input) == "" {
				return wireToolCall("td-1", "todo_write", `{"todos":[{"content":"x","status":"pending"}]}`), nil
			}
			if !strings.Contains(toolResultText(input), "permission denied: todo_write is outside the subagent scope") {
				return nil, errors.New("out-of-scope tool was not denied: " + toolResultText(input))
			}
			return schema.AssistantMessage("denied ok", nil), nil
		default:
			return nil, errors.New("unexpected caller: " + lastUserText(input))
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	execution := wireExecution(chat, []string{"list_assets"}, assetDeps(&assetCalls))
	result := runWireAgent(ctx, t, execution, chat, "parent task")
	if result.err != nil || result.interrupted {
		t.Fatalf("parent run failed: err=%v interrupted=%v", result.err, result.interrupted)
	}
	if result.answer != "parent done" {
		t.Fatalf("unexpected parent answer %q", result.answer)
	}
}

func TestSpawnToolDispatchPermissionIntersection(t *testing.T) {
	var assetCalls atomic.Int64
	chat := &wireModel{}
	chat.step = func(_ context.Context, input []*schema.Message) (*schema.Message, error) {
		if lastUserText(input) == "parent task" && toolResultText(input) == "" {
			return wireToolCall("sp-1", subagent.SpawnToolName, `{"task":"child task"}`), nil
		}
		return wireToolCall("ex-1", "exec_commands", `{"commands":["id"]}`), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	deps := assetDeps(&assetCalls)
	deps.Transport = func(context.Context, string) (base.Transport, error) { return nil, errors.New("unreachable") }
	execution := wireExecution(chat, []string{"exec_commands", "list_assets"}, deps)
	result := runWireAgent(ctx, t, execution, chat, "parent task")
	if result.err == nil {
		t.Fatal("child reached exec_commands despite the missing session scope")
	}
	if !strings.Contains(result.err.Error(), "exec_commands") {
		t.Fatalf("error should name the intersected tool: %v", result.err)
	}
	if assetCalls.Load() != 0 {
		t.Fatalf("unexpected scoped tool calls: %d", assetCalls.Load())
	}
}

func TestSpawnToolDispatchCancellation(t *testing.T) {
	started := make(chan struct{})
	var startedOnce sync.Once
	var childCanceled atomic.Bool
	chat := &wireModel{}
	chat.step = func(ctx context.Context, input []*schema.Message) (*schema.Message, error) {
		switch lastUserText(input) {
		case "parent task":
			return wireToolCall("sp-1", subagent.SpawnToolName, `{"task":"child task"}`), nil
		case "child task":
			startedOnce.Do(func() { close(started) })
			<-ctx.Done()
			childCanceled.Store(true)
			return nil, ctx.Err()
		default:
			return nil, errors.New("unexpected caller: " + lastUserText(input))
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-started
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	result := runWireAgent(ctx, t, wireExecution(chat, nil, tools.Dependencies{}), chat, "parent task")
	if !childCanceled.Load() {
		t.Fatal("cancellation did not reach the child model")
	}
	if result.err == nil {
		t.Fatal("canceled spawn returned no error")
	}
	if !errors.Is(result.err, context.Canceled) && !strings.Contains(strings.ToLower(result.err.Error()), "cancel") {
		t.Fatalf("cancellation error = %v", result.err)
	}
}

func TestSpawnToolEmitsAttributedSubagentEvents(t *testing.T) {
	var assetCalls atomic.Int64
	chat := &wireModel{}
	chat.step = func(_ context.Context, input []*schema.Message) (*schema.Message, error) {
		switch lastUserText(input) {
		case "parent task":
			if toolResultText(input) == "" {
				return wireToolCall("sp-1", subagent.SpawnToolName, `{"task":"child task"}`), nil
			}
			return schema.AssistantMessage("parent done", nil), nil
		case "child task":
			if toolResultText(input) == "" {
				return wireToolCall("ls-1", "list_assets", `{}`), nil
			}
			return schema.AssistantMessage("child done", nil), nil
		default:
			return nil, errors.New("unexpected caller: " + lastUserText(input))
		}
	}
	var mu sync.Mutex
	var events []subagent.Event
	var attributions []string
	execution := wireExecution(chat, nil, assetDeps(&assetCalls))
	execution.SubagentEvents = func(_ context.Context, parentCallID string, depth int, event subagent.Event) {
		mu.Lock()
		events = append(events, event)
		attributions = append(attributions, fmt.Sprintf("%s/%d", parentCallID, depth))
		mu.Unlock()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result := runWireAgent(ctx, t, execution, chat, "parent task")
	if result.err != nil || result.interrupted {
		t.Fatalf("parent run failed: err=%v interrupted=%v", result.err, result.interrupted)
	}
	mu.Lock()
	recorded := append([]subagent.Event(nil), events...)
	seen := append([]string(nil), attributions...)
	mu.Unlock()
	if len(recorded) != 4 {
		t.Fatalf("subagent events = %d, want 4: %+v", len(recorded), recorded)
	}
	for _, attribution := range seen {
		if attribution != "sp-1/1" {
			t.Fatalf("event attribution = %q, want sp-1/1", attribution)
		}
	}
	wantKinds := []subagent.EventKind{subagent.EventToolCall, subagent.EventToolResult, subagent.EventDelta, subagent.EventDone}
	for index, kind := range wantKinds {
		if recorded[index].Kind != kind {
			t.Fatalf("event %d kind = %s, want %s (%+v)", index, recorded[index].Kind, kind, recorded)
		}
		if recorded[index].TaskID == "" {
			t.Fatalf("event %d has no task ID: %+v", index, recorded[index])
		}
	}
	if recorded[0].CallID != "ls-1" || recorded[0].Name != "list_assets" {
		t.Fatalf("toolCall event = %+v", recorded[0])
	}
	if recorded[1].CallID != "ls-1" || !recorded[1].OK || !strings.Contains(recorded[1].Summary, "server") {
		t.Fatalf("toolResult event = %+v", recorded[1])
	}
	if recorded[2].Text != "child done" {
		t.Fatalf("delta event = %+v", recorded[2])
	}
	if recorded[3].Status != subagent.StatusCompleted || recorded[3].Summary != "child done" {
		t.Fatalf("done event = %+v", recorded[3])
	}
}

func TestSpawnToolEmitsNoEventsWithoutSink(t *testing.T) {
	var assetCalls atomic.Int64
	chat := &wireModel{}
	chat.step = func(_ context.Context, input []*schema.Message) (*schema.Message, error) {
		switch lastUserText(input) {
		case "parent task":
			if toolResultText(input) == "" {
				return wireToolCall("sp-1", subagent.SpawnToolName, `{"task":"child task"}`), nil
			}
			return schema.AssistantMessage("parent done", nil), nil
		case "child task":
			return schema.AssistantMessage("child done", nil), nil
		default:
			return nil, errors.New("unexpected caller: " + lastUserText(input))
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result := runWireAgent(ctx, t, wireExecution(chat, nil, assetDeps(&assetCalls)), chat, "parent task")
	if result.err != nil || result.interrupted {
		t.Fatalf("parent run failed: err=%v interrupted=%v", result.err, result.interrupted)
	}
	if result.answer != "parent done" {
		t.Fatalf("unexpected parent answer %q", result.answer)
	}
}
