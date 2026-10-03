package steer

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

type modelStep func(context.Context, []*schema.Message, int) (*schema.Message, error)

type scriptedModel struct {
	mu    sync.Mutex
	calls [][]*schema.Message
	step  modelStep
}

func (m *scriptedModel) Generate(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.mu.Lock()
	m.calls = append(m.calls, CloneHistory(input))
	call := len(m.calls)
	m.mu.Unlock()
	return m.step(ctx, input, call)
}

func (m *scriptedModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func (m *scriptedModel) Inputs() [][]*schema.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([][]*schema.Message, len(m.calls))
	for i := range m.calls {
		result[i] = CloneHistory(m.calls[i])
	}
	return result
}

type emptyArgs struct{}

func TestRunnerSteersAfterToolResultsAndRoutesLeftover(t *testing.T) {
	toolStarted := make(chan struct{})
	releaseTool := make(chan struct{})
	secondModel := make(chan struct{})
	thirdDone := make(chan struct{})
	var toolOnce sync.Once
	var secondOnce sync.Once
	var thirdOnce sync.Once

	probe, err := utils.InferTool("probe", "test probe", func(ctx context.Context, _ emptyArgs) (string, error) {
		toolOnce.Do(func() { close(toolStarted) })
		select {
		case <-releaseTool:
			return "probe complete", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	chat := &scriptedModel{}
	chat.step = func(_ context.Context, _ []*schema.Message, call int) (*schema.Message, error) {
		switch call {
		case 1:
			return schema.AssistantMessage("", []schema.ToolCall{{ID: "call-1", Type: "function", Function: schema.FunctionCall{Name: "probe", Arguments: `{}`}}}), nil
		case 2:
			secondOnce.Do(func() { close(secondModel) })
			return schema.AssistantMessage("second done", nil), nil
		case 3:
			return schema.AssistantMessage("third done", nil), nil
		default:
			return schema.AssistantMessage("later", nil), nil
		}
	}
	runner, err := NewRunner(Config{
		NewAgent: func(ctx context.Context) (adk.Agent, error) {
			return adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
				Name: "steer-test", Description: "test", Model: chat, MaxIterations: 4,
				ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{probe}, ExecuteSequentially: true}},
			})
		},
		OnEvent: func(_ context.Context, event *adk.AgentEvent) error {
			if event.Output != nil && event.Output.MessageOutput != nil && event.Output.MessageOutput.Message != nil && event.Output.MessageOutput.Message.Content == "third done" {
				thirdOnce.Do(func() { close(thirdDone) })
			}
			return nil
		},
		MaxPending: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Start(context.Background(), StartRequest{Message: schema.UserMessage("initial")}); err != nil {
		t.Fatal(err)
	}
	waitChannel(t, toolStarted, "tool did not start")
	if err := runner.Steer(schema.UserMessage("second")); err != nil {
		t.Fatal(err)
	}
	if err := runner.Steer(schema.UserMessage("third")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-secondModel:
		t.Fatal("next model call started before the active tool round completed")
	case <-time.After(40 * time.Millisecond):
	}
	close(releaseTool)
	waitChannel(t, thirdDone, "leftover steering input did not start a new run")
	runner.Cancel()
	_, err = runner.Wait(context.Background())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait error = %v, want context.Canceled", err)
	}

	inputs := chat.Inputs()
	if len(inputs) != 3 {
		t.Fatalf("model calls = %d, want 3", len(inputs))
	}
	for i, input := range inputs {
		if err := ValidateHistory(input); err != nil {
			t.Fatalf("model input %d has broken tool pairing: %v", i+1, err)
		}
	}
	if len(inputs[1]) != 4 || inputs[1][0].Content != "initial" || inputs[1][1].Role != schema.Assistant || inputs[1][2].Role != schema.Tool || inputs[1][3].Content != "second" {
		t.Fatalf("unexpected second-run history: %#v", inputs[1])
	}
	if len(inputs[2]) != 6 || inputs[2][3].Content != "second" || inputs[2][4].Content != "second done" || inputs[2][5].Content != "third" {
		t.Fatalf("leftover input was not routed to a fresh run: %#v", inputs[2])
	}
	if err := ValidateHistory(runner.History()); err != nil {
		t.Fatalf("final stable history is invalid: %v", err)
	}
}

