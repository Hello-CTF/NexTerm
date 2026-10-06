package takeover

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
	"unicode/utf8"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/agent"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/guard"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type runResult struct {
	answer string
	reason string
	steps  int
	err    error
}

type takeoverRuntime struct {
	runner     *adk.Runner
	input      []*schema.Message
	resume     *adk.ResumeParams
	permission guard.Config
	allowWrite bool
	answer     string
	reason     string
	steps      int
}

func (m *Manager) runJob(state *runState) {
	result := runResult{}
	paused := false
	userPaused := false
	pauseEscalated := false
	defer func() {
		if recovered := recover(); recovered != nil {
			result.err = fmt.Errorf("接管任务内部错误: %v", recovered)
			paused = false
			userPaused = false
			pauseEscalated = false
		}
		resumePaused := false
		state.pendingMu.Lock()
		if paused && state.ctx.Err() == nil && state.eino.resume != nil {
			resumePaused = true
			state.running = true
			state.installIterationLocked()
		} else {
			state.running = false
		}
		state.pendingMu.Unlock()
		if resumePaused {
			go m.runJob(state)
			return
		}
		if paused && state.ctx.Err() != nil {
			paused = false
			result = controlledResult(state, result.steps)
		}
		if paused {
			return
		}
		if pauseEscalated {
			m.complete(state, runResult{err: errors.New("暂停超时：模型未在安全点内响应，任务已停止")})
			return
		}
		if userPaused && state.stopReason() == "" {
			m.mu.Lock()
			current := m.jobs[state.id] == state
			m.mu.Unlock()
			if !current {
				return
			}
			state.pendingMu.Lock()
			outcomeCh := state.cancelOutcome
			state.pendingMu.Unlock()
			escalated := false
			if outcomeCh != nil {
				select {
				case outcome := <-outcomeCh:
					escalated = errors.Is(outcome, adk.ErrCancelTimeout)
				case <-time.After(3 * time.Second):
					escalated = true
				}
			}
			if escalated {
				m.complete(state, runResult{err: errors.New("暂停超时：模型未在安全点内响应，任务已停止")})
				return
			}
			if !m.checkpointExists(state) && state.eino != nil && state.eino.steps == 0 && state.eino.answer == "" {
				m.complete(state, runResult{answer: "用户暂停", reason: "用户暂停"})
				return
			}
			if !m.awaitCheckpoint(state) {
				result.err = errors.New("接管 checkpoint 未能持久化，暂停不可恢复")
				m.complete(state, result)
				return
			}
			m.persistRecovery(state)
			emitCtx, cancel := context.WithTimeout(context.WithoutCancel(state.ctx), time.Second)
			defer cancel()
			_ = state.emit(emitCtx, agent.Event{Type: "paused", Reason: "用户暂停", TabID: state.args.TabID})
			state.pendingMu.Lock()
			state.pauseReady = true
			state.pendingMu.Unlock()
			return
		}
		if result.reason == "" {
			result.reason = state.stopReason()
		}
		if result.reason == "" {
			result.reason = "任务完成"
		}
		m.complete(state, result)
	}()
	state.pendingMu.Lock()
	iterCtx, iterCancel := state.iterCtx, state.iterCancel
	state.pendingMu.Unlock()
	defer iterCancel()
	result = m.run(iterCtx, state)
	if errors.Is(result.err, errRunPaused) {
		paused = true
		result.err = nil
	}
	if errors.Is(result.err, errRunUserPaused) {
		userPaused = true
		result.err = nil
	}
	if errors.Is(result.err, errPauseEscalated) {
		pauseEscalated = true
		result.err = nil
	}
}

