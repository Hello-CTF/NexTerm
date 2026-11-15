package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"

	"github.com/Hello-CTF/NexTerm/internal/ai/provider"
)

const SpawnToolName = "spawn_subagent"

const spawnToolDescription = "Run an isolated subtask with a fresh context. The subagent can only use the tools configured by the caller; nested spawn is not supported. " +
	"It cannot ask the user: a tool call that would need user confirmation is not executed and the subagent is told to leave that action to you. " +
	"It runs at most a limited number of model steps within a time limit. If it fails, runs out of steps or time, the result is a failed tool result with the reason, its partial output and its recent tool calls, so you can continue the work yourself, retry with a narrower task, or ask the user. " +
	"A model profile can be specified via modelProfileId; the currently active profile is used by default."

type SpawnInput struct {
	Task           string `json:"task" jsonschema:"required" jsonschema_description:"Complete, self-contained description of the subtask; the subagent sees nothing else from this conversation"`
	Persona        string `json:"persona,omitempty" jsonschema_description:"Optional extra instruction describing the role or style the subagent should take"`
	ModelProfileID string `json:"modelProfileId,omitempty" jsonschema_description:"Model profile id to run the subagent with (default the active profile)"`
}

// SpawnOutput uses the tool result envelope shared with the other AI tools:
// Text is shown to the user and ModelText, when set, is what the model reads.
type SpawnOutput struct {
	OK        bool   `json:"ok"`
	Text      string `json:"text"`
	ModelText string `json:"modelText,omitempty"`
	ExitCode  int    `json:"exitCode"`
}

func NewSpawnTool(manager *Manager, scope Scope, factories ...ObserverFactory) (tool.InvokableTool, error) {
	if manager == nil {
		return nil, errors.New("subagent manager is required")
	}
	normalized, _, err := normalizeScope(&scope)
	if err != nil {
		return nil, err
	}
	return utils.InferTool(SpawnToolName, spawnToolDescription, func(ctx context.Context, input SpawnInput) (SpawnOutput, error) {
		request := Request{Task: input.Task, Persona: input.Persona, Scope: &normalized, ModelProfileID: input.ModelProfileID}
		if len(factories) > 0 && factories[0] != nil {
			request.Observer = factories[0](ctx)
		}
		handle, err := manager.Spawn(ctx, request)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return SpawnOutput{}, ctxErr
			}
			if errors.Is(err, ErrNestedSpawn) {
				return SpawnOutput{Text: err.Error(), ExitCode: 1}, nil
			}
			return SpawnOutput{
				Text:      "子代理无法启动：" + err.Error(),
				ModelText: "the subagent could not be started: " + provider.ProjectModelErrorText(err.Error()),
				ExitCode:  1,
			}, nil
		}
		stop := make(chan struct{})
		defer close(stop)
		go func() {
			select {
			case <-ctx.Done():
				_ = manager.Cancel(handle)
			case <-stop:
			}
		}()
		result, err := manager.Wait(ctx, handle)
		// Only the caller's own cancellation ends the caller's run; every
		// failure of the subagent itself is reported back as a tool result.
		if ctxErr := ctx.Err(); ctxErr != nil {
			if err == nil || !errors.Is(err, ctxErr) {
				err = errors.Join(ctxErr, err)
			}
			return SpawnOutput{}, err
		}
		if err == nil && result.Status == StatusCompleted {
			return SpawnOutput{OK: true, Text: result.Output}, nil
		}
		return manager.failureOutput(result, err), nil
	})
}

const (
	reportPartialBytes = 2000
	reportToolCalls    = 6
	reportArgsBytes    = 160
	reportResultBytes  = 240
)

