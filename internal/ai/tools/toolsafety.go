package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

const maxToolFailureBytes = 600

// maxRepeatedToolFailures is how many consecutive argument or unknown-tool
// failures of one tool name are answered with a plain retry hint before the
// result tells the model to stop and explain the problem to the user.
const maxRepeatedToolFailures = 3

// UnknownToolHandler matches compose.ToolsNodeConfig.UnknownToolsHandler.
type UnknownToolHandler func(ctx context.Context, name, input string) (string, error)

// NewToolSafety returns the two hooks that keep a malformed or hallucinated
// tool call from ending the whole run: a handler that turns tool errors into
// a failed tool result, and the unknown-tool handler. available lists the
// tools this agent can actually call and is the only thing the unknown-tool
// reply names. The handler must be the last (innermost) entry of the agent's
// Handlers so an interrupt raised by an outer layer is never swallowed.
// fatal lists extra sentinel errors that intentionally abort the run.
func NewToolSafety(available []string, fatal ...error) (adk.ChatModelAgentMiddleware, UnknownToolHandler) {
	names := append([]string(nil), available...)
	sort.Strings(names)
	state := &toolSafetyState{streaks: map[string]int{}, fatal: fatal}
	handler := &toolSafetyHandler{BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{}, state: state}
	return handler, func(ctx context.Context, name, _ string) (string, error) {
		result := encodeToolFailure(state.limit(name, unknownToolOutput(name, names)))
		// The tool node does not route unknown tools through the tool
		// middleware chain, so adk never emits their result event; send it
		// here so the run loop persists and shows it like any other result.
		message := schema.ToolMessage(result, compose.GetToolCallID(ctx), schema.WithToolName(name))
		adk.EnsureMessageID(message)
		_ = adk.SendEvent(ctx, adk.EventFromMessage(message, nil, schema.Tool, name))
		return result, nil
	}
}

// ToolNames lists the names of the given tools.
func ToolNames(ctx context.Context, list []tool.BaseTool) ([]string, error) {
	names := make([]string, 0, len(list))
	for _, candidate := range list {
		info, err := candidate.Info(ctx)
		if err != nil {
			return nil, err
		}
		names = append(names, info.Name)
	}
	return names, nil
}

// toolSafetyState is shared by one agent's handler and unknown-tool handler.
type toolSafetyState struct {
	mu           sync.Mutex
	streaks      map[string]int
	lengthCutOff bool
	fatal        []error
}

func (s *toolSafetyState) setLengthCutOff(value bool) {
	s.mu.Lock()
	s.lengthCutOff = value
	s.mu.Unlock()
}

func (s *toolSafetyState) cutOff() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lengthCutOff
}

func (s *toolSafetyState) succeeded(name string) {
	s.mu.Lock()
	delete(s.streaks, name)
	s.mu.Unlock()
}

// limit counts a consecutive failure of name and, once the streak reaches the
// cap, appends the instruction to stop retrying.
func (s *toolSafetyState) limit(name string, output Output) Output {
	s.mu.Lock()
	s.streaks[name]++
	count := s.streaks[name]
	s.mu.Unlock()
	if count >= maxRepeatedToolFailures {
		output.Text += fmt.Sprintf("（已连续失败 %d 次，请停止重试）", count)
		output.ModelText += fmt.Sprintf(" This call has now failed %d times in a row. Stop retrying it: tell the user what is going wrong and ask how to proceed.", count)
	}
	return output
}

type toolSafetyHandler struct {
	*adk.BaseChatModelAgentMiddleware
	state *toolSafetyState
}

// AfterModelRewriteState remembers whether the model turn was cut off by the
// output length limit, which is the usual cause of truncated tool arguments.
func (h *toolSafetyHandler) AfterModelRewriteState(ctx context.Context, state *adk.ChatModelAgentState, _ *adk.ModelContext) (context.Context, *adk.ChatModelAgentState, error) {
	cut := false
	if state != nil && len(state.Messages) > 0 {
		last := state.Messages[len(state.Messages)-1]
		cut = last != nil && last.Role == schema.Assistant && last.ResponseMeta != nil && last.ResponseMeta.FinishReason == "length"
	}
	h.state.setLengthCutOff(cut)
	return ctx, state, nil
}