func (m *Manager) run(iterCtx context.Context, state *runState) runResult {
	if state.eino == nil {
		if err := m.initializeEino(state); err != nil {
			return runResult{err: err}
		}
	}
	runtime := state.eino
	cancelOption, cancelFn := adk.WithCancel()
	state.pendingMu.Lock()
	state.cancelFn = cancelFn
	state.cancelOutcome = make(chan error, 1)
	paused := state.userPaused
	state.pendingMu.Unlock()
	if paused {
		m.cancelIteration(cancelFn, state, true)
	}
	options := []adk.AgentRunOption{cancelOption, adk.WithCheckPointID(state.id)}
	var iterator *adk.AsyncIterator[*adk.AgentEvent]
	var err error
	state.pendingMu.Lock()
	started := state.started
	state.started = true
	resume := runtime.resume
	runtime.resume = nil
	state.pendingMu.Unlock()
	switch {
	case resume != nil:
		iterator, err = runtime.runner.ResumeWithParams(iterCtx, state.id, resume, options...)
	case started:
		iterator, err = runtime.runner.Resume(iterCtx, state.id, options...)
	default:
		iterator = runtime.runner.Run(iterCtx, runtime.input, options...)
	}
	if err == nil {
		result := m.consume(iterCtx, state, iterator)
		if result.err != nil && isCancelKind(result.err) {
			state.pendingMu.Lock()
			userPaused := state.userPaused
			state.pendingMu.Unlock()
			if userPaused && state.ctx.Err() == nil {
				if isTimeoutEscalation(result.err) {
					return runResult{err: errPauseEscalated}
				}
				return runResult{err: errRunUserPaused}
			}
			return controlledResult(state, runtime.steps)
		}
		return result
	}
	if isCancelKind(err) {
		state.pendingMu.Lock()
		userPaused := state.userPaused
		state.pendingMu.Unlock()
		if userPaused && state.ctx.Err() == nil {
			if isTimeoutEscalation(err) {
				return runResult{err: errPauseEscalated}
			}
			return runResult{err: errRunUserPaused}
		}
		return controlledResult(state, runtime.steps)
	}
	return runResult{err: err}
}

func isCancelKind(err error) bool {
	var cancelErr *adk.CancelError
	return errors.Is(err, context.Canceled) || errors.Is(err, adk.ErrStreamCanceled) || errors.As(err, &cancelErr)
}

func isTimeoutEscalation(err error) bool {
	var cancelErr *adk.CancelError
	return errors.As(err, &cancelErr) && cancelErr.Info != nil && cancelErr.Info.Escalated && cancelErr.Info.Timeout
}

func (m *Manager) initializeEino(state *runState) error {
	permission, err := m.deps.Permission(state.ctx)
	if err != nil {
		return err
	}
	permission = permission.Normalized()
	chatModel, _, err := m.deps.Model(state.ctx)
	if err != nil {
		return err
	}
	runtime := &takeoverRuntime{
		input: []*schema.Message{
			schema.SystemMessage("你是 NexTerm 的终端接管助手。根据当前屏幕逐步完成用户指令。只能使用 read_screen、send_keys、wait_for 和 done。命令会真实写入终端；完成或无法继续时必须调用 done，并如实报告 success。中文回答。"),
			schema.UserMessage(state.args.Instruction),
		},
		permission: permission, allowWrite: state.args.AllowWrite == nil || *state.args.AllowWrite,
	}
	execution := &actionExecution{manager: m, state: state, runtime: runtime}
	einoTools, err := execution.tools()
	if err != nil {
		return err
	}
	chatAgent, err := adk.NewChatModelAgent(state.ctx, &adk.ChatModelAgentConfig{
		Name: "nexterm-takeover", Description: "终端接管助手",
		Instruction: "根据每次模型调用前提供的新鲜终端屏幕执行动作，不要复述过期的屏幕。",
		Model:       chatModel, MaxIterations: state.args.MaxSteps,
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: einoTools, ExecuteSequentially: true}, ReturnDirectly: map[string]bool{"done": true}},
		Middlewares: []adk.AgentMiddleware{{BeforeChatModel: func(ctx context.Context, agentState *adk.ChatModelAgentState) error {
			m.waitIdle(ctx, state.args.TabID)
			screen, err := m.deps.Snapshot(ctx, state.args.TabID)
			if err != nil {
				return err
			}
			if err := state.emit(ctx, agent.Event{Type: "screen", TabID: state.args.TabID, Text: screen.Text}); err != nil {
				return err
			}
			agentState.Messages = append(agentState.Messages, schema.UserMessage(fmt.Sprintf("[当前终端]\n光标 (%d,%d)，空闲 %dms\n%s", screen.CursorRow, screen.CursorCol, screen.IdleMS, screen.Text)))
			return nil
		}}},
	})
	if err != nil {
		return err
	}
	runtime.runner = adk.NewRunner(state.ctx, adk.RunnerConfig{Agent: chatAgent, EnableStreaming: true, CheckPointStore: m.checkpoints})
	state.eino = runtime
	return nil
}

