package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/subagent"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func stopAfter(t *testing.T, handler adk.ChatModelAgentMiddleware) error {
	t.Helper()
	_, _, err := handler.BeforeModelRewriteState(context.Background(), &adk.ChatModelAgentState{}, nil)
	return err
}

func wrapTool(t *testing.T, handler adk.ChatModelAgentMiddleware, name string, run func() (string, error)) func() string {
	t.Helper()
	endpoint, err := handler.WrapInvokableToolCall(context.Background(), func(context.Context, string, ...tool.Option) (string, error) {
		return run()
	}, &adk.ToolContext{Name: name})
	if err != nil {
		t.Fatal(err)
	}
	return func() string {
		raw, err := endpoint(context.Background(), `{}`)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
}

func TestToolSafetyHardStopsRepeatedUnknownToolCalls(t *testing.T) {
	handler, unknown := NewToolSafety([]string{"a_tool"})
	for i := 1; i <= maxHardStopSameTool; i++ {
		if err := stopAfter(t, handler); err != nil {
			t.Fatalf("stopped early before call %d: %v", i, err)
		}
		raw, err := unknown(context.Background(), "ghost", "{}")
		if err != nil || decodeOutput(t, raw).ExitCode != 1 {
			t.Fatalf("call %d raw=%q err=%v", i, raw, err)
		}
	}
	err := stopAfter(t, handler)
	var stop *RepeatedToolFailureError
	if !errors.Is(err, ErrRepeatedToolFailure) || !errors.As(err, &stop) || stop.Tool != "ghost" || stop.Count != maxHardStopSameTool {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(err.Error(), "模型反复调用失败的工具 ghost（连续 6 次），已停止本轮") {
		t.Fatalf("message=%q", err.Error())
	}
}

func TestToolSafetySuccessResetsHardStop(t *testing.T) {
	handler, unknown := NewToolSafety([]string{"ok_tool"})
	good := wrapTool(t, handler, "ok_tool", func() (string, error) { return "ok", nil })
	for round := 0; round < 3; round++ {
		for i := 0; i < maxHardStopSameTool-1; i++ {
			_, _ = unknown(context.Background(), "ghost", "{}")
		}
		good()
		if err := stopAfter(t, handler); err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
	}
}

func TestToolSafetyGenericExecutionErrorsNeverHardStop(t *testing.T) {
	handler, _ := NewToolSafety(nil)
	failing := wrapTool(t, handler, "run", func() (string, error) { return "", errors.New("exit status 1") })
	for i := 0; i < 3*maxHardStopAnyTool; i++ {
		failing()
		if err := stopAfter(t, handler); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	// An executed failure also breaks an argument-failure streak.
	handler, unknown := NewToolSafety(nil)
	failing = wrapTool(t, handler, "ghost", func() (string, error) { return "", errors.New("exit status 1") })
	for i := 0; i < maxHardStopSameTool-1; i++ {
		_, _ = unknown(context.Background(), "ghost", "{}")
	}
	failing()
	_, _ = unknown(context.Background(), "ghost", "{}")
	if err := stopAfter(t, handler); err != nil {
		t.Fatal(err)
	}
}

func TestToolSafetyHardStopsAcrossToolNames(t *testing.T) {
	handler, unknown := NewToolSafety(nil)
	for i := 0; i < maxHardStopAnyTool; i++ {
		if err := stopAfter(t, handler); err != nil {
			t.Fatalf("stopped early at %d: %v", i, err)
		}
		_, _ = unknown(context.Background(), fmt.Sprintf("ghost_%d", i%5), "{}")
	}
	var stop *RepeatedToolFailureError
	if err := stopAfter(t, handler); !errors.As(err, &stop) || stop.Tool != "" || stop.Count != maxHardStopAnyTool {
		t.Fatalf("err=%v", err)
	}
}

func TestToolSafetyHardStopYieldsToCancellation(t *testing.T) {
	handler, unknown := NewToolSafety(nil)
	for i := 0; i < maxHardStopSameTool; i++ {
		_, _ = unknown(context.Background(), "ghost", "{}")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := handler.BeforeModelRewriteState(ctx, &adk.ChatModelAgentState{}, nil); err != nil {
		t.Fatalf("a cancelled context must not be reported as a hard stop: %v", err)
	}
}

func TestSubagentRepeatedUnknownToolFailsSubagentOnly(t *testing.T) {
	manager := newSafetySubagent(t, func(call int, _ []*schema.Message) *schema.Message {
		return toolCallTurn(fmt.Sprintf("c%d", call), "ghost", `{}`)
	}, nil)
	scope := &subagent.Scope{AllowedTools: []string{"fail_tool"}}
	handle, err := manager.Spawn(context.Background(), subagent.Request{Task: "go", Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := manager.Wait(waitCtx, handle) // Wait reports the failure as err too
	if result.Status != subagent.StatusFailed || !strings.Contains(result.Error, "模型反复调用失败的工具 ghost") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	// The failure stays with the subagent: another one still runs to completion.
	second := newSafetySubagent(t, func(int, []*schema.Message) *schema.Message { return schema.AssistantMessage("fine", nil) }, nil)
	h2, err := second.Spawn(context.Background(), subagent.Request{Task: "go", Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	if r, err := second.Wait(waitCtx, h2); err != nil || r.Status != subagent.StatusCompleted {
		t.Fatalf("r=%+v err=%v", r, err)
	}
}
