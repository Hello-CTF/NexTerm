package hitl

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type integrationModel struct {
	mu    sync.Mutex
	steps []*schema.Message
	index int
}

func (m *integrationModel) next() *schema.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.index >= len(m.steps) {
		return schema.AssistantMessage("done", nil)
	}
	message := m.steps[m.index]
	m.index++
	return message
}

func (m *integrationModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return m.next(), nil
}

func (m *integrationModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{m.next()}), nil
}

type integrationArgs struct {
	Value string `json:"value" jsonschema:"required"`
}

func TestEinoCheckpointInterruptResumeIntegration(t *testing.T) {
	var executions atomic.Int64
	confirmationTool, err := utils.InferTool("confirm_change", "Confirm a change", func(ctx context.Context, _ integrationArgs) (string, error) {
		if interrupted, _, state := tool.GetInterruptState[string](ctx); interrupted {
			targeted, hasData, decision := tool.GetResumeContext[string](ctx)
			if !targeted || !hasData {
				return "", tool.StatefulInterrupt(ctx, "confirm", state)
			}
			executions.Add(1)
			return decision, nil
		}
		return "", tool.StatefulInterrupt(ctx, "confirm", "pending")
	})
	if err != nil {
		t.Fatal(err)
	}
	chatModel := &integrationModel{steps: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{{ID: "call-one", Type: "function", Function: schema.FunctionCall{Name: "confirm_change", Arguments: `{"value":"deploy"}`}}}),
		schema.AssistantMessage("finished", nil),
	}}
	chatAgent, err := adk.NewChatModelAgent(context.Background(), &adk.ChatModelAgentConfig{
		Name: "hitl-test", Description: "HITL integration test", Instruction: "Use the tool", Model: chatModel, MaxIterations: 3,
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{confirmationTool}, ExecuteSequentially: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	store := newFakeCheckpoints()
	runner := adk.NewRunner(context.Background(), adk.RunnerConfig{Agent: chatAgent, CheckPointStore: store})
	manager, err := NewManager(Config{Checkpoints: store, TTL: time.Hour, NewNonce: func() (string, error) { return "integration-nonce", nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if _, err := manager.RegisterRun(context.Background(), "run", "checkpoint"); err != nil {
		t.Fatal(err)
	}
	iterator := runner.Run(context.Background(), []adk.Message{schema.UserMessage("deploy")}, adk.WithCheckPointID("checkpoint"))
	var interruptContext *adk.InterruptCtx
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			t.Fatal(event.Err)
		}
		if event.Action != nil && event.Action.Interrupted != nil {
			for _, candidate := range event.Action.Interrupted.InterruptContexts {
				if candidate.IsRootCause {
					interruptContext = candidate
				}
			}
		}
	}
	if interruptContext == nil {
		t.Fatal("Eino did not emit a root interrupt")
	}
	request, err := manager.Interrupt(context.Background(), interruptContext, InterruptInput{
		RunID: "run", CheckpointID: "checkpoint", CallID: "call-one", Tool: "confirm_change", Kind: KindConfirm,
		Parameters: []byte(`{"value":"deploy"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	resume, resumed, err := manager.Resume(context.Background(), runner, answerFor("answer", request))
	if err != nil {
		t.Fatal(err)
	}
	if resume.TargetID != interruptContext.ID {
		t.Fatalf("resume target = %q, want %q", resume.TargetID, interruptContext.ID)
	}
	var completedText string
	for {
		event, ok := resumed.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			t.Fatal(event.Err)
		}
		if event.Output != nil && event.Output.MessageOutput != nil && event.Output.MessageOutput.Message != nil {
			completedText = event.Output.MessageOutput.Message.Content
		}
	}
	if completedText != "finished" {
		t.Fatalf("final model output = %q", completedText)
	}
	if executions.Load() != 1 {
		t.Fatalf("tool executions = %d", executions.Load())
	}
	if _, _, err := manager.Resume(context.Background(), runner, answerFor("answer", request)); !errors.Is(err, ErrRequestConsumed) {
		t.Fatalf("duplicate resume error = %v", err)
	}
	if _, err := manager.Finish("run", TerminalCompleted, nil); err != nil {
		t.Fatal(err)
	}
	if store.deleteCount("checkpoint") != 1 || executions.Load() != 1 {
		t.Fatalf("checkpoint deletes=%d executions=%d", store.deleteCount("checkpoint"), executions.Load())
	}
}
