package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Hello-CTF/NexTerm/internal/ai/hitl"
	"github.com/Hello-CTF/NexTerm/internal/ai/memory"
	"github.com/Hello-CTF/NexTerm/internal/ai/provider"
	"github.com/Hello-CTF/NexTerm/internal/ai/subagent"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ai/usage"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type einoRuntime struct {
	runner        *adk.Runner
	input         []*schema.Message
	contextWindow uint64
	mu            sync.Mutex
	turns         int
	total         usage.Usage
	answer        string
}

func (r *einoRuntime) currentTurn() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.turns
}

func (r *einoRuntime) addTurn() {
	r.mu.Lock()
	r.turns++
	r.mu.Unlock()
}

func (r *einoRuntime) setAnswer(answer string) {
	r.mu.Lock()
	r.answer = answer
	r.mu.Unlock()
}

func (r *einoRuntime) addUsage(value usage.Usage) {
	r.mu.Lock()
	r.total.Accumulate(value)
	r.mu.Unlock()
}

func (r *einoRuntime) summary() (string, int, usage.Usage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.answer, r.turns, r.total
}

func (r *einoRuntime) failure(err error) (string, int, usage.Usage, error) {
	_, turns, total := r.summary()
	return "", turns, total, err
}

type memoryCheckpoints struct {
	mu     sync.RWMutex
	values map[string][]byte
}

func NewMemoryCheckpoints() *memoryCheckpoints {
	return &memoryCheckpoints{values: make(map[string][]byte)}
}

func (s *memoryCheckpoints) Get(_ context.Context, id string) ([]byte, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.values[id]
	return append([]byte(nil), value...), ok, nil
}

func (s *memoryCheckpoints) Set(_ context.Context, id string, value []byte) error {
	s.mu.Lock()
	s.values[id] = append([]byte(nil), value...)
	s.mu.Unlock()
	return nil
}

func (s *memoryCheckpoints) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	delete(s.values, id)
	s.mu.Unlock()
	return nil
}

func (r *Runner) runJob(current *job) {
	var answer string
	var turns int
	var total usage.Usage
	var runErr error
	defer func() {
		if recovered := recover(); recovered != nil {
			runErr = fmt.Errorf("AI 任务内部错误: %v", recovered)
		}
		if errors.Is(runErr, errRunPaused) {
			r.parkOrHandoff(current)
			return
		}
		r.complete(current, answer, turns, total, runErr)
	}()
	answer, turns, total, runErr = r.run(current)
}

func (r *Runner) consumeResumed(current *job, iterator *adk.AsyncIterator[*adk.AgentEvent]) {
	var answer string
	var turns int
	var total usage.Usage
	var runErr error
	defer func() {
		if recovered := recover(); recovered != nil {
			runErr = fmt.Errorf("AI 任务内部错误: %v", recovered)
		}
		if errors.Is(runErr, errRunPaused) {
			r.parkOrHandoff(current)
			return
		}
		if runErr == nil {
			if err := r.persistAssistant(current.ctx, current.args.ConversationID, answer, total); err != nil {
				runErr = err
			}
		}
		r.complete(current, answer, turns, total, runErr)
	}()
	answer, turns, total, runErr = r.consume(current, iterator)
}

var parkOrHandoffTestHook func()

func (r *Runner) parkOrHandoff(current *job) {
	current.pendingMu.Lock()
	if current.ctx.Err() != nil {
		current.running = false
		current.pendingMu.Unlock()
		r.complete(current, "", 0, usage.Usage{}, current.ctx.Err())
		return
	}
	iterator := current.resumeIterator
	current.resumeIterator = nil
	if parkOrHandoffTestHook != nil {
		parkOrHandoffTestHook()
	}
	if iterator != nil {
		current.pendingMu.Unlock()
		go r.consumeResumed(current, iterator)
		return
	}
	current.running = false
	current.pendingMu.Unlock()
}

