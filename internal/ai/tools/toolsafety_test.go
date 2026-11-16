package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func callWrapped(t *testing.T, ctx context.Context, fail error) (string, error) {
	t.Helper()
	handler, _ := NewToolSafety([]string{"b_tool", "a_tool"})
	endpoint, err := handler.WrapInvokableToolCall(ctx, func(context.Context, string, ...tool.Option) (string, error) {
		return "", fail
	}, &adk.ToolContext{Name: "a_tool", CallID: "c1"})
	if err != nil {
		t.Fatal(err)
	}
	return endpoint(ctx, "{}")
}

func decodeOutput(t *testing.T, raw string) Output {
	t.Helper()
	var output Output
	if err := json.Unmarshal([]byte(raw), &output); err != nil {
		t.Fatalf("not a tool envelope: %q: %v", raw, err)
	}
	return output
}

func TestToolSafetyConvertsMalformedArgumentsToFailedResult(t *testing.T) {
	raw, err := callWrapped(t, context.Background(), errors.New("[LocalFunc] failed to unmarshal arguments in json: password=hunter2 unexpected end of JSON input"))
	if err != nil {
		t.Fatalf("error should become a result: %v", err)
	}
	output := decodeOutput(t, raw)
	if output.OK || output.ExitCode == 0 {
		t.Fatalf("output=%+v", output)
	}
	if !strings.HasPrefix(output.Text, "工具参数无法解析：") || strings.Contains(output.Text, "hunter2") || strings.Contains(output.ModelText, "hunter2") {
		t.Fatalf("text=%q model=%q", output.Text, output.ModelText)
	}
	if !strings.Contains(output.ModelText, "a_tool") || !strings.Contains(output.ModelText, "Fix the arguments") {
		t.Fatalf("model text=%q", output.ModelText)
	}
}

func TestToolSafetyConvertsGenericErrorAndTruncates(t *testing.T) {
	raw, err := callWrapped(t, context.Background(), errors.New(strings.Repeat("错", 2000)))
	if err != nil {
		t.Fatal(err)
	}
	output := decodeOutput(t, raw)
	if output.OK || !strings.HasPrefix(output.Text, "工具执行出错：") || len(output.Text) > 1000 {
		t.Fatalf("len=%d output=%+v", len(output.Text), output)
	}
}

func TestToolSafetyPassesThroughRunEndingErrors(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	cases := map[string]struct {
		ctx context.Context
		err error
	}{
		"run canceled":  {canceled, errors.New("anything")},
		"stream cancel": {context.Background(), fmt.Errorf("wrap: %w", &adk.StreamCanceledError{})},
		"cancel error":  {context.Background(), &adk.CancelError{}},
		"asset changed": {context.Background(), fmt.Errorf("drift: %w", ErrAssetIdentityChanged)},
		"unverifiable":  {context.Background(), ErrAssetIdentityUnverifiable},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := callWrapped(t, c.ctx, c.err)
			if err == nil || raw != "" {
				t.Fatalf("raw=%q err=%v", raw, err)
			}
		})
	}
}

func TestToolSafetyPassesThroughCallerSentinels(t *testing.T) {
	sentinel := errors.New("owner lost")
	handler, _ := NewToolSafety(nil, sentinel)
	endpoint, _ := handler.WrapInvokableToolCall(context.Background(), func(context.Context, string, ...tool.Option) (string, error) {
		return "", fmt.Errorf("wrapped: %w", sentinel)
	}, &adk.ToolContext{Name: "x"})
	if _, err := endpoint(context.Background(), "{}"); !errors.Is(err, sentinel) {
		t.Fatalf("err=%v", err)
	}
}

func TestToolSafetyPassesThroughInterrupt(t *testing.T) {
	_, err := callWrapped(t, context.Background(), tool.Interrupt(context.Background(), "need confirmation"))
	if err == nil {
		t.Fatal("interrupt must not be converted into a result")
	}
}

func TestToolSafetySuccessIsUntouched(t *testing.T) {
	handler, _ := NewToolSafety(nil)
	endpoint, _ := handler.WrapInvokableToolCall(context.Background(), func(context.Context, string, ...tool.Option) (string, error) {
		return `{"ok":true}`, nil
	}, &adk.ToolContext{Name: "x"})
	if raw, err := endpoint(context.Background(), "{}"); err != nil || raw != `{"ok":true}` {
		t.Fatalf("raw=%q err=%v", raw, err)
	}
}

func invalidJSONResult(t *testing.T, handler adk.ChatModelAgentMiddleware, name string) (Output, bool) {
	t.Helper()
	called := false
	endpoint, err := handler.WrapInvokableToolCall(context.Background(), func(context.Context, string, ...tool.Option) (string, error) {
		called = true
		return `{"ok":true}`, nil
	}, &adk.ToolContext{Name: name})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := endpoint(context.Background(), `{"content":"cut off`)
	if err != nil {
		t.Fatal(err)
	}
	return decodeOutput(t, raw), called
}