func TestRunnerCancelClearsQueuedSteering(t *testing.T) {
	toolStarted := make(chan struct{})
	probe, err := utils.InferTool("probe", "test probe", func(ctx context.Context, _ emptyArgs) (string, error) {
		close(toolStarted)
		<-ctx.Done()
		return "", ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	chat := &scriptedModel{step: func(_ context.Context, _ []*schema.Message, _ int) (*schema.Message, error) {
		return schema.AssistantMessage("", []schema.ToolCall{{ID: "call-1", Function: schema.FunctionCall{Name: "probe", Arguments: `{}`}}}), nil
	}}
	runner, err := NewRunner(Config{NewAgent: func(ctx context.Context) (adk.Agent, error) {
		return adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
			Name: "cancel-test", Description: "test", Model: chat,
			ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{probe}}},
		})
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Start(context.Background(), StartRequest{Message: schema.UserMessage("initial")}); err != nil {
		t.Fatal(err)
	}
	waitChannel(t, toolStarted, "tool did not start")
	if err := runner.Steer(schema.UserMessage("stale")); err != nil {
		t.Fatal(err)
	}
	runner.Cancel()
	result, err := runner.Wait(context.Background())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait error = %v, want context.Canceled", err)
	}
	if runner.Pending() != 0 || len(result.Leftover) != 0 {
		t.Fatalf("cancel retained steering: pending=%d leftover=%d", runner.Pending(), len(result.Leftover))
	}
	if err := runner.Steer(schema.UserMessage("late")); !errors.Is(err, ErrCanceled) {
		t.Fatalf("late Steer error = %v, want ErrCanceled", err)
	}
	if err := ValidateHistory(result.History); err != nil {
		t.Fatalf("cancel returned invalid history: %v", err)
	}
	if calls := len(chat.Inputs()); calls != 1 {
		t.Fatalf("model calls after cancel = %d, want 1", calls)
	}
}

func TestRunnerConcurrentSteerAndCancel(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	chat := &scriptedModel{step: func(ctx context.Context, _ []*schema.Message, _ int) (*schema.Message, error) {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	runner, err := NewRunner(Config{NewAgent: func(ctx context.Context) (adk.Agent, error) {
		return adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{Name: "race-test", Description: "test", Model: chat})
	}, MaxPending: 32})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Start(context.Background(), StartRequest{Message: schema.UserMessage("initial")}); err != nil {
		t.Fatal(err)
	}
	waitChannel(t, started, "model did not start")
	var wg sync.WaitGroup
	var accepted atomic.Int64
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := runner.Steer(schema.UserMessage("race"))
			if err == nil {
				accepted.Add(1)
				return
			}
			if !errors.Is(err, ErrCanceled) && !errors.Is(err, ErrClosed) && !errors.Is(err, ErrQueueFull) {
				t.Errorf("unexpected Steer error: %v", err)
			}
		}()
	}
	go runner.Cancel()
	wg.Wait()
	runner.Cancel()
	result, err := runner.Wait(context.Background())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait error = %v", err)
	}
	if runner.Pending() != 0 || len(result.Leftover) != 0 {
		t.Fatalf("race left stale input: accepted=%d pending=%d leftover=%d", accepted.Load(), runner.Pending(), len(result.Leftover))
	}
}

func TestValidateHistoryToolPairs(t *testing.T) {
	assistant := schema.AssistantMessage("", []schema.ToolCall{
		{ID: "a", Function: schema.FunctionCall{Name: "one", Arguments: `{}`}},
		{ID: "b", Function: schema.FunctionCall{Name: "two", Arguments: `{}`}},
	})
	resultA := schema.ToolMessage("a-result", "a")
	resultB := schema.ToolMessage("b-result", "b")
	valid := []*schema.Message{schema.UserMessage("go"), assistant, resultA, resultB, schema.AssistantMessage("done", nil)}
	if err := ValidateHistory(valid); err != nil {
		t.Fatalf("valid history rejected: %v", err)
	}
	cases := map[string][]*schema.Message{
		"missing result": {schema.UserMessage("go"), assistant, resultA},
		"wrong ID":       {schema.UserMessage("go"), assistant, resultA, schema.ToolMessage("wrong", "c")},
		"duplicate":      {schema.UserMessage("go"), assistant, resultA, resultA},
		"orphan result":  {schema.UserMessage("go"), resultA},
		"empty call ID":  {schema.AssistantMessage("", []schema.ToolCall{{Function: schema.FunctionCall{Name: "one"}}}), schema.ToolMessage("x", "")},
	}
	for name, history := range cases {
		t.Run(name, func(t *testing.T) {
			if err := ValidateHistory(history); !errors.Is(err, ErrInvalidHistory) {
				t.Fatalf("ValidateHistory error = %v, want ErrInvalidHistory", err)
			}
		})
	}
}

func waitChannel(t *testing.T, channel <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-channel:
	case <-time.After(5 * time.Second):
		t.Fatal(message)
	}
}