func (r *Runner) watchHITL(current *job) {
	done, err := r.hitl.Done(current.id)
	if err != nil {
		return
	}
	select {
	case <-done:
		snapshot, err := r.hitl.Snapshot(current.id)
		if err != nil || snapshot.Terminal == nil {
			return
		}
		r.complete(current, "", 0, usage.Usage{}, hitlTerminalError(snapshot.Terminal))
	case <-current.ctx.Done():
	}
}

func hitlTerminalError(terminal *hitl.Event) error {
	switch terminal.Reason {
	case hitl.TerminalExpired:
		return errors.New("AI 确认请求已过期，请重新发送")
	case hitl.TerminalFailed:
		if terminal.Message != "" {
			return errors.New(terminal.Message)
		}
		return errors.New("AI 任务失败")
	case hitl.TerminalCanceled:
		return context.Canceled
	default:
		return errors.New("AI 任务已结束")
	}
}

func (r *Runner) run(current *job) (string, int, usage.Usage, error) {
	if current.eino == nil {
		if err := r.initializeEino(current); err != nil {
			return "", 0, usage.Usage{}, err
		}
	}
	runtime := current.eino
	cancelOption, cancelFn := adk.WithCancel()
	current.pendingMu.Lock()
	current.cancelFn = cancelFn
	current.pendingMu.Unlock()
	iterator := runtime.runner.Run(current.ctx, runtime.input, cancelOption, adk.WithCheckPointID(current.id))
	answer, turns, total, err := r.consume(current, iterator)
	if err == nil {
		if err := r.persistAssistant(current.ctx, current.args.ConversationID, answer, total); err != nil {
			return "", turns, total, err
		}
	}
	return answer, turns, total, err
}

func (r *Runner) initializeEino(current *job) error {
	rows, err := r.store.MsgList(current.ctx, current.args.ConversationID)
	if err != nil {
		return err
	}
	if r.config.Model == nil || r.config.Tools == nil {
		return errors.New("Eino ChatModel 或工具注册表未配置")
	}
	permission, err := r.config.Permission(current.ctx)
	if err != nil {
		return err
	}
	permission = permission.Normalized()
	chatModel, contextWindow, err := r.modelFor(current)
	if err != nil {
		return err
	}
	if contextWindow == 0 {
		contextWindow = 32768
	}
	runtime := &einoRuntime{contextWindow: contextWindow}
	compaction := newCompactionHandler(chatModel, contextWindow, func() {
		current.queueEmits(statusEvent("compacting", runtime.currentTurn()))
	})
	messages := historyMessages(rows, current.id)
	messages, err = compaction.compactHistory(current.ctx, messages)
	if err != nil {
		return err
	}
	if r.config.Context != nil {
		bundle := r.config.Context.Build(current.ctx, current.args.Scope, current.args.Selection)
		if bundle.Volatile != "" {
			messages = append(messages, schema.UserMessage("[环境上下文]\n"+bundle.Volatile))
		}
	}
	messages = append(messages, userMessage(current.args))
	if r.config.Memory != nil {

		injection, err := r.config.Memory.Inject(current.ctx, r.config.MemoryScope, messages, memory.Selection{}, memory.Budget{})
		if err != nil {
			return err
		}
		messages = injection.Messages
	}
	subagents := r.config.Subagents
	if subagents != nil {
		cloned := *subagents
		if r.runs != nil {
			cloned.Runs = r.runs
		}
		cloned.ConversationID = current.args.ConversationID
		if cloned.ActiveProfileID == nil {
			cloned.ActiveProfileID = func() string { return r.profileIDFor(ChatArgs{}) }
		}
		subagents = &cloned
	}
	execution := &tools.Execution{JobID: current.id, ConversationID: current.args.ConversationID, Registry: r.config.Tools, Scope: current.args.Scope, Permission: permission, Memory: current.memory, PlanMode: current.args.PlanMode, Subagents: subagents, SubagentEvents: func(ctx context.Context, parentCallID string, depth int, event subagent.Event) {
		if event.Kind == "" {
			return
		}
		_ = current.emit(context.WithoutCancel(ctx), mapSubagentEvent(parentCallID, depth, event))
	}}
	einoTools, err := execution.Tools()
	if err != nil {
		return err
	}
	current.pendingMu.Lock()
	current.subagents = execution.SubagentManager
	completed := current.completed
	current.pendingMu.Unlock()
	if completed && execution.SubagentManager != nil {
		if err := execution.SubagentManager.Close(); err != nil {
			slog.Warn("close subagent manager after completed run failed", "error", err)
		}
	}
	memoryTools, err := r.memoryTools(current.ctx, current.args.PlanMode)
	if err != nil {
		return err
	}
	einoTools = append(einoTools, memoryTools...)
	runtime.input = messages
	instruction := systemPrompt()
	returnDirectly := map[string]bool{}
	if current.args.PlanMode {
		instruction = planSystemPrompt()
		returnDirectly["exit_plan_mode"] = true
	}
	chatAgent, err := adk.NewChatModelAgent(current.ctx, &adk.ChatModelAgentConfig{
		Name: "nexterm-ai", Description: "NexTerm 运维助手", Instruction: instruction, Model: chatModel, MaxIterations: r.config.MaxTurns,
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: einoTools, ExecuteSequentially: true}, ReturnDirectly: returnDirectly},
		Middlewares: []adk.AgentMiddleware{{BeforeChatModel: func(_ context.Context, state *adk.ChatModelAgentState) error {

			var pending []Event
			for _, steered := range current.steer.Drain() {
				state.Messages = append(state.Messages, steered)
				pending = append(pending, Event{Type: "steered", Text: steered.Content})
			}
			pending = append(pending, statusEvent("thinking", runtime.currentTurn()))
			current.queueTurnEmits(pending...)
			return nil
		}}},
		Handlers: []adk.ChatModelAgentMiddleware{compaction},
	})
	if err != nil {
		return err
	}
	runtime.runner = adk.NewRunner(current.ctx, adk.RunnerConfig{Agent: chatAgent, EnableStreaming: true, CheckPointStore: r.checkpoints})
	current.eino = runtime
	return nil
}