func (h *toolSafetyHandler) WrapInvokableToolCall(_ context.Context, endpoint adk.InvokableToolCallEndpoint, tCtx *adk.ToolContext) (adk.InvokableToolCallEndpoint, error) {
	name := ""
	if tCtx != nil {
		name = tCtx.Name
	}
	return func(ctx context.Context, arguments string, opts ...tool.Option) (string, error) {
		// The tool node's argument handler can only rewrite arguments or fail
		// the whole run, so the JSON check lives here where a failed result
		// can be returned instead.
		if strings.TrimSpace(arguments) != "" && !json.Valid([]byte(arguments)) {
			return encodeToolFailure(h.state.limit(name, invalidArgumentsOutput(name, h.state.cutOff()))), nil
		}
		result, err := endpoint(ctx, arguments, opts...)
		if err == nil {
			h.state.succeeded(name)
			return result, nil
		}
		if toolErrorEndsRun(ctx, err) {
			return result, err
		}
		for _, sentinel := range h.state.fatal {
			if errors.Is(err, sentinel) {
				return result, err
			}
		}
		output := toolErrorOutput(name, err)
		if strings.Contains(err.Error(), "failed to unmarshal arguments") {
			output = h.state.limit(name, output)
		}
		return encodeToolFailure(output), nil
	}, nil
}

// toolErrorEndsRun reports errors that must keep propagating: HITL
// interrupts, cancellation of the run itself, and adk cancel signals.
func toolErrorEndsRun(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	// Identity drift between confirmation and execution must fail the run
	// closed instead of letting the model retry against another asset.
	if errors.Is(err, ErrAssetIdentityChanged) || errors.Is(err, ErrAssetIdentityUnverifiable) {
		return true
	}
	if _, ok := compose.IsInterruptRerunError(err); ok {
		return true
	}
	if _, ok := compose.ExtractInterruptInfo(err); ok {
		return true
	}
	var cancel *adk.CancelError
	if errors.As(err, &cancel) {
		return true
	}
	var streamCancel *adk.StreamCanceledError
	return errors.As(err, &streamCancel)
}

func toolErrorOutput(name string, err error) Output {
	detail := clipToolFailure(RedactText(err.Error()))
	if strings.Contains(err.Error(), "failed to unmarshal arguments") {
		return Output{
			Text:      "工具参数无法解析：" + detail,
			ModelText: fmt.Sprintf("The arguments for tool %s are not valid JSON for its schema (%s). The tool did not run. Fix the arguments and call it again.", name, detail),
			ExitCode:  1,
		}
	}
	return Output{
		Text:      "工具执行出错：" + detail,
		ModelText: fmt.Sprintf("Tool %s failed with an error (%s). Check the arguments and the current state, then retry or choose another approach.", name, detail),
		ExitCode:  1,
	}
}

func invalidArgumentsOutput(name string, lengthCutOff bool) Output {
	text := "工具参数不是有效的 JSON，可能被截断；请用更短的参数重试（例如把大段文件内容拆成多次写入）"
	modelText := fmt.Sprintf("The arguments for tool %s are not valid JSON, probably because they were truncated. The tool did not run. Retry with shorter arguments (for example split large file content into several smaller writes).", name)
	if lengthCutOff {
		text = "模型输出达到长度上限，工具参数被截断；" + text
		modelText = "The model's output hit the length limit, so the tool arguments were cut off. " + modelText
	}
	return Output{Text: text, ModelText: modelText, ExitCode: 1}
}

func unknownToolOutput(name string, available []string) Output {
	shown := clipToolFailure(RedactText(name))
	list := strings.Join(available, ", ")
	return Output{
		Text:      fmt.Sprintf("工具 %s 不存在；可用工具：%s", shown, list),
		ModelText: fmt.Sprintf("Tool %s does not exist and was not run. Available tools: %s. Call one of these exactly by name.", shown, list),
		ExitCode:  1,
	}
}

func encodeToolFailure(output Output) string {
	encoded, err := json.Marshal(output)
	if err != nil {
		return `{"ok":false,"text":"工具执行出错","exitCode":1,"truncated":false}`
	}
	return string(encoded)
}

func clipToolFailure(text string) string {
	if len(text) <= maxToolFailureBytes {
		return text
	}
	cut := maxToolFailureBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "..."
}
