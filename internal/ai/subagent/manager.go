package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/provider"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/usage"
)

type Manager struct {
	config Config

	mu             sync.Mutex
	tasks          map[string]*task
	finishErrs     []error
	active         int
	nextGeneration uint64
	closed         bool
	wg             sync.WaitGroup
}

type task struct {
	handle   Handle
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	recorder *historyRecorder
	observer Observer

	result   Result
	err      error
	finished bool
}

type childContextKey struct{}

type childContextValue struct {
	manager    *Manager
	generation uint64
}

func NewManager(config Config) (*Manager, error) {
	if config.NewModel == nil {
		return nil, errors.New("subagent model factory is required")
	}
	if config.MaxIterations < 0 || config.MaxRunTime < 0 || config.MaxTasks < 0 || config.MaxConcurrent < 0 || config.MaxOutputBytes < 0 || config.MaxHistoryMessages < 0 || config.MaxHistoryBytes < 0 {
		return nil, errors.New("subagent limits cannot be negative")
	}
	if config.MaxIterations == 0 {
		config.MaxIterations = 8
	}
	if config.MaxRunTime == 0 {
		config.MaxRunTime = 2 * time.Minute
	}
	if config.MaxTasks == 0 {
		config.MaxTasks = 64
	}
	if config.MaxConcurrent == 0 {
		config.MaxConcurrent = 4
	}
	if config.MaxConcurrent > config.MaxTasks {
		config.MaxConcurrent = config.MaxTasks
	}
	if config.MaxOutputBytes == 0 {
		config.MaxOutputBytes = 64 << 10
	}
	if config.MaxHistoryMessages == 0 {
		config.MaxHistoryMessages = 64
	}
	if config.MaxHistoryBytes == 0 {
		config.MaxHistoryBytes = 256 << 10
	}
	return &Manager{config: config, tasks: make(map[string]*task)}, nil
}

func (m *Manager) Spawn(ctx context.Context, request Request) (Handle, error) {
	if _, nested := ctx.Value(childContextKey{}).(childContextValue); nested {
		return Handle{}, ErrNestedSpawn
	}
	if strings.TrimSpace(request.Task) == "" {
		return Handle{}, errors.New("subagent task cannot be empty")
	}
	if request.ModelProfileID != "" && m.config.NewModelForProfile == nil {
		return Handle{}, errors.New("subagent model profile selection is not configured")
	}
	if request.ModelProfileID == "" && m.config.NewModelForProfile != nil && m.config.ResolveDefaultProfileID != nil {
		request.ModelProfileID = m.config.ResolveDefaultProfileID()
	}
	if request.Timeout < 0 || request.Timeout > m.config.MaxRunTime {
		return Handle{}, fmt.Errorf("subagent timeout must be between 0 and %s", m.config.MaxRunTime)
	}
	scope, allowed, err := normalizeScope(request.Scope)
	if err != nil {
		return Handle{}, err
	}
	if err := ctx.Err(); err != nil {
		return Handle{}, err
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Handle{}, ErrManagerClosed
	}
	if m.active >= m.config.MaxConcurrent {
		m.mu.Unlock()
		return Handle{}, ErrTooManyActive
	}
	if current := m.tasks[request.ID]; current != nil && request.ID != "" {
		if !current.finished {
			m.mu.Unlock()
			return Handle{}, ErrTaskExists
		}
		delete(m.tasks, request.ID)
	}
	if err := m.makeRoomLocked(); err != nil {
		m.mu.Unlock()
		return Handle{}, err
	}
	m.nextGeneration++
	generation := m.nextGeneration
	if request.ID == "" {
		request.ID = fmt.Sprintf("subagent-%d", generation)
	}
	handle := Handle{ID: request.ID, Generation: generation}
	timeout := request.Timeout
	if timeout == 0 {
		timeout = m.config.MaxRunTime
	}
	childCtx := context.WithValue(ctx, childContextKey{}, childContextValue{manager: m, generation: generation})
	childCtx, cancel := context.WithTimeout(childCtx, timeout)
	current := &task{
		handle:   handle,
		ctx:      childCtx,
		cancel:   cancel,
		done:     make(chan struct{}),
		recorder: newHistoryRecorder(request.Task, m.config.MaxHistoryMessages, m.config.MaxHistoryBytes),
		observer: request.Observer,
		result:   Result{Handle: handle, Status: StatusRunning},
	}
	m.tasks[handle.ID] = current
	m.active++
	m.wg.Add(1)
	m.mu.Unlock()

	go m.run(current, request, scope, allowed)
	return handle, nil
}