func (r *Runner) modelFor(current *job) (model.BaseChatModel, uint64, error) {
	if current.args.ModelProfileID != "" {
		if r.config.ModelForProfile == nil {
			return nil, 0, errors.New("按档案选择 AI 模型未配置")
		}
		return r.config.ModelForProfile(current.ctx, current.args.ModelProfileID)
	}
	return r.config.Model(current.ctx)
}

func userMessage(args ChatArgs) *schema.Message {
	if len(args.Images) == 0 {
		return schema.UserMessage(args.Message)
	}
	parts := []schema.MessageInputPart{{Type: schema.ChatMessagePartTypeText, Text: args.Message}}
	for _, image := range args.Images {
		mime, encoded := imageParts(image)
		parts = append(parts, schema.MessageInputPart{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{Base64Data: &encoded, MIMEType: mime}}})
	}
	return &schema.Message{Role: schema.User, UserInputMultiContent: parts}
}

func imageParts(image string) (string, string) {
	if index := strings.Index(image, ";base64,"); index >= 0 {
		mime := strings.TrimPrefix(image[:index], "data:")
		return mime, image[index+8:]
	}
	return "image/png", image
}

func (r *Runner) flushPendingEvents(current *job, turn uint64, bestEffort bool) error {
	pending := current.drainEmitsUpTo(turn)
	if len(pending) == 0 {
		return nil
	}
	return r.emitPendingEvents(current, pending, bestEffort)
}

func (r *Runner) flushAllPendingEvents(current *job, bestEffort bool) error {
	pending := current.drainAllEmits()
	if len(pending) == 0 {
		return nil
	}
	return r.emitPendingEvents(current, pending, bestEffort)
}

