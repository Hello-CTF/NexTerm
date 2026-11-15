package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/provider"
	"github.com/Hello-CTF/NexTerm/internal/ai/subagent"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

const subagentConfirmationMarker = "子代理内无法请求确认"

type scriptedModel struct {
	mu     sync.Mutex
	inputs [][]*schema.Message
	step   func([]*schema.Message) (*schema.Message, error)
}

func (m *scriptedModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.mu.Lock()
	m.inputs = append(m.inputs, append([]*schema.Message(nil), input...))
	m.mu.Unlock()
	return m.step(input)
}

func (m *scriptedModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func (m *scriptedModel) callsFor(task string) [][]*schema.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result [][]*schema.Message
	for _, input := range m.inputs {
		if firstUserText(input) == task {
			result = append(result, input)
		}
	}
	return result
}

func firstUserText(input []*schema.Message) string {
	for _, message := range input {
		if message.Role == schema.User {
			return message.Content
		}
	}
	return ""
}

func toolResults(input []*schema.Message) []Output {
	var results []Output
	for _, message := range input {
		if message.Role != schema.Tool {
			continue
		}
		var output Output
		if err := json.Unmarshal([]byte(message.Content), &output); err == nil {
			results = append(results, output)
		}
	}
	return results
}

func scriptedToolCall(id, name, args string) *schema.Message {
	return schema.AssistantMessage("", []schema.ToolCall{{ID: id, Type: "function", Function: schema.FunctionCall{Name: name, Arguments: args}}})
}

func TestSubagentConfirmationReturnsFailedToolResultAndRunContinues(t *testing.T) {
	transport := &fakeTransport{result: base.ExecResult{Stdout: "ok"}}
	chat := &scriptedModel{}
	chat.step = func(input []*schema.Message) (*schema.Message, error) {
		results := toolResults(input)
		switch firstUserText(input) {
		case "parent task":
			if len(results) == 0 {
				return scriptedToolCall("sp-1", subagent.SpawnToolName, `{"task":"child task"}`), nil
			}
			return schema.AssistantMessage("parent will ask the user", nil), nil
		case "child task":
			if len(results) == 0 {
				return scriptedToolCall("ex-1", "exec_commands", `{"commands":["systemctl restart nginx"]}`), nil
			}
			if results[0].OK || !strings.Contains(results[0].Text, subagentConfirmationMarker) {
				return nil, errors.New("child did not get the confirmation failure: " + results[0].Text)
			}
			return schema.AssistantMessage("needs the parent to run: systemctl restart nginx", nil), nil
		default:
			return nil, errors.New("unexpected caller")
		}
	}
	execution := outcomeExecution(nil, transport)
	execution.Subagents = &SubagentConfig{
		Model:        func(context.Context) (model.BaseChatModel, error) { return chat, nil },
		AllowedTools: []string{"exec_commands"},
	}
	einoTools, err := execution.Tools()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name: "parent", Description: "test", Instruction: "test", Model: chat, MaxIterations: 8,
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: einoTools, ExecuteSequentially: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	iterator := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent, EnableStreaming: true}).Run(ctx, []*schema.Message{schema.UserMessage("parent task")})
	answer := ""
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			t.Fatalf("parent run failed: %v", event.Err)
		}
		if event.Action != nil && event.Action.Interrupted != nil {
			t.Fatal("subagent confirmation interrupted the parent run")
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		message, err := event.Output.MessageOutput.GetMessage()
		if err == nil && message != nil && message.Role == schema.Assistant && len(message.ToolCalls) == 0 {
			answer = message.Content
		}
	}
	if answer != "parent will ask the user" {
		t.Fatalf("parent did not reach its next turn: %q", answer)
	}
	if got := transport.calls(); got != 0 {
		t.Fatalf("a call needing confirmation ran inside the subagent: %d executions", got)
	}
	if got := len(chat.callsFor("child task")); got != 2 {
		t.Fatalf("child model turns = %d, want 2 (it keeps working after the failed call)", got)
	}
	parentCalls := chat.callsFor("parent task")
	spawned := toolResults(parentCalls[len(parentCalls)-1])
	if len(spawned) != 1 || !spawned[0].OK || !strings.Contains(spawned[0].Text, "systemctl restart nginx") {
		t.Fatalf("parent did not receive the child's report: %+v", spawned)
	}
}