func (m *Manager) failureOutput(result Result, err error) SpawnOutput {
	if err == nil {
		message := result.Error
		if message == "" {
			message = "subagent ended with status " + string(result.Status)
		}
		err = errors.New(message)
	}
	status := result.Status
	if status == "" || status == StatusRunning || status == StatusCompleted {
		status = StatusFailed
	}
	zhReason, enReason := m.failureReason(err)
	calls := reportCalls(result.History)
	partial, cut := capText(strings.TrimSpace(result.Output), reportPartialBytes)
	if cut {
		partial += "…"
	}

	var zh, en strings.Builder
	fmt.Fprintf(&zh, "子代理未完成（状态：%s，已执行 %d 步）。\n原因：%s。", status, result.Turns, zhReason)
	fmt.Fprintf(&en, "The subagent did not finish (status: %s, %d steps taken).\nReason: %s.", status, result.Turns, enReason)
	if partial != "" {
		zh.WriteString("\n部分输出：\n" + partial)
		en.WriteString("\nPartial output:\n" + partial)
	}
	if len(calls) > 0 {
		zh.WriteString("\n最近的工具调用：")
		en.WriteString("\nRecent tool calls:")
		for _, call := range calls {
			zh.WriteString("\n- " + call.zh)
			en.WriteString("\n- " + call.en)
		}
	}
	if partial == "" && len(calls) == 0 {
		zh.WriteString("\n子代理没有产生输出，也没有完成任何工具调用。")
		en.WriteString("\nThe subagent produced no output and completed no tool calls.")
	}
	en.WriteString("\nIts work is incomplete: perform the remaining steps yourself, retry with a narrower task, or ask the user.")
	return SpawnOutput{Text: zh.String(), ModelText: en.String(), ExitCode: 1}
}

func (m *Manager) failureReason(err error) (string, string) {
	switch {
	case errors.Is(err, ErrIterationLimit):
		return fmt.Sprintf("达到步数上限（%d 步）时仍未完成", m.config.MaxIterations),
			fmt.Sprintf("it reached its step limit (%d steps) before finishing", m.config.MaxIterations)
	case errors.Is(err, ErrInteractionUnsupported):
		return "它需要用户交互，但子代理内无法请求用户交互",
			"it needed user interaction, which is not available inside a subagent"
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Sprintf("超出运行时限（%s）", m.config.MaxRunTime),
			fmt.Sprintf("it ran out of time (limit %s)", m.config.MaxRunTime)
	case errors.Is(err, context.Canceled):
		return "已被取消", "it was canceled"
	default:
		text := err.Error()
		return "运行出错：" + text, "it failed with an error: " + provider.ProjectModelErrorText(text)
	}
}

type reportCall struct {
	zh string
	en string
}

// reportCalls summarizes the most recent completed tool calls in the
// subagent history, pairing each call with its result.
func reportCalls(history []*schema.Message) []reportCall {
	type pending struct {
		name string
		args string
	}
	calls := make(map[string]pending)
	var result []reportCall
	for _, message := range history {
		if message == nil {
			continue
		}
		switch message.Role {
		case schema.Assistant:
			for _, call := range message.ToolCalls {
				calls[call.ID] = pending{name: call.Function.Name, args: call.Function.Arguments}
			}
		case schema.Tool:
			call, ok := calls[message.ToolCallID]
			if !ok {
				continue
			}
			delete(calls, message.ToolCallID)
			args, cut := capText(oneLine(call.args), reportArgsBytes)
			if cut {
				args += "…"
			}
			head := strings.TrimSpace(call.name + " " + args)
			zhResult, enResult := reportResult(call.name, message.Content)
			result = append(result, reportCall{zh: head + " -> " + zhResult, en: head + " -> " + enResult})
		}
	}
	if len(result) > reportToolCalls {
		result = result[len(result)-reportToolCalls:]
	}
	return result
}

func reportResult(name, content string) (string, string) {
	var envelope struct {
		OK        *bool  `json:"ok"`
		Text      string `json:"text"`
		ModelText string `json:"modelText"`
		Truncated bool   `json:"truncated"`
		Panic     bool   `json:"panic"`
	}
	if json.Unmarshal([]byte(content), &envelope) != nil || envelope.OK == nil {
		text := capLine(content)
		return text, text
	}
	modelText := envelope.ModelText
	if modelText == "" {
		modelText = provider.ProjectModelOutput(name, *envelope.OK, envelope.Text, envelope.Truncated, envelope.Panic)
	}
	if *envelope.OK {
		return "成功：" + capLine(envelope.Text), "ok: " + capLine(modelText)
	}
	return "失败：" + capLine(envelope.Text), "failed: " + capLine(modelText)
}

func capLine(text string) string {
	line, cut := capText(oneLine(text), reportResultBytes)
	if cut {
		line += "…"
	}
	return line
}

func oneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}
