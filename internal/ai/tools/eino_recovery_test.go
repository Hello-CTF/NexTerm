package tools

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/hitl"
	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/outcome"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type recoveryCheckpoints struct {
	mu     sync.Mutex
	values map[string][]byte
}

func newRecoveryCheckpoints() *recoveryCheckpoints {
	return &recoveryCheckpoints{values: make(map[string][]byte)}
}
func (s *recoveryCheckpoints) Get(_ context.Context, id string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[id]
	return append([]byte(nil), value...), ok, nil
}
func (s *recoveryCheckpoints) Set(_ context.Context, id string, value []byte) error {
	s.mu.Lock()
	s.values[id] = append([]byte(nil), value...)
	s.mu.Unlock()
	return nil
}
func (s *recoveryCheckpoints) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	delete(s.values, id)
	s.mu.Unlock()
	return nil
}

type recoveryModel struct {
	mu    sync.Mutex
	steps []*schema.Message
	index int
}

func (m *recoveryModel) next() *schema.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.index >= len(m.steps) {
		return schema.AssistantMessage("finished", nil)
	}
	message := m.steps[m.index]
	m.index++
	return message
}
func (m *recoveryModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return m.next(), nil
}
func (m *recoveryModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{m.next()}), nil
}

// TestEinoHITLOutcomeProductionResumeWithoutEnvironmentNamesRejected pins the
// fail-closed contract: a production resume whose confirmations omit the typed
// environment names executes nothing, the rejection is recorded, and the
// consumed request still cannot be replayed. The typed-name success path is
// covered end to end by the agent-level production exec regressions.
func TestEinoHITLOutcomeProductionResumeWithoutEnvironmentNamesRejected(t *testing.T) {
	const arguments = `{"commands":["echo one"],"asset_ids":["asset-prod"]}`
	transport := successfulBatchTransport()
	service := batchToolService(map[string]execbatch.Target{"session-prod": batchTarget(transport, "asset-prod", "production")}, &batchAuditRecorder{})
	_, ledger, sqliteStore := outcomeFixture(t)
	execution := &Execution{
		JobID: "job", Scope: Scope{SessionID: "session-current"}, Permission: guard.Config{Mode: guard.Silent}, Memory: guard.NewMemory(),
		Registry: NewRegistry(Dependencies{
			ResolveExecTargets: func(context.Context, []string, []string) (ExecTargetSet, error) {
				return ExecTargetSet{SessionIDs: []string{"session-prod"}, Production: true}, nil
			},
			ExecBatch: func(ctx context.Context, request execbatch.Request) (execbatch.Batch, error) {
				return service.Execute(ctx, "owner-a", request)
			},
			Outcome: ledger,
		}),
	}
	available, err := execution.Tools()
	if err != nil {
		t.Fatal(err)
	}
	chatModel := &recoveryModel{steps: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{{ID: "call-exec", Type: "function", Function: schema.FunctionCall{Name: "exec_commands", Arguments: arguments}}}),
		schema.AssistantMessage("finished", nil),
	}}
	chatAgent, err := adk.NewChatModelAgent(context.Background(), &adk.ChatModelAgentConfig{
		Name: "recovery", Description: "recovery", Instruction: "Use the tool", Model: chatModel, MaxIterations: 3,
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: available, ExecuteSequentially: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	checkpoints := newRecoveryCheckpoints()
	runner := adk.NewRunner(context.Background(), adk.RunnerConfig{Agent: chatAgent, CheckPointStore: checkpoints})
	manager, err := hitl.NewManager(hitl.Config{Checkpoints: checkpoints, TTL: time.Hour, NewNonce: func() (string, error) { return "recovery-nonce", nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if _, err := manager.RegisterRun(context.Background(), "run", "checkpoint"); err != nil {
		t.Fatal(err)
	}
	iterator := runner.Run(context.Background(), []adk.Message{schema.UserMessage("execute")}, adk.WithCheckPointID("checkpoint"))
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
		t.Fatal("production execution did not interrupt")
	}
	if transport.calls() != 0 {
		t.Fatalf("commands executed before confirmation: %v", transport.execCalls)
	}
	request, err := manager.Interrupt(context.Background(), interruptContext, hitl.InterruptInput{
		RunID: "run", CheckpointID: "checkpoint", CallID: "call-exec", Tool: "exec_commands", Kind: hitl.KindConfirm, Parameters: []byte(arguments),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, resumed, err := manager.Resume(context.Background(), runner, hitl.Answer{
		ID: "answer", RunID: request.RunID, RequestID: request.ID, CheckpointID: request.CheckpointID,
		TargetID: request.TargetID, CallID: request.CallID, Nonce: request.Nonce,
		Parameters: request.Parameters, Decision: hitl.DecisionAllow,
	})
	if err != nil {
		t.Fatal(err)
	}
	var completed string
	for {
		event, ok := resumed.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			t.Fatal(event.Err)
		}
		if event.Output != nil && event.Output.MessageOutput != nil && event.Output.MessageOutput.Message != nil {
			completed = event.Output.MessageOutput.Message.Content
		}
	}
	if completed != "finished" || transport.calls() != 0 {
		t.Fatalf("completed = %q, commands = %v", completed, transport.execCalls)
	}
	if record := recordedOutcome(t, sqliteStore, OutcomeKey("job", "call-exec:production")); record.Outcome != outcome.OutcomeUnknown {
		t.Fatalf("rejected resume outcome = %+v", record)
	}
	if _, _, err := manager.Resume(context.Background(), runner, hitl.Answer{
		ID: "answer", RunID: request.RunID, RequestID: request.ID, CheckpointID: request.CheckpointID,
		TargetID: request.TargetID, CallID: request.CallID, Nonce: request.Nonce,
		Parameters: request.Parameters, Decision: hitl.DecisionAllow,
	}); !errors.Is(err, hitl.ErrRequestConsumed) {
		t.Fatalf("duplicate resume error = %v", err)
	}
	if transport.calls() != 0 {
		t.Fatalf("duplicate resume executed commands: %v", transport.execCalls)
	}
}