func TestSubagentExecutionKeepsPermissionDecision(t *testing.T) {
	ctx := context.Background()
	transport := &fakeTransport{result: base.ExecResult{Stdout: "ok"}}
	child := outcomeExecution(nil, transport)
	child.Subagent = true

	allowed, err := child.initial(ctx, execCall("allowed", "echo hi"))
	if err != nil || !allowed.OK || transport.calls() != 1 {
		t.Fatalf("call allowed without asking did not run: %+v err=%v calls=%d", allowed, err, transport.calls())
	}

	asked, err := child.initial(ctx, execCall("asked", "systemctl restart nginx"))
	if err != nil {
		t.Fatalf("confirmation inside a subagent returned an error: %v", err)
	}
	if asked.OK || !strings.Contains(asked.Text, subagentConfirmationMarker) || transport.calls() != 1 {
		t.Fatalf("call needing confirmation was not refused: %+v calls=%d", asked, transport.calls())
	}
	modelText := provider.ProjectModelOutput("exec_commands", asked.OK, asked.Text, false, false)
	if !strings.Contains(modelText, "confirmation is not available inside a subagent") || !strings.Contains(modelText, "parent agent") {
		t.Fatalf("model-facing text not translated: %q", modelText)
	}

	forbidden, err := child.initial(ctx, execCall("forbidden", "rm -rf /"))
	if err != nil || forbidden.OK || !strings.Contains(forbidden.Text, "权限策略已拒绝") || transport.calls() != 1 {
		t.Fatalf("forbidden call was not denied by policy: %+v err=%v calls=%d", forbidden, err, transport.calls())
	}

	approved := outcomeExecution(nil, transport)
	approved.Subagent = true
	approved.Memory.Remember(guard.ClassifyTool("exec_commands", execCall("x", "systemctl restart nginx").Args, approved.Permission))
	remembered, err := approved.initial(ctx, execCall("remembered", "systemctl restart nginx"))
	if err != nil || !remembered.OK || transport.calls() != 2 {
		t.Fatalf("session approval did not apply inside the subagent: %+v err=%v calls=%d", remembered, err, transport.calls())
	}

	parent := outcomeExecution(nil, transport)
	if _, err := parent.initial(ctx, execCall("parent-asked", "systemctl restart nginx")); err == nil {
		t.Fatal("the parent execution must still pause for confirmation")
	}
	if transport.calls() != 2 {
		t.Fatalf("parent confirmation ran the command: calls=%d", transport.calls())
	}
}

type erroringSpawnTool struct{ err error }

func (erroringSpawnTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: subagent.SpawnToolName}, nil
}

func (e erroringSpawnTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	return "", e.err
}

func TestSpawnOutputToolPropagatesOnlyParentCancellation(t *testing.T) {
	spawn := spawnOutputTool{InvokableTool: erroringSpawnTool{err: errors.New("invalid spawn arguments")}}
	output, err := spawn.InvokableRun(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("spawn error ended the parent run: %v", err)
	}
	var result Output
	if err := json.Unmarshal([]byte(output), &result); err != nil || result.OK || !strings.Contains(result.Text, "invalid spawn arguments") {
		t.Fatalf("spawn error not reported as a failed result: %s (%v)", output, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	canceled := spawnOutputTool{InvokableTool: erroringSpawnTool{err: context.Canceled}}
	if _, err := canceled.InvokableRun(ctx, `{}`); !errors.Is(err, context.Canceled) {
		t.Fatalf("parent cancellation error = %v, want context.Canceled", err)
	}
}
