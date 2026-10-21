package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/hitl"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// The arbitration baseline is refreshed only by read_screen and post-write
// prediction (plus the confirmed-resume sync): the Eino classification path
// must never mark unseen user input as observed, or an auto-allowed write
// would silently overwrite it.

func arbitrationExecution(terminal *fakeTerminal, mode guard.Mode) *Execution {
	registry := NewRegistry(Dependencies{Terminal: terminal, PollInterval: time.Millisecond})
	return &Execution{JobID: "job", Registry: registry, Scope: Scope{SessionID: "s", TabID: "t"}, Permission: guard.Config{Mode: mode}, Memory: guard.NewMemory()}
}

func typeUserInput(terminal *fakeTerminal, text string, cursorCol int) {
	terminal.mu.Lock()
	terminal.screen = Screen{Text: text, CursorCol: cursorCol, Cols: 80, Rows: 24}
	terminal.mu.Unlock()
}

func TestExecutionInitialAutoAllowKeepsArbitration(t *testing.T) {
	terminal := &fakeTerminal{screen: Screen{Text: "$ ", CursorCol: 2, Cols: 80, Rows: 24}}
	execution := arbitrationExecution(terminal, guard.ReadWrite)
	scope := execution.Scope

	if result := execution.Registry.Execute(context.Background(), "job", scope, p1Call("r", "read_screen", `{}`), nil); !result.OK {
		t.Fatalf("baseline read = %+v", result)
	}
	typeUserInput(terminal, "$ true ", 7)

	// Safe keys are auto-allowed by the guard, so only the arbitration
	// baseline can stop the write over the user's new input.
	output, err := execution.initial(context.Background(), p1Call("k1", "send_keys", `{"keys":"true","enter":true}`))
	if err != nil {
		t.Fatalf("initial err = %v", err)
	}
	if output.OK || !strings.Contains(output.Text, ErrUserInputPending.Error()) {
		t.Fatalf("auto-allowed write bypassed arbitration: %+v", output)
	}
	if len(terminal.writes) != 0 {
		t.Fatalf("write went through: %q", terminal.writes)
	}

	// force=true still writes with an explicit note.
	output, err = execution.initial(context.Background(), p1Call("k2", "send_keys", `{"keys":"true","enter":true,"force":true}`))
	if err != nil || !output.OK || !strings.Contains(output.Text, "force=true") {
		t.Fatalf("forced write = %+v err=%v", output, err)
	}
}

func TestExecutionInitialDenyKeepsArbitration(t *testing.T) {
	terminal := &fakeTerminal{screen: Screen{Text: "$ ", CursorCol: 2, Cols: 80, Rows: 24}}
	execution := arbitrationExecution(terminal, guard.ReadOnly)
	scope := execution.Scope

	if result := execution.Registry.Execute(context.Background(), "job", scope, p1Call("r", "read_screen", `{}`), nil); !result.OK {
		t.Fatalf("baseline read = %+v", result)
	}
	typeUserInput(terminal, "$ git ", 6)

	// Read-only mode denies the confirmation-needing write outright, and the
	// denied attempt must not mark the user's input as observed.
	output, err := execution.initial(context.Background(), p1Call("k1", "send_keys", `{"keys":"push","enter":true}`))
	if err != nil || output.OK {
		t.Fatalf("denied write = %+v err=%v", output, err)
	}
	if len(terminal.writes) != 0 {
		t.Fatalf("denied write went through: %q", terminal.writes)
	}
	state := execution.Registry.state("job")
	state.mu.Lock()
	baseline, ok := state.lastInput["t"]
	state.mu.Unlock()
	if !ok || baseline.buffered != "" || baseline.cursor != 0 {
		t.Fatalf("denied attempt refreshed the baseline: %+v", baseline)
	}

	// The user clears the line back to the observed state; the next
	// auto-allowed write proceeds — no stale refusal from the denied attempt.
	typeUserInput(terminal, "$ ", 2)
	output, err = execution.initial(context.Background(), p1Call("k2", "send_keys", `{"keys":"true","enter":true}`))
	if err != nil || !output.OK {
		t.Fatalf("post-deny write refused: %+v err=%v", output, err)
	}
}

// The confirmed-resume path is the one place that may sync the baseline: the
// user confirmed against exactly the live input, so the write executes and a
// follow-up write is not refused on stale ground.
func TestExecutionConfirmResumeSyncsBaseline(t *testing.T) {
	terminal := &fakeTerminal{screen: Screen{Text: "$ ", CursorCol: 2, Cols: 80, Rows: 24}}
	execution := arbitrationExecution(terminal, guard.ReadWrite)
	scope := execution.Scope

	if result := execution.Registry.Execute(context.Background(), "job", scope, p1Call("r", "read_screen", `{}`), nil); !result.OK {
		t.Fatalf("baseline read = %+v", result)
	}
	typeUserInput(terminal, "$ git ", 6)

	available, err := execution.Tools()
	if err != nil {
		t.Fatal(err)
	}
	const arguments = `{"keys":"touch /tmp/x","enter":true}`
	chatModel := &recoveryModel{steps: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{{ID: "call-keys", Type: "function", Function: schema.FunctionCall{Name: "send_keys", Arguments: arguments}}}),
		schema.AssistantMessage("finished", nil),
	}}
	chatAgent, err := adk.NewChatModelAgent(context.Background(), &adk.ChatModelAgentConfig{
		Name: "arbitration", Description: "arbitration", Instruction: "Use the tool", Model: chatModel, MaxIterations: 3,
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: available, ExecuteSequentially: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	checkpoints := newRecoveryCheckpoints()
	runner := adk.NewRunner(context.Background(), adk.RunnerConfig{Agent: chatAgent, CheckPointStore: checkpoints})
	manager, err := hitl.NewManager(hitl.Config{Checkpoints: checkpoints, TTL: time.Hour, NewNonce: func() (string, error) { return "arbitration-nonce", nil }})
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
		t.Fatal("send_keys did not interrupt for confirmation")
	}
	if len(terminal.writes) != 0 {
		t.Fatal("write happened before confirmation")
	}
	request, err := manager.Interrupt(context.Background(), interruptContext, hitl.InterruptInput{
		RunID: "run", CheckpointID: "checkpoint", CallID: "call-keys", Tool: "send_keys", Kind: hitl.KindConfirm, Parameters: []byte(arguments),
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
	for {
		if _, ok := resumed.Next(); !ok {
			break
		}
	}
	if len(terminal.writes) != 1 {
		t.Fatalf("confirmed write did not execute: %q", terminal.writes)
	}

	// The submission clears the line to a fresh prompt, matching the
	// post-write baseline, so the follow-up auto-allowed write proceeds.
	typeUserInput(terminal, "$ ", 2)
	output, err := execution.initial(context.Background(), p1Call("k2", "send_keys", `{"keys":"true","enter":true}`))
	if err != nil || !output.OK {
		t.Fatalf("post-confirm write refused: %+v err=%v", output, err)
	}
	if len(terminal.writes) != 2 {
		t.Fatalf("writes = %q", terminal.writes)
	}
}