func TestToolSafetyRejectsInvalidJSONWithoutRunningTool(t *testing.T) {
	handler, _ := NewToolSafety(nil)
	output, called := invalidJSONResult(t, handler, "write_file")
	if called || output.OK || !strings.Contains(output.Text, "不是有效的 JSON") || !strings.Contains(output.ModelText, "shorter arguments") {
		t.Fatalf("called=%v output=%+v", called, output)
	}
	if strings.Contains(output.ModelText, "length limit") {
		t.Fatalf("no length cut-off was seen: %q", output.ModelText)
	}
}

func TestToolSafetyLeavesValidAndEmptyArgumentsAlone(t *testing.T) {
	handler, _ := NewToolSafety(nil)
	var seen []string
	endpoint, _ := handler.WrapInvokableToolCall(context.Background(), func(_ context.Context, arguments string, _ ...tool.Option) (string, error) {
		seen = append(seen, arguments)
		return "ok", nil
	}, &adk.ToolContext{Name: "x"})
	for _, arguments := range []string{`{"a": 1}`, "", " "} {
		if raw, err := endpoint(context.Background(), arguments); err != nil || raw != "ok" {
			t.Fatalf("arguments %q: raw=%q err=%v", arguments, raw, err)
		}
	}
	if len(seen) != 3 || seen[0] != `{"a": 1}` {
		t.Fatalf("seen=%q", seen)
	}
}

func TestToolSafetyMentionsLengthLimitAfterCutOffTurn(t *testing.T) {
	handler, _ := NewToolSafety(nil)
	cut := schema.AssistantMessage("", nil)
	cut.ResponseMeta = &schema.ResponseMeta{FinishReason: "length"}
	if _, _, err := handler.AfterModelRewriteState(context.Background(), &adk.ChatModelAgentState{Messages: []*schema.Message{cut}}, nil); err != nil {
		t.Fatal(err)
	}
	output, _ := invalidJSONResult(t, handler, "write_file")
	if !strings.Contains(output.ModelText, "hit the length limit") || !strings.Contains(output.Text, "长度上限") {
		t.Fatalf("output=%+v", output)
	}
	stopped := schema.AssistantMessage("", nil)
	stopped.ResponseMeta = &schema.ResponseMeta{FinishReason: "tool_calls"}
	_, _, _ = handler.AfterModelRewriteState(context.Background(), &adk.ChatModelAgentState{Messages: []*schema.Message{stopped}}, nil)
	if output, _ := invalidJSONResult(t, handler, "write_file"); strings.Contains(output.ModelText, "length limit") {
		t.Fatalf("stale cut-off flag: %+v", output)
	}
}

func TestToolSafetyStopsRepeatedFailuresAndResetsOnSuccess(t *testing.T) {
	handler, unknown := NewToolSafety([]string{"write_file"})
	for attempt := 1; attempt <= 4; attempt++ {
		output, _ := invalidJSONResult(t, handler, "write_file")
		stop := strings.Contains(output.ModelText, "Stop retrying")
		if stop != (attempt >= maxRepeatedToolFailures) {
			t.Fatalf("attempt %d stop=%v output=%+v", attempt, stop, output)
		}
		if stop && !strings.Contains(output.Text, "请停止重试") {
			t.Fatalf("text=%q", output.Text)
		}
	}
	ok, _ := handler.WrapInvokableToolCall(context.Background(), func(context.Context, string, ...tool.Option) (string, error) { return "{}", nil }, &adk.ToolContext{Name: "write_file"})
	if _, err := ok(context.Background(), `{}`); err != nil {
		t.Fatal(err)
	}
	if output, _ := invalidJSONResult(t, handler, "write_file"); strings.Contains(output.ModelText, "Stop retrying") {
		t.Fatalf("streak was not reset: %+v", output)
	}
	var last Output
	for attempt := 0; attempt < maxRepeatedToolFailures; attempt++ {
		raw, _ := unknown(context.Background(), "ghost", "{}")
		last = decodeOutput(t, raw)
	}
	if !strings.Contains(last.ModelText, "Stop retrying") {
		t.Fatalf("unknown tool streak: %+v", last)
	}
}

func TestUnknownToolHandlerListsOnlyAvailableTools(t *testing.T) {
	_, unknown := NewToolSafety([]string{"read_file", "list_dir"})
	raw, err := unknown(context.Background(), "rm_rf", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	output := decodeOutput(t, raw)
	if output.OK || !strings.Contains(output.Text, "rm_rf") || !strings.Contains(output.Text, "list_dir, read_file") {
		t.Fatalf("output=%+v", output)
	}
	if !strings.Contains(output.ModelText, "Available tools: list_dir, read_file") || strings.Contains(output.ModelText, "write_file") {
		t.Fatalf("model text=%q", output.ModelText)
	}
}