func (m *Manager) makeRoomLocked() error {
	for len(m.tasks) >= m.config.MaxTasks {
		var oldest *task
		for _, candidate := range m.tasks {
			if candidate.finished && (oldest == nil || candidate.handle.Generation < oldest.handle.Generation) {
				oldest = candidate
			}
		}
		if oldest == nil {
			return ErrRegistryFull
		}
		delete(m.tasks, oldest.handle.ID)
	}
	return nil
}

func (m *Manager) Cancel(handle Handle) error {
	m.mu.Lock()
	current, err := m.lookupLocked(handle)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	if current.finished {
		m.mu.Unlock()
		return ErrTaskFinished
	}
	cancel := current.cancel
	m.mu.Unlock()
	cancel()
	return nil
}

func (m *Manager) Wait(ctx context.Context, handle Handle) (Result, error) {
	m.mu.Lock()
	current, err := m.lookupLocked(handle)
	m.mu.Unlock()
	if err != nil {
		return Result{}, err
	}
	select {
	case <-current.done:
		m.mu.Lock()
		defer m.mu.Unlock()
		return current.result.clone(), current.err
	case <-ctx.Done():
		select {
		case <-current.done:
		case <-time.After(terminalWaitTimeout):
			m.mu.Lock()
			defer m.mu.Unlock()
			return current.result.clone(), errors.Join(ctx.Err(), fmt.Errorf("%w: %s", ErrTerminalWaitTimeout, handle.ID))
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		return current.result.clone(), current.err
	}
}

const terminalWaitTimeout = 10 * time.Second

var ErrTerminalWaitTimeout = errors.New("subagent terminal state wait timed out")

func (m *Manager) Snapshot(handle Handle) (Result, error) {
	m.mu.Lock()
	current, err := m.lookupLocked(handle)
	if err != nil {
		m.mu.Unlock()
		return Result{}, err
	}
	result := current.result.clone()
	m.mu.Unlock()
	history, truncated := current.recorder.snapshot()
	result.History = history
	result.HistoryTruncated = result.HistoryTruncated || truncated
	return result, nil
}

func (m *Manager) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.tasks)
}

func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		m.wg.Wait()
		return m.finishErrors()
	}
	m.closed = true
	cancels := make([]context.CancelFunc, 0, m.active)
	for _, current := range m.tasks {
		if !current.finished {
			cancels = append(cancels, current.cancel)
		}
	}
	m.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	m.wg.Wait()
	return m.finishErrors()
}

func (m *Manager) finishErrors() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return errors.Join(m.finishErrs...)
}

func (m *Manager) lookupLocked(handle Handle) (*task, error) {
	current := m.tasks[handle.ID]
	if current == nil {
		return nil, ErrTaskNotFound
	}
	if handle.Generation == 0 || current.handle.Generation != handle.Generation {
		return nil, ErrStaleGeneration
	}
	return current, nil
}

func (m *Manager) run(current *task, request Request, scope Scope, allowed map[string]struct{}) {
	defer m.wg.Done()
	output, turns, total, err := m.execute(current, request, scope, allowed)
	current.recorder.discardIncomplete()
	status := StatusCompleted
	if err != nil {
		status = StatusFailed
		if errors.Is(err, context.Canceled) {
			status = StatusCanceled
		}
	}
	capped, truncated := capText(output, m.config.MaxOutputBytes)
	history, historyTruncated := current.recorder.snapshot()
	result := Result{
		Handle:           current.handle,
		Status:           status,
		Output:           capped,
		OutputTruncated:  truncated,
		History:          history,
		HistoryTruncated: historyTruncated,
		Turns:            turns,
		Usage:            total,
	}
	if err != nil {
		result.Error = err.Error()
	}
	current.emit(Event{Kind: EventDone, Status: status, Summary: summarizeText(capped), Err: result.Error})

	m.mu.Lock()
	current.result = result
	current.err = err
	current.finished = true
	if registered := m.tasks[current.handle.ID]; registered == current && registered.handle.Generation == current.handle.Generation {
		m.active--
	}
	m.mu.Unlock()

	var finishErr error
	if m.config.OnFinish != nil {
		finishErr = m.config.OnFinish(context.WithoutCancel(current.ctx), request, result)
	}

	m.mu.Lock()
	if finishErr != nil {
		m.finishErrs = append(m.finishErrs, fmt.Errorf("subagent %s: %w", current.handle.ID, finishErr))
	}
	if current.err == nil && (m.closed || errors.Is(current.ctx.Err(), context.Canceled)) {
		current.err = context.Canceled
		current.result.Status = StatusCanceled
		current.result.Error = context.Canceled.Error()
	}
	current.cancel()
	close(current.done)
	m.mu.Unlock()
}