func (m *Manager) consume(iterCtx context.Context, state *runState, iterator *adk.AsyncIterator[*adk.AgentEvent]) runResult {
	runtime := state.eino
	for {
		event, ok := iterator.Next()
		if !ok {
			reason := runtime.reason
			if reason == "" {
				reason = "模型停止"
			}
			return runResult{answer: runtime.answer, reason: reason, steps: runtime.steps}
		}
		if event.Err != nil {
			if errors.Is(event.Err, adk.ErrExceedMaxIterations) {
				return runResult{answer: fmt.Sprintf("已达到最大 %d 步接管上限", state.args.MaxSteps), reason: "达到最大步骤", steps: state.args.MaxSteps}
			}
			return runResult{err: event.Err}
		}
		if event.Action != nil && event.Action.Interrupted != nil {
			if err := m.handleInterrupt(iterCtx, state, event.Action.Interrupted.InterruptContexts); err != nil {
				return runResult{err: err}
			}
			return runResult{err: errRunPaused}
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		variant := event.Output.MessageOutput
		message, err := m.consumeMessageVariant(iterCtx, state, variant)
		if err != nil {
			if isCancelKind(err) {
				continue
			}
			return runResult{err: err}
		}
		if message == nil {
			continue
		}
		switch variant.Role {
		case schema.Assistant:
			runtime.steps++
			runtime.answer = message.Content
			if !variant.IsStreaming {
				if err := emitText(iterCtx, state, message); err != nil {
					return runResult{err: err}
				}
			}
			if err := m.emitToolCalls(iterCtx, state, message.ToolCalls); err != nil {
				return runResult{err: err}
			}
		case schema.Tool:
			if err := m.emitToolResult(iterCtx, state, message); err != nil {
				return runResult{err: err}
			}
		}
	}
}

func controlledResult(state *runState, steps int) runResult {
	reason := state.stopReason()
	if reason == "" {
		reason = "用户取消"
	}
	return runResult{answer: reason, reason: reason, steps: steps}
}

func (m *Manager) waitIdle(ctx context.Context, tabID string) {
	deadline, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	ticker := time.NewTicker(m.deps.PollInterval)
	defer ticker.Stop()
	for {
		screen, err := m.deps.Snapshot(deadline, tabID)
		if err == nil && screen.IdleMS >= 300 {
			return
		}
		select {
		case <-deadline.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *Manager) consumeMessageVariant(iterCtx context.Context, state *runState, variant *adk.MessageVariant) (*schema.Message, error) {
	if !variant.IsStreaming {
		return variant.Message, nil
	}
	defer variant.MessageStream.Close()
	var frames []*schema.Message
	progress := make(map[string]int)
	lastProgress := time.Time{}
	for {
		frame, err := variant.MessageStream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		frames = append(frames, frame)
		if err := emitText(iterCtx, state, frame); err != nil {
			return nil, err
		}
		for _, call := range frame.ToolCalls {
			key := call.ID
			if key == "" && call.Index != nil {
				key = fmt.Sprintf("index-%d", *call.Index)
			}
			progress[key] += len(call.Function.Arguments)
			now := time.Now()
			if !lastProgress.IsZero() && now.Sub(lastProgress) < 120*time.Millisecond {
				continue
			}
			lastProgress = now
			if err := state.emit(iterCtx, agent.Event{Type: "toolArgs", Tool: call.Function.Name, Chars: progress[key]}); err != nil {
				return nil, err
			}
		}
	}
	if len(frames) == 0 {
		return nil, nil
	}
	return schema.ConcatMessages(frames)
}

func emitText(iterCtx context.Context, state *runState, message *schema.Message) error {
	if message.ReasoningContent != "" {
		if err := state.emit(iterCtx, agent.Event{Type: "reasoning", Text: message.ReasoningContent}); err != nil {
			return err
		}
	}
	if message.Content != "" {
		if err := state.emit(iterCtx, agent.Event{Type: "delta", Text: message.Content}); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) emitToolCalls(iterCtx context.Context, state *runState, calls []schema.ToolCall) error {
	for _, call := range calls {
		if call.Function.Name == "read_screen" {
			continue
		}
		toolCall := tools.Call{ID: call.ID, Name: call.Function.Name, Args: json.RawMessage(call.Function.Arguments)}
		if toolCall.ID == "" {
			return errors.New("模型返回了空 tool call ID")
		}
		if err := state.emit(iterCtx, agent.Event{Type: "toolCall", ID: toolCall.ID, Name: toolCall.Name, Args: toolCall.Args, Display: tools.DisplayCall(toolCall)}); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) emitToolResult(iterCtx context.Context, state *runState, message *schema.Message) error {
	if message.ToolName == "read_screen" {
		return nil
	}
	var result tools.Output
	if err := json.Unmarshal([]byte(message.Content), &result); err != nil {
		return fmt.Errorf("解析接管工具 %s 结果失败: %w", message.ToolName, err)
	}
	if message.ToolName == "done" {
		state.eino.answer = result.Text
		if result.OK {
			state.eino.reason = "任务完成"
		} else {
			state.eino.reason = "任务失败"
		}
	}
	text, cut := takeoverPrefix(result.Text, 64<<10)
	result.Truncated = result.Truncated || cut
	summary, _ := takeoverPrefix(result.Text, 400)
	return state.emit(iterCtx, agent.Event{Type: "toolResult", ID: message.ToolCallID, OK: result.OK, Summary: summary, Text: text, Truncated: result.Truncated, ExitCode: result.ExitCode, Panic: result.Panic})
}

func (m *Manager) handleInterrupt(iterCtx context.Context, state *runState, contexts []*adk.InterruptCtx) error {
	for i := len(contexts) - 1; i >= 0; i-- {
		context := contexts[i]
		var interaction tools.Interaction
		switch value := context.Info.(type) {
		case tools.Interaction:
			interaction = value
		case *tools.Interaction:
			if value != nil {
				interaction = *value
			}
		default:
			continue
		}
		if interaction.Kind != "confirm" {
			return fmt.Errorf("未知接管 HITL 类型 %s", interaction.Kind)
		}
		state.pendingMu.Lock()
		state.pending = &pending{callID: interaction.CallID, nonce: context.ID}
		state.pendingMu.Unlock()
		args := withNonce(json.RawMessage(interaction.Args), context.ID)
		return state.emit(iterCtx, agent.Event{Type: "confirmRequired", ID: interaction.CallID, Tool: interaction.Tool, Args: args, Nonce: context.ID, Risk: interaction.Risk, Rendered: interaction.Rendered, Reason: interaction.Reason, Preview: interaction.Preview})
	}
	return errors.New("收到无法识别的接管 interrupt")
}

func withNonce(raw json.RawMessage, nonce string) json.RawMessage {
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	if fields == nil {
		fields = map[string]any{}
	}
	fields["confirmationNonce"] = nonce
	encoded, _ := json.Marshal(fields)
	return encoded
}

func takeoverPrefix(value string, limit int) (string, bool) {
	if len(value) <= limit {
		return value, false
	}
	for limit > 0 && !utf8.ValidString(value[:limit]) {
		limit--
	}
	return value[:limit], true
}

type actionExecution struct {
	manager *Manager
	state   *runState
	runtime *takeoverRuntime
}

type TakeoverDoneArgs struct {
	Summary string `json:"summary" jsonschema:"required"`
	Success bool   `json:"success" jsonschema:"required"`
}

func (e *actionExecution) tools() ([]tool.BaseTool, error) {
	readScreen, err := utils.InferTool("read_screen", "读取执行动作时的最新终端屏幕。", func(ctx context.Context, _ tools.EmptyArgs) (tools.Output, error) {
		return tools.Guarded(ctx, "read_screen", func() (tools.Output, error) {
			return e.readScreen(ctx)
		})
	})
	if err != nil {
		return nil, err
	}
	sendKeys, err := utils.InferTool("send_keys", "发送按键；enter 或 <enter> 会真实提交命令。", func(ctx context.Context, input tools.SendKeysArgs) (tools.Output, error) {
		return tools.Guarded(ctx, "send_keys", func() (tools.Output, error) {
			return e.sendKeys(ctx, input)
		})
	})
	if err != nil {
		return nil, err
	}
	waitFor, err := utils.InferTool("wait_for", "等待终端最近输出匹配正则。", func(ctx context.Context, input tools.WaitForArgs) (tools.Output, error) {
		return tools.Guarded(ctx, "wait_for", func() (tools.Output, error) {
			output, err := e.manager.waitFor(ctx, e.state.args.TabID, input.Pattern, input.TimeoutMS)
			return output, err
		})
	})
	if err != nil {
		return nil, err
	}
	done, err := utils.InferTool("done", "结束接管并报告任务是否成功。", func(ctx context.Context, input TakeoverDoneArgs) (tools.Output, error) {
		return tools.Guarded(ctx, "done", func() (tools.Output, error) {
			return tools.Output{OK: input.Success, Text: input.Summary, ExitCode: 0, Plan: input.Summary}, nil
		})
	})
	if err != nil {
		return nil, err
	}
	return []tool.BaseTool{readScreen, sendKeys, waitFor, done}, nil
}

func (e *actionExecution) readScreen(ctx context.Context) (tools.Output, error) {
	screen, err := e.manager.deps.Snapshot(ctx, e.state.args.TabID)
	if err != nil {
		return tools.Fail(err), nil
	}
	return tools.OK(fmt.Sprintf("光标 (%d, %d)，空闲 %dms\n%s", screen.CursorRow, screen.CursorCol, screen.IdleMS, screen.Text)), nil
}

func (e *actionExecution) decide(ctx context.Context, ruling guard.Ruling) guard.Decision {
	if e.manager.deps.Grants != nil {
		return e.manager.deps.Grants.Evaluate(ctx, e.runtime.permission, ruling, e.state.memory, e.state.assetID, "send_keys", e.state.id)
	}
	return guard.Decide(e.runtime.permission, ruling, e.state.memory)
}

func (e *actionExecution) sendKeys(ctx context.Context, input tools.SendKeysArgs) (tools.Output, error) {
	callID := compose.GetToolCallID(ctx)
	if callID == "" {
		return tools.Output{}, errors.New("Eino tool call id is empty")
	}
	encodedArgs, _ := json.Marshal(input)
	if wasInterrupted, _, state := tool.GetInterruptState[tools.InteractionState](ctx); wasInterrupted {
		target, hasData, decision := tool.GetResumeContext[string](ctx)
		if !target || !hasData {
			return tools.Output{}, tool.StatefulInterrupt(ctx, state.Info, state)
		}
		if decision == "deny" {
			return tools.Fail(errors.New("用户拒绝了此操作")), nil
		}
		if decision != "allow" && decision != "allow_session" {
			return tools.Fail(errors.New("确认结果无效，操作未执行")), nil
		}
		screen, err := e.manager.deps.Snapshot(ctx, e.state.args.TabID)
		if err != nil {
			return tools.Fail(err), nil
		}
		buffered, cursor := tools.TerminalInputCursor(screen)
		if buffered != state.TerminalInput || cursor != state.TerminalCursor {
			return tools.Fail(tools.ErrTerminalInputChanged), nil
		}
		if decision == "allow_session" {
			for _, kind := range state.MemoryKinds {
				e.state.memory.Add(kind)
			}
			if len(state.MemoryKinds) == 0 {
				e.state.memory.Add(state.MemoryKind)
			}
		}
		return e.writeKeys(ctx, callID, input)
	}
	if !e.runtime.allowWrite {
		return tools.Fail(ErrWriteDisabled), nil
	}
	screen, err := e.manager.deps.Snapshot(ctx, e.state.args.TabID)
	if err != nil {
		return tools.Fail(err), nil
	}
	terminalInput, cursor := tools.TerminalInputCursor(screen)
	ruling := guard.ClassifySendKeysWithCursor(input.Keys, terminalInput, cursor, input.Enter, e.runtime.permission.DangerRules)
	decision := e.decide(ctx, ruling)
	if decision.Action == guard.ActionDeny {
		return tools.Fail(errors.New("权限策略已拒绝: " + ruling.Reason)), nil
	}
	if decision.Action == guard.ActionAsk {
		info := tools.Interaction{Kind: "confirm", CallID: callID, Tool: "send_keys", Args: string(encodedArgs), Risk: ruling.Risk.String(), Rendered: tools.DisplaySendKeys(tools.Call{ID: callID, Name: "send_keys", Args: encodedArgs}, terminalInput, cursor), Reason: ruling.Reason}
		state := tools.InteractionState{Kind: "confirm", CallID: callID, MemoryKind: ruling.Kind, MemoryKinds: ruling.ApprovalKinds(), TerminalInput: terminalInput, TerminalCursor: cursor, Info: info}
		return tools.Output{}, tool.StatefulInterrupt(ctx, info, state)
	}
	return e.writeKeys(ctx, callID, input)
}

func (e *actionExecution) writeKeys(ctx context.Context, callID string, input tools.SendKeysArgs) (tools.Output, error) {
	encoded, err := tools.EncodeKeys(input.Keys, input.Enter)
	if err != nil {
		return tools.Fail(err), nil
	}
	lock := e.manager.tabLock(e.state.args.TabID)
	lock.Lock()
	defer lock.Unlock()
	e.manager.mu.Lock()
	current := e.manager.owners[e.state.args.TabID] == e.state.owner
	e.manager.mu.Unlock()
	if !current {
		return tools.Output{}, ErrStaleOwnership
	}
	records, err := e.manager.loadExecRecords(ctx, e.state.id)
	if err != nil {
		return tools.Fail(err), nil
	}
	if recorded, ok := records[callID]; ok {
		e.auditWrite(ctx, callID, input, len(encoded), true)
		if recorded.State == execStateDone {
			return recorded.Output, nil
		}
		return tools.Fail(errors.New("相同调用已执行过但结果未知，为避免重复写入未再次发送；请读取屏幕确认终端状态")), nil
	}
	records[callID] = execRecord{State: execStateAttempt}
	if err := e.manager.saveExecRecords(ctx, e.state.id, records); err != nil {
		return tools.Fail(fmt.Errorf("无法记录执行尝试，未写入终端: %w", err)), nil
	}
	if err := e.manager.deps.WriteAI(ctx, e.state.args.TabID, encoded); err != nil {
		return tools.Fail(err), nil
	}
	records[callID] = execRecord{State: execStateDone, Output: tools.OK("已发送")}
	if err := e.manager.saveExecRecords(ctx, e.state.id, records); err != nil {
		return tools.Fail(fmt.Errorf("写入已发生但结果记录失败，请读取屏幕确认: %w", err)), nil
	}
	e.auditWrite(ctx, callID, input, len(encoded), false)
	return tools.OK("已发送"), nil
}

func (e *actionExecution) auditWrite(ctx context.Context, callID string, input tools.SendKeysArgs, keyBytes int, replay bool) {
	if e.manager.deps.Audit == nil {
		return
	}
	entry := tools.AuditEntry{AssetID: e.state.assetID, Kind: "takeover", Payload: map[string]any{
		"tool": "send_keys", "keys": fmt.Sprintf("<redacted:%d bytes>", keyBytes), "enter": input.Enter,
		"tab": e.state.args.TabID, "call_id": callID, "run_id": e.state.id, "replay": replay,
	}}
	if e.manager.deps.TabSession != nil {
		entry.SessionID = e.manager.deps.TabSession(e.state.args.TabID)
	}
	_ = e.manager.deps.Audit(context.WithoutCancel(ctx), entry)
}