func (r *Runner) emitPendingEvents(current *job, pending []Event, bestEffort bool) error {
	ctx := current.ctx
	if bestEffort {
		parent := current.deliveryCtx
		if parent == nil {
			parent = context.WithoutCancel(current.ctx)
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(parent, 5*time.Second)
		defer cancel()
	}
	for _, event := range pending {
		if err := current.emit(ctx, event); err != nil {
			if bestEffort {
				return nil
			}
			return err
		}
	}
	return nil
}

func (r *Runner) consume(current *job, iterator *adk.AsyncIterator[*adk.AgentEvent]) (string, int, usage.Usage, error) {
	runtime := current.eino

	defer func() { _ = r.flushAllPendingEvents(current, true) }()
	for {
		event, ok := iterator.Next()
		if !ok {
			answer, turns, total := runtime.summary()
			return answer, turns, total, nil
		}
		if event.Err != nil {
			if errors.Is(event.Err, adk.ErrExceedMaxIterations) {
				return runtime.failure(&maxIterationsError{limit: r.config.MaxTurns})
			}
			return runtime.failure(event.Err)
		}
		if event.Action != nil && event.Action.Interrupted != nil {
			if err := r.handleInterrupt(current, event.Action.Interrupted.InterruptContexts); err != nil {
				return runtime.failure(err)
			}
			return runtime.failure(errRunPaused)
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		variant := event.Output.MessageOutput
		started := time.Time{}
		if variant.Role == schema.Assistant {
			started = time.Now()
			current.assistantTurn++
			if err := r.flushPendingEvents(current, current.assistantTurn, false); err != nil {
				return runtime.failure(err)
			}
		}
		message, err := r.consumeMessageVariant(current, variant)
		if err != nil {
			if message != nil && variant.Role == schema.Assistant {
				latency := time.Duration(0)
				if !started.IsZero() {
					latency = time.Since(started)
				}
				_ = r.emitUsage(current, message, latency)
			}
			return runtime.failure(err)
		}
		if message == nil {
			continue
		}
		switch variant.Role {
		case schema.Assistant:
			runtime.addTurn()
			runtime.setAnswer(message.Content)
			if !variant.IsStreaming {
				if err := emitAssistantText(current, message); err != nil {
					return runtime.failure(err)
				}
			}
			latency := time.Duration(0)
			if !started.IsZero() {
				latency = time.Since(started)
			}
			if err := r.emitUsage(current, message, latency); err != nil {
				return runtime.failure(err)
			}
			if err := r.emitToolCalls(current, message.ToolCalls); err != nil {
				return runtime.failure(err)
			}
			if len(message.ToolCalls) > 0 {
				if err := r.persistToolCallRows(current.ctx, current.args.ConversationID, current.id, message.ToolCalls); err != nil {
					if ctxErr := current.ctx.Err(); ctxErr != nil {
						return runtime.failure(ctxErr)
					}
					return runtime.failure(err)
				}
			}
		case schema.Tool:
			result, text, err := r.emitToolResult(current, message)
			if err != nil {
				return runtime.failure(err)
			}
			if err := r.persistToolResultRows(current.ctx, current, message, result, text); err != nil {
				if ctxErr := current.ctx.Err(); ctxErr != nil {
					return runtime.failure(ctxErr)
				}
				return runtime.failure(err)
			}
		}
	}
}

func (r *Runner) consumeMessageVariant(current *job, variant *adk.MessageVariant) (*schema.Message, error) {
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
			if len(frames) == 0 {
				return nil, err
			}
			message, concatErr := schema.ConcatMessages(frames)
			if concatErr != nil {
				return nil, err
			}
			return message, err
		}
		frames = append(frames, frame)
		if err := emitAssistantText(current, frame); err != nil {
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
			if err := current.emit(current.ctx, Event{Type: "toolArgs", Tool: call.Function.Name, Chars: progress[key]}); err != nil {
				return nil, err
			}
		}
	}
	if len(frames) == 0 {
		return nil, nil
	}
	return schema.ConcatMessages(frames)
}