func (m *Manager) execute(current *task, request Request, scope Scope, allowed map[string]struct{}) (string, int, usage.Usage, error) {
	chatModel, err := m.modelFor(current, request)
	if err != nil {
		return "", 0, usage.Usage{}, err
	}
	if chatModel == nil {
		return "", 0, usage.Usage{}, errors.New("subagent model factory returned nil")
	}
	tools, err := m.scopedTools(current.ctx, scope, allowed)
	if err != nil {
		return "", 0, usage.Usage{}, err
	}
	instruction := strings.TrimSpace(strings.Join([]string{m.config.Instruction, request.Persona}, "\n"))
	agent, err := adk.NewChatModelAgent(current.ctx, &adk.ChatModelAgentConfig{
		Name:          current.handle.ID,
		Description:   "Bounded isolated task",
		Instruction:   instruction,
		Model:         chatModel,
		MaxIterations: m.config.MaxIterations,
		ToolsConfig:   adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: tools, ExecuteSequentially: true}},
		Middlewares: []adk.AgentMiddleware{{BeforeChatModel: func(_ context.Context, state *adk.ChatModelAgentState) error {
			visible := make([]*schema.ToolInfo, 0, len(allowed))
			for _, info := range state.ToolInfos {
				if _, ok := allowed[info.Name]; ok {
					visible = append(visible, info)
				}
			}
			state.ToolInfos = visible
			return nil
		}}},
	})
	if err != nil {
		return "", 0, usage.Usage{}, err
	}
	runner := adk.NewRunner(current.ctx, adk.RunnerConfig{Agent: agent, EnableStreaming: true})
	iterator := runner.Run(current.ctx, []*schema.Message{schema.UserMessage(request.Task)})
	var output string
	var turns int
	var total usage.Usage
	var firstErr error
	var forcedErr error
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		if event == nil {
			continue
		}
		if event.Action != nil && event.Action.Interrupted != nil && forcedErr == nil && current.ctx.Err() == nil {
			forcedErr = ErrInteractionUnsupported
			current.cancel()
		}
		if event.Err != nil && firstErr == nil {
			firstErr = event.Err
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		variant := event.Output.MessageOutput
		started := time.Time{}
		if variant.Role == schema.Assistant {
			started = time.Now()
		}
		message, streamed, err := consumeVariant(current, variant)
		if err != nil {
			if message != nil && variant.Role == schema.Assistant {
				latency := time.Duration(0)
				if !started.IsZero() {
					latency = time.Since(started)
				}
				accumulateUsage(&total, message, latency)
			}
			if forcedErr == nil && current.ctx.Err() == nil {
				forcedErr = err
				current.cancel()
			}
			continue
		}
		if message == nil {
			continue
		}
		if err := current.recorder.record(variant.Role, message); err != nil && forcedErr == nil && current.ctx.Err() == nil {
			forcedErr = err
			current.cancel()
		}
		switch variant.Role {
		case schema.Assistant:
			turns++
			latency := time.Duration(0)
			if !started.IsZero() {
				latency = time.Since(started)
			}
			accumulateUsage(&total, message, latency)
			if !streamed && message.Content != "" {
				current.emit(Event{Kind: EventDelta, Text: message.Content})
			}
			for _, call := range message.ToolCalls {
				current.emit(Event{Kind: EventToolCall, CallID: call.ID, Name: call.Function.Name})
			}
			if len(message.ToolCalls) == 0 {
				output = message.Content
			}
		case schema.Tool:
			emitToolResult(current, message)
		}
	}
	if forcedErr != nil {
		return "", turns, total, forcedErr
	}
	if err := current.ctx.Err(); err != nil {
		return "", turns, total, err
	}
	if firstErr != nil {
		return "", turns, total, firstErr
	}
	return output, turns, total, nil
}

func accumulateUsage(total *usage.Usage, message *schema.Message, latency time.Duration) {
	if meta := message.ResponseMeta; meta != nil && meta.Usage != nil {
		total.Accumulate(usage.Usage{
			PromptTokens:     uint64(max(meta.Usage.PromptTokens, 0)),
			CompletionTokens: uint64(max(meta.Usage.CompletionTokens, 0)),
			CachedTokens:     uint64(max(meta.Usage.PromptTokenDetails.CachedTokens, 0)),
		})
	}
	if raw, ok := message.Extra[provider.MessageExtraCacheCreation]; ok {
		switch typed := raw.(type) {
		case uint64:
			total.CacheCreationTokens = usage.SaturatingAdd(total.CacheCreationTokens, typed)
		case float64:
			total.CacheCreationTokens = usage.SaturatingAdd(total.CacheCreationTokens, uint64(max(typed, 0)))
		case int:
			total.CacheCreationTokens = usage.SaturatingAdd(total.CacheCreationTokens, uint64(max(typed, 0)))
		}
	}
	if latency > 0 {
		total.LatencyMS += latency.Milliseconds()
	}
}

