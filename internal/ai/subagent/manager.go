package subagent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type Manager struct {
	config Config

	mu             sync.Mutex
	tasks          map[string]*task
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
	if request.Timeout < 0 || request.Timeout > m.config.MaxRunTime {
		return Handle{}, fmt.Errorf("subagent timeout must be between 0 and %s", m.config.MaxRunTime)
	}
	scope, allowed, err := normalizeScope(request.Scope)
	if err != nil {
		return Handle{}, err
	}
	if request.ID == "" && m.config.NewID != nil {
		request.ID = m.config.NewID()
		if strings.TrimSpace(request.ID) == "" {
			return Handle{}, errors.New("subagent ID factory returned an empty ID")
		}
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
		return Result{}, ctx.Err()
	}
}

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
		return nil
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
	return nil
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
	output, err := m.execute(current, request, scope, allowed)
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
	}
	if err != nil {
		result.Error = err.Error()
	}

	m.mu.Lock()
	current.result = result
	current.err = err
	current.finished = true
	if registered := m.tasks[current.handle.ID]; registered == current && registered.handle.Generation == current.handle.Generation {
		m.active--
	}
	current.cancel()
	close(current.done)
	m.mu.Unlock()
}

func (m *Manager) execute(current *task, request Request, scope Scope, allowed map[string]struct{}) (string, error) {
	chatModel, err := m.config.NewModel(current.ctx)
	if err != nil {
		return "", err
	}
	if chatModel == nil {
		return "", errors.New("subagent model factory returned nil")
	}
	tools, err := m.scopedTools(current.ctx, scope, allowed)
	if err != nil {
		return "", err
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
		return "", err
	}
	runner := adk.NewRunner(current.ctx, adk.RunnerConfig{Agent: agent, EnableStreaming: true})
	iterator := runner.Run(current.ctx, []*schema.Message{schema.UserMessage(request.Task)})
	var output string
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
		message, err := variant.GetMessage()
		if err != nil {
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
		if variant.Role == schema.Assistant && len(message.ToolCalls) == 0 {
			output = message.Content
		}
	}
	if forcedErr != nil {
		return "", forcedErr
	}
	if err := current.ctx.Err(); err != nil {
		return "", err
	}
	if firstErr != nil {
		return "", firstErr
	}
	return output, nil
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