func emitAssistantText(current *job, message *schema.Message) error {
	if message.ReasoningContent != "" {
		if err := current.emit(current.ctx, Event{Type: "reasoning", Text: message.ReasoningContent}); err != nil {
			return err
		}
	}
	if message.Content != "" {
		if err := current.emit(current.ctx, Event{Type: "delta", Text: message.Content}); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) emitUsage(current *job, message *schema.Message, latency time.Duration) error {
	runtime := current.eino
	if message.ResponseMeta == nil || message.ResponseMeta.Usage == nil {
		return nil
	}
	value := message.ResponseMeta.Usage
	window := runtime.contextWindow
	if configured, ok := message.Extra[provider.MessageExtraContextWindow].(uint64); ok && configured > 0 {
		window = configured
	}
	currentUsage := usage.Usage{
		PromptTokens:     uint64(max(value.PromptTokens, 0)),
		CompletionTokens: uint64(max(value.CompletionTokens, 0)),
		CachedTokens:     uint64(max(value.PromptTokenDetails.CachedTokens, 0)),
		ContextWindow:    window,
	}
	if raw, ok := message.Extra[provider.MessageExtraCacheCreation]; ok {
		switch typed := raw.(type) {
		case uint64:
			currentUsage.CacheCreationTokens = typed
		case float64:
			currentUsage.CacheCreationTokens = uint64(max(typed, 0))
		case int:
			currentUsage.CacheCreationTokens = uint64(max(typed, 0))
		}
	}
	if latency > 0 {
		currentUsage.LatencyMS = latency.Milliseconds()
	}
	runtime.addUsage(currentUsage)
	modelName, _ := message.Extra["model"].(string)
	runID, _ := message.Extra["run_id"].(string)
	callID, _ := message.Extra["call_id"].(string)
	return current.emit(current.ctx, Event{Type: "usage", Model: modelName, RunID: runID, CallID: callID, PromptTokens: currentUsage.PromptTokens, CompletionTokens: currentUsage.CompletionTokens, CachedTokens: currentUsage.CachedTokens, CacheCreationTokens: currentUsage.CacheCreationTokens, LatencyMS: currentUsage.LatencyMS, ContextWindow: window})
}

func (r *Runner) emitToolCalls(current *job, calls []schema.ToolCall) error {
	for _, call := range calls {
		toolCall := tools.Call{ID: call.ID, Name: call.Function.Name, Args: json.RawMessage(call.Function.Arguments)}
		if toolCall.ID == "" {
			return errors.New("模型返回了空 tool call ID")
		}
		if err := current.emit(current.ctx, Event{Type: "toolCall", ID: toolCall.ID, Name: toolCall.Name, Args: toolCall.Args, Display: tools.DisplayCall(toolCall)}); err != nil {
			return err
		}
	}
	return nil
}

func mapSubagentEvent(parentCallID string, depth int, event subagent.Event) Event {
	mapped := Event{ParentCallID: parentCallID, SubagentID: event.TaskID, Depth: depth}
	switch event.Kind {
	case subagent.EventDelta:
		mapped.Type, mapped.Text = "subagentDelta", event.Text
	case subagent.EventToolCall:
		mapped.Type, mapped.ID, mapped.Name = "subagentToolCall", event.CallID, event.Name
	case subagent.EventToolResult:
		mapped.Type, mapped.ID, mapped.OK, mapped.Summary, mapped.Text = "subagentToolResult", event.CallID, event.OK, event.Summary, event.Text
		mapped.Truncated, mapped.ExitCode, mapped.Panic = event.Truncated, event.ExitCode, event.Panic
	case subagent.EventDone:
		mapped.Type, mapped.Status, mapped.Summary, mapped.Message = "subagentDone", string(event.Status), event.Summary, event.Err
	}
	return mapped
}

func (r *Runner) emitToolResult(current *job, message *schema.Message) (tools.Output, string, error) {
	var result tools.Output
	if err := json.Unmarshal([]byte(message.Content), &result); err != nil {
		return result, "", fmt.Errorf("解析领域工具 %s 结果失败: %w", message.ToolName, err)
	}
	if result.Change != nil {
		if err := current.emit(current.ctx, Event{Type: "fileChange", ID: result.Change.ID, Path: result.Change.Path, Before: result.Change.Before, After: result.Change.After}); err != nil {
			return result, "", err
		}
	}
	text, cut := prefixBytes(result.Text, persistedToolResultLimit)
	result.Truncated = result.Truncated || cut
	if err := current.emit(current.ctx, Event{Type: "toolResult", ID: message.ToolCallID, OK: result.OK, Summary: summarize(result.Text), Text: text, Truncated: result.Truncated, ExitCode: result.ExitCode, Panic: result.Panic}); err != nil {
		return result, "", err
	}
	if result.Todos != nil {
		if err := current.emit(current.ctx, Event{Type: "todos", Items: result.Todos}); err != nil {
			return result, "", err
		}
	}
	if result.Plan != "" && current.args.PlanMode {
		if err := current.emit(current.ctx, Event{Type: "planSubmitted", Plan: result.Plan}); err != nil {
			return result, "", err
		}
		current.eino.setAnswer(result.Plan)
	}
	return result, text, nil
}

func (r *Runner) handleInterrupt(current *job, contexts []*adk.InterruptCtx) error {
	for i := len(contexts) - 1; i >= 0; i-- {
		context := contexts[i]
		interaction, ok := interactionValue(context.Info)
		if !ok {
			continue
		}
		kind, question, err := hitlInteraction(interaction)
		if err != nil {
			return err
		}
		request, err := r.hitl.Interrupt(current.ctx, context, hitl.InterruptInput{
			RunID:        current.id,
			CheckpointID: current.id,
			CallID:       interaction.CallID,
			Tool:         interaction.Tool,
			Kind:         kind,
			Parameters:   json.RawMessage(interaction.Args),
			Question:     question,
		})
		if err != nil {
			return err
		}
		r.updateRunStatus(current, store.RunStatusInterrupted)
		args := hitl.WithNonce(json.RawMessage(interaction.Args), request.Nonce)
		switch kind {
		case hitl.KindConfirm:
			if err := current.emit(current.ctx, Event{Type: "confirmRequired", ID: interaction.CallID, Tool: interaction.Tool, Args: args, Nonce: request.Nonce, Risk: interaction.Risk, Rendered: interaction.Rendered, Reason: interaction.Reason, Preview: interaction.Preview, Commands: interaction.Commands, RulePattern: interaction.RulePattern, RequestID: request.ID, Attempt: request.Attempt}); err != nil {
				return err
			}
		case hitl.KindQuestion:
			if err := current.emit(current.ctx, Event{Type: "questionRequired", ID: interaction.CallID, Nonce: request.Nonce, Question: interaction.Question, RequestID: request.ID, Attempt: request.Attempt}); err != nil {
				return err
			}
		}
		return nil
	}
	return errors.New("收到无法识别的 Eino interrupt")
}

func hitlInteraction(interaction tools.Interaction) (hitl.Kind, *hitl.Question, error) {
	switch interaction.Kind {
	case "confirm":
		return hitl.KindConfirm, nil, nil
	case "question":
		if interaction.Question == nil {
			return "", nil, fmt.Errorf("未知 HITL 类型 %s", interaction.Kind)
		}
		return hitl.KindQuestion, &hitl.Question{Text: interaction.Question.Question, Options: interaction.Question.Options}, nil
	default:
		return "", nil, fmt.Errorf("未知 HITL 类型 %s", interaction.Kind)
	}
}

func interactionValue(value any) (tools.Interaction, bool) {
	switch typed := value.(type) {
	case tools.Interaction:
		return typed, true
	case *tools.Interaction:
		if typed != nil {
			return *typed, true
		}
	}
	return tools.Interaction{}, false
}

func (r *Runner) persistAssistant(ctx context.Context, conversationID, answer string, total usage.Usage) error {
	tokensIn := clampTokensInt64(total.PromptTokens)
	tokensOut := clampTokensInt64(total.CompletionTokens)
	return r.store.MsgInsert(ctx, conversationID, "assistant", map[string]any{"role": "assistant", "content": answer}, &tokensIn, &tokensOut)
}

func clampTokensInt64(value uint64) int64 {
	return int64(min64(value, uint64(^uint64(0)>>1)))
}

func fitMessageBudget(messages []*schema.Message, window uint64) ([]*schema.Message, bool, error) {
	if window == 0 {
		return messages, false, nil
	}
	messages = append([]*schema.Message(nil), messages...)
	limit := int(window * 3)
	compacted := false
	for estimatedMessages(messages) > limit {
		changed := false
		kept := 0
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role != schema.Tool {
				continue
			}
			kept++
			if kept > 4 && messages[i].Content != "[较早的工具输出已省略]" {
				messages[i].Content = "[较早的工具输出已省略]"
				compacted = true
				changed = true
				break
			}
		}
		if !changed {
			break
		}
	}
	for estimatedMessages(messages) > limit && len(messages) > 2 {
		var dropped bool
		messages, dropped = dropOldestMessageUnit(messages)
		if !dropped {
			break
		}
		compacted = true
	}
	if estimatedMessages(messages) > limit {
		return nil, compacted, fmt.Errorf("当前输入与系统上下文超过模型预算（%d > %d 字节），请缩短输入或提高 contextWindow", estimatedMessages(messages), limit)
	}
	return messages, compacted, nil
}