func (m *Manager) modelFor(current *task, request Request) (model.BaseChatModel, error) {
	if request.ModelProfileID != "" {
		return m.config.NewModelForProfile(current.ctx, request.ModelProfileID)
	}
	return m.config.NewModel(current.ctx)
}

func (t *task) emit(event Event) {
	if t.observer == nil {
		return
	}
	event.TaskID = t.handle.ID
	t.observer(event)
}

func consumeVariant(current *task, variant *adk.MessageVariant) (*schema.Message, bool, error) {
	if !variant.IsStreaming {
		return variant.Message, false, nil
	}
	defer variant.MessageStream.Close()
	var frames []*schema.Message
	for {
		frame, err := variant.MessageStream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if len(frames) == 0 {
				return nil, true, err
			}
			message, concatErr := schema.ConcatMessages(frames)
			if concatErr != nil {
				return nil, true, err
			}
			return message, true, err
		}
		frames = append(frames, frame)
		if frame != nil && frame.Content != "" {
			current.emit(Event{Kind: EventDelta, Text: frame.Content})
		}
	}
	if len(frames) == 0 {
		return nil, true, nil
	}
	message, err := schema.ConcatMessages(frames)
	if err != nil {
		return nil, true, err
	}
	return message, true, nil
}

func emitToolResult(current *task, message *schema.Message) {
	var result struct {
		OK        bool   `json:"ok"`
		Text      string `json:"text"`
		ExitCode  int    `json:"exitCode"`
		Truncated bool   `json:"truncated"`
		Panic     bool   `json:"panic"`
	}
	_ = json.Unmarshal([]byte(message.Content), &result)
	text, cut := capText(result.Text, 4096)
	current.emit(Event{
		Kind:      EventToolResult,
		CallID:    message.ToolCallID,
		OK:        result.OK,
		Summary:   summarizeText(result.Text),
		Text:      text,
		Truncated: result.Truncated || cut,
		ExitCode:  result.ExitCode,
		Panic:     result.Panic,
	})
}

func summarizeText(text string) string {
	if len(text) <= 400 {
		return text
	}
	end := 400
	for end > 0 && !utf8.ValidString(text[:end]) {
		end--
	}
	return text[:end] + "…"
}

func (m *Manager) scopedTools(ctx context.Context, scope Scope, allowed map[string]struct{}) ([]tool.BaseTool, error) {
	if len(allowed) == 0 {
		return nil, nil
	}
	if m.config.NewTools == nil {
		return nil, fmt.Errorf("%w: no tool factory configured", ErrToolUnavailable)
	}
	available, err := m.config.NewTools(ctx, scope)
	if err != nil {
		return nil, err
	}
	selected := make([]tool.BaseTool, 0, len(available))
	seen := make(map[string]struct{}, len(available))
	found := make(map[string]struct{}, len(allowed))
	for _, candidate := range available {
		if candidate == nil {
			continue
		}
		info, err := candidate.Info(ctx)
		if err != nil {
			return nil, err
		}
		if info == nil {
			return nil, errors.New("subagent tool returned nil info")
		}
		if _, duplicate := seen[info.Name]; duplicate {
			return nil, fmt.Errorf("subagent tool %q was provided more than once", info.Name)
		}
		seen[info.Name] = struct{}{}
		if _, ok := allowed[info.Name]; !ok {
			selected = append(selected, deniedTool{info: info})
			continue
		}
		found[info.Name] = struct{}{}
		selected = append(selected, candidate)
	}
	for name := range allowed {
		if _, ok := found[name]; !ok {
			return nil, fmt.Errorf("%w: %s", ErrToolUnavailable, name)
		}
	}
	return selected, nil
}

type deniedTool struct {
	info *schema.ToolInfo
}

func (d deniedTool) Info(context.Context) (*schema.ToolInfo, error) {
	return d.info, nil
}

func (d deniedTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	return "permission denied: " + d.info.Name + " is outside the subagent scope", nil
}

func normalizeScope(scope *Scope) (Scope, map[string]struct{}, error) {
	if scope == nil {
		return Scope{}, nil, ErrScopeRequired
	}
	normalized := Scope{AllowedTools: make([]string, 0, len(scope.AllowedTools))}
	allowed := make(map[string]struct{}, len(scope.AllowedTools))
	for _, name := range scope.AllowedTools {
		name = strings.TrimSpace(name)
		if name == "" {
			return Scope{}, nil, errors.New("subagent scope contains an empty tool name")
		}
		if _, duplicate := allowed[name]; duplicate {
			continue
		}
		allowed[name] = struct{}{}
		normalized.AllowedTools = append(normalized.AllowedTools, name)
	}
	return normalized, allowed, nil
}

func capText(text string, limit int) (string, bool) {
	if len(text) <= limit {
		return text, false
	}
	end := limit
	for end > 0 && !utf8.ValidString(text[:end]) {
		end--
	}
	return text[:end], true
}