func dropOldestMessageUnit(messages []*schema.Message) ([]*schema.Message, bool) {
	start := 0
	for start < len(messages) && messages[start].Role == schema.System {
		start++
	}
	latestUser := -1
	for index := len(messages) - 1; index >= start; index-- {
		if messages[index].Role == schema.User {
			latestUser = index
			break
		}
	}
	for index := start; index < len(messages); index++ {
		if messages[index].Role == schema.User && index == latestUser {
			continue
		}
		if messages[index].Role == schema.Assistant && len(messages[index].ToolCalls) > 0 {
			end := index + 1
			for end < len(messages) && messages[end].Role == schema.Tool {
				end++
			}
			return append(messages[:index], messages[end:]...), true
		}
		if messages[index].Role == schema.Tool && index > start && messages[index-1].Role == schema.Assistant && len(messages[index-1].ToolCalls) > 0 {
			start := index - 1
			end := index + 1
			for end < len(messages) && messages[end].Role == schema.Tool {
				end++
			}
			return append(messages[:start], messages[end:]...), true
		}
		return append(messages[:index], messages[index+1:]...), true
	}
	return messages, false
}

func toolMessagesPaired(messages []*schema.Message) bool {
	for i, message := range messages {
		if message.Role == schema.Tool && (i == 0 || len(messages[i-1].ToolCalls) == 0) {
			return false
		}
	}
	return true
}

func estimatedMessages(messages []*schema.Message) int {
	total := 0
	for _, message := range messages {
		total += len(message.Content) + len(message.ReasoningContent)
		for _, call := range message.ToolCalls {
			total += len(call.Function.Arguments)
		}
		if len(message.UserInputMultiContent) != 0 {
			encoded, _ := json.Marshal(message.UserInputMultiContent)
			total += len(encoded)
		}
	}
	return total
}

func summarize(text string) string {
	if len(text) <= 400 {
		return text
	}
	trimmed, _ := prefixBytes(text, 400)
	return trimmed + "…"
}

func prefixBytes(text string, limit int) (string, bool) {
	if len(text) <= limit {
		return text, false
	}
	for limit > 0 && !utf8.ValidString(text[:limit]) {
		limit--
	}
	return text[:limit], true
}

func min64(value, maximum uint64) uint64 {
	if value > maximum {
		return maximum
	}
	return value
}
