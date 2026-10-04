package agent

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/guard"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/hitl"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/steer"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/subagent"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/usage"
	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

type Runner struct {
	config       Config
	store        ConversationStore
	runs         RunStore
	checkpoints  adk.CheckPointStore
	hitl         *hitl.Manager
	mu           sync.Mutex
	jobs         map[string]*job
	reservedJobs map[string]struct{}
	closed       bool
	wg           sync.WaitGroup
}

func NewRunner(config Config) *Runner {
	if config.NewID == nil {
		config.NewID = ids.New
	}
	if config.MaxTurns <= 0 {
		config.MaxTurns = 24
	}
	if config.MaxImages <= 0 {
		config.MaxImages = 8
	}
	if config.MaxImageBytes <= 0 {
		config.MaxImageBytes = 5 << 20
	}
	if config.MaxPendingSteers <= 0 {
		config.MaxPendingSteers = 16
	}
	if config.Permission == nil && config.Permissions != nil {
		config.Permission = config.Permissions.Snapshot
	}
	if config.Permission == nil {
		config.Permission = func(context.Context) (guard.Config, error) {
			return guard.Config{Mode: guard.ReadWrite}, nil
		}
	}
	if config.Model == nil && config.Profiles != nil {
		config.Model = activeProfileModel(config.Profiles)
	}
	if config.Checkpoints == nil {
		config.Checkpoints = NewMemoryCheckpoints()
	}
	manager := config.HITL
	if manager == nil {
		hitlConfig := hitl.Config{Checkpoints: config.Checkpoints}
		if config.Runs != nil {
			hitlConfig.Store = hitlStoreBridge{config.Runs}
		}
		var err error
		manager, err = hitl.NewManager(hitlConfig)
		if err != nil {
			panic(fmt.Sprintf("agent: hitl manager: %v", err))
		}
	}
	return &Runner{config: config, store: config.Store, runs: config.Runs, checkpoints: config.Checkpoints, hitl: manager, jobs: make(map[string]*job), reservedJobs: make(map[string]struct{})}
}

func (r *Runner) reserveJobID(jobID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.reservedJobs[jobID]; r.closed || r.jobs[jobID] != nil || exists {
		return errors.New("AI runner 已关闭或 job ID 重复")
	}
	r.reservedJobs[jobID] = struct{}{}
	return nil
}

func (r *Runner) releaseJobID(jobID string) {
	r.mu.Lock()
	delete(r.reservedJobs, jobID)
	r.mu.Unlock()
}

func (r *Runner) Start(ctx context.Context, args ChatArgs, factory StreamFactory) (StartResponse, error) {
	if factory == nil {
		return StartResponse{}, errors.New("AI 事件流未配置")
	}
	if r.store == nil {
		return StartResponse{}, errors.New("AI 会话存储未配置")
	}
	if strings.TrimSpace(args.Message) == "" && len(args.Images) == 0 {
		return StartResponse{}, errors.New("消息不能为空")
	}
	if len(args.Images) > r.config.MaxImages {
		return StartResponse{}, errors.New("图片数量超出限制")
	}
	totalImageBytes := 0
	for _, image := range args.Images {
		size, err := decodedImageSize(image)
		if err != nil || size > r.config.MaxImageBytes {
			return StartResponse{}, errors.New("图片必须为有效 base64/data URI 且不超过大小限制")
		}
		totalImageBytes += size
		if totalImageBytes > 4*r.config.MaxImageBytes {
			return StartResponse{}, errors.New("图片总大小超出限制")
		}
	}
	conversationID := args.ConversationID
	var err error
	if conversationID == "" {
		row, createErr := r.store.ConvCreate(ctx, AutoTitle(args.Message), map[string]any{"scope": args.Scope})
		if createErr != nil {
			return StartResponse{}, createErr
		}
		conversationID = row.ID
	} else if _, err = r.store.ConvGet(ctx, conversationID); err != nil {
		return StartResponse{}, err
	}
	args.ConversationID = conversationID
	if args.ModelProfileID == "" && r.config.Profiles != nil {
		if profile, ok := r.config.Profiles.ActiveProfile(); ok {
			args.ModelProfileID = profile.ID
		}
	}
	jobID := r.config.NewID()
	if strings.TrimSpace(jobID) == "" {
		return StartResponse{}, errors.New("AI job ID 为空")
	}
	if err := r.reserveJobID(jobID); err != nil {
		return StartResponse{}, err
	}
	jobContext, cancel := context.WithCancel(context.WithoutCancel(ctx))
	deliveryContext, forceCancel := context.WithCancel(context.WithoutCancel(jobContext))
	stream, err := factory(ctx, args.ChannelID, jobID)
	if err != nil {
		r.releaseJobID(jobID)
		cancel()
		forceCancel()
		return StartResponse{}, err
	}
	if stream == nil {
		r.releaseJobID(jobID)
		cancel()
		forceCancel()
		return StartResponse{}, errors.New("AI 事件流为空")
	}

	if err := r.store.MsgInsert(ctx, conversationID, "user", map[string]any{"role": "user", "content": args.Message, "imageCount": len(args.Images), "jobId": jobID}, nil, nil); err != nil {
		r.releaseJobID(jobID)
		cancel()
		forceCancel()
		_ = stream.Close()
		return StartResponse{}, err
	}
	if r.runs != nil {
		if err := r.runs.RunInsert(ctx, store.RunRow{ID: jobID, ConversationID: conversationID, Status: store.RunStatusRunning, PlanMode: args.PlanMode, Source: args.Source, ProfileID: r.profileIDFor(args)}); err != nil {
			r.releaseJobID(jobID)
			cancel()
			forceCancel()
			_ = stream.Close()
			return StartResponse{}, err
		}
	}
	stream = r.wrapStream(stream, jobID)
	current := &job{id: jobID, args: args, ctx: jobContext, cancel: cancel, deliveryCtx: deliveryContext, forceCancel: forceCancel, stream: stream, memory: guard.NewMemory(), steer: steer.NewQueue(r.config.MaxPendingSteers), running: true}
	r.mu.Lock()
	if r.closed || r.jobs[jobID] != nil {
		r.mu.Unlock()
		r.releaseJobID(jobID)
		cancel()
		forceCancel()
		_ = stream.Close()
		if r.runs != nil {
			_ = r.runs.RunDelete(ctx, jobID)
		}
		return StartResponse{}, errors.New("AI runner 已关闭或 job ID 重复")
	}
	r.jobs[jobID] = current
	r.wg.Add(1)
	r.mu.Unlock()
	if _, err := r.hitl.RegisterRun(jobContext, jobID, jobID); err != nil {
		r.mu.Lock()
		if r.jobs[jobID] == current {
			delete(r.jobs, jobID)
		}
		r.wg.Done()
		r.mu.Unlock()
		cancel()
		forceCancel()
		_ = stream.Close()
		r.releaseJobID(jobID)
		if r.runs != nil {
			_ = r.runs.RunDelete(ctx, jobID)
		}
		return StartResponse{}, err
	}
	go r.watchHITL(current)
	go r.runJob(current)
	return StartResponse{JobID: jobID, ConversationID: conversationID}, nil
}

func decodedImageSize(image string) (int, error) {
	encoded := image
	if index := strings.Index(image, ";base64,"); index >= 0 {
		if !strings.HasPrefix(image, "data:image/") {
			return 0, errors.New("invalid image data URI")
		}
		encoded = image[index+8:]
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	return len(decoded), err
}

var cancelTestHook func()

func (r *Runner) Cancel(jobID string) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return ErrJobNotFound
	}
	current := r.jobs[jobID]
	r.mu.Unlock()
	if current == nil {
		if snapshot, err := r.hitl.Snapshot(jobID); err == nil && snapshot.Terminal == nil {
			_, err := r.hitl.Cancel(jobID)
			return err
		}
		if r.config.FallbackCancel != nil {
			return r.config.FallbackCancel(jobID)
		}
		return ErrJobNotFound
	}
	current.cancel()
	current.pendingMu.Lock()
	running := current.running
	cancelFn := current.cancelFn
	current.pendingMu.Unlock()
	if cancelFn != nil && running {
		_, _ = cancelFn(adk.WithAgentCancelMode(adk.CancelImmediate))
	}
	if cancelTestHook != nil {
		cancelTestHook()
	}
	err := r.cancelRun(jobID)
	if !running {
		r.complete(current, "", 0, usage.Usage{}, context.Canceled)
	}
	return err
}

func (r *Runner) Steer(jobID, message string) error {
	if strings.TrimSpace(message) == "" {
		return errors.New("AI 补充指令不能为空")
	}
	r.mu.Lock()
	current := r.jobs[jobID]
	r.mu.Unlock()
	if current == nil || current.ctx.Err() != nil {
		return ErrJobNotFound
	}
	if err := current.steer.Push(schema.UserMessage(message)); err != nil {
		return fmt.Errorf("%w: %w", ErrSteerQueueFull, err)
	}

	if err := r.store.MsgInsert(current.ctx, current.args.ConversationID, "user", map[string]any{"role": "user", "content": message, "steered": true, "jobId": current.id}, nil, nil); err != nil {
		return err
	}
	return nil
}

func (r *Runner) cancelRun(jobID string) error {
	_, err := r.hitl.Cancel(jobID)
	if err == nil || errors.Is(err, hitl.ErrRunNotFound) {
		return nil
	}
	if !errors.Is(err, hitl.ErrRunFinished) {
		return err
	}
	snapshot, snapshotErr := r.hitl.Snapshot(jobID)
	if snapshotErr == nil && snapshot.Terminal != nil {
		return nil
	}
	return err
}

func (r *Runner) Confirm(confirmation Confirmation) error {
	return r.ConfirmStream(context.Background(), confirmation, nil)
}

func (r *Runner) ConfirmStream(ctx context.Context, confirmation Confirmation, factory StreamFactory) error {
	if confirmation.CallID == "" || confirmation.Nonce == "" {
		return ErrInvalidConfirmation
	}
	switch confirmation.Decision {
	case "allow", "allow_session", "deny":
	default:
		return ErrInvalidConfirmation
	}
	current := r.lookupJob(confirmation.JobID)
	if current == nil {
		restored, err := r.restoreJob(ctx, confirmation.JobID, factory)
		if err != nil {
			if r.config.FallbackConfirm != nil {
				return r.config.FallbackConfirm(confirmation)
			}
			return ErrJobNotFound
		}
		current = restored
	}
	if current.ctx.Err() != nil {
		return ErrJobNotFound
	}
	request, err := r.pendingInterrupt(current.id, confirmation.CallID, confirmation.Nonce, hitl.KindConfirm)
	if err != nil {
		return err
	}
	answer := hitl.Answer{
		ID:           r.config.NewID(),
		RunID:        current.id,
		RequestID:    request.ID,
		CheckpointID: request.CheckpointID,
		TargetID:     request.TargetID,
		CallID:       request.CallID,
		Nonce:        confirmation.Nonce,
		Parameters:   request.Parameters,
		Decision:     hitl.Decision(confirmation.Decision),
	}
	return r.resumeWithAnswer(current, answer)
}

func (r *Runner) Answer(answer Answer) error {
	return r.AnswerStream(context.Background(), answer, nil)
}

func (r *Runner) AnswerStream(ctx context.Context, answer Answer, factory StreamFactory) error {
	if answer.CallID == "" || answer.Nonce == "" || strings.TrimSpace(answer.Text) == "" {
		return ErrInvalidConfirmation
	}
	current := r.lookupJob(answer.JobID)
	if current == nil {
		restored, err := r.restoreJob(ctx, answer.JobID, factory)
		if err != nil {
			return ErrJobNotFound
		}
		current = restored
	}
	if current.ctx.Err() != nil {
		return ErrJobNotFound
	}
	request, err := r.pendingInterrupt(current.id, answer.CallID, answer.Nonce, hitl.KindQuestion)
	if err != nil {
		return err
	}
	hitlAnswer := hitl.Answer{
		ID:           r.config.NewID(),
		RunID:        current.id,
		RequestID:    request.ID,
		CheckpointID: request.CheckpointID,
		TargetID:     request.TargetID,
		CallID:       request.CallID,
		Nonce:        answer.Nonce,
		Parameters:   request.Parameters,
		Text:         answer.Text,
	}
	return r.resumeWithAnswer(current, hitlAnswer)
}

func (r *Runner) lookupJob(jobID string) *job {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.jobs[jobID]
}

func (r *Runner) pendingInterrupt(jobID, callID, nonce string, kind hitl.Kind) (hitl.Interrupt, error) {
	snapshot, err := r.hitl.Snapshot(jobID)
	if err != nil {
		return hitl.Interrupt{}, mapResumeError(err)
	}
	for _, pending := range snapshot.Pending {
		if pending.CallID == callID && pending.Kind == kind && subtle.ConstantTimeCompare([]byte(pending.Nonce), []byte(nonce)) == 1 {
			return pending, nil
		}
	}
	return hitl.Interrupt{}, fmt.Errorf("%w: 没有待处理的确认请求匹配该调用", ErrConfirmationStale)
}

func (r *Runner) resumeWithAnswer(current *job, answer hitl.Answer) error {
	current.pendingMu.Lock()
	if current.ctx.Err() != nil || current.eino == nil || current.eino.runner == nil {
		current.pendingMu.Unlock()
		return ErrJobNotFound
	}
	resumer := current.eino.runner
	_, iterator, err := r.hitl.Resume(context.Background(), resumer, answer)
	if err != nil {
		current.pendingMu.Unlock()
		return mapResumeError(err)
	}
	r.updateRunStatus(current, store.RunStatusRunning)
	if current.running {
		current.resumeIterator = iterator
		current.pendingMu.Unlock()
		return nil
	}
	current.running = true
	current.pendingMu.Unlock()
	go r.consumeResumed(current, iterator)
	return nil
}

func mapResumeError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, hitl.ErrRunFinished), errors.Is(err, hitl.ErrRunNotFound), errors.Is(err, hitl.ErrManagerClosed):
		return fmt.Errorf("%w: %w", ErrJobNotFound, err)
	case errors.Is(err, hitl.ErrInvalidArgument):
		return fmt.Errorf("%w: %w", ErrInvalidConfirmation, err)
	default:
		return fmt.Errorf("%w: %w", ErrConfirmationStale, err)
	}
}

func (r *Runner) HITLSnapshot(jobID string) (hitl.Snapshot, error) {
	return r.hitl.Snapshot(jobID)
}

func (r *Runner) HITLEvents(jobID string, after uint64) ([]hitl.Event, error) {
	return r.hitl.Events(jobID, after)
}

func (r *Runner) complete(current *job, answer string, turns int, total usage.Usage, terminalErr error) {
	current.completeOnce.Do(func() {
		r.reportSteerLeftover(current)
		if err := r.closeSubagents(current); err != nil && terminalErr == nil {
			terminalErr = err
		}
		terminal, _ := r.hitl.FinishError(current.id, terminalErr)
		current.finish(answer, turns, total, terminalErr)
		r.finishRun(current, answer, turns, total, terminalErr, terminal)
		r.cleanup(current)
	})
}

var errSubagentCloseTimeout = errors.New("subagent shutdown timed out before persistence")

func (r *Runner) closeSubagents(current *job) error {
	current.pendingMu.Lock()
	current.completed = true
	manager := current.subagents
	current.pendingMu.Unlock()
	if manager == nil {
		return nil
	}
	return r.closeSubagentManager(manager)
}

func (r *Runner) closeSubagentManager(manager *subagent.Manager) error {
	done := make(chan struct{})
	go func() {
		_ = manager.Close()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-time.After(5 * time.Second):
		return errSubagentCloseTimeout
	}
}

func (r *Runner) profileIDFor(args ChatArgs) string {
	if args.ModelProfileID != "" {
		return args.ModelProfileID
	}
	if r.config.Profiles != nil {
		if profile, ok := r.config.Profiles.ActiveProfile(); ok {
			return profile.ID
		}
	}
	return ""
}

func (r *Runner) finishRun(current *job, answer string, turns int, total usage.Usage, terminalErr error, terminal hitl.Event) {
	if r.runs == nil {
		return
	}
	status := store.RunStatusCompleted
	message := ""
	if terminalErr != nil {
		message = terminalErr.Error()
		switch terminal.Reason {
		case hitl.TerminalCanceled:
			status = store.RunStatusCanceled
		case hitl.TerminalExpired:
			status = store.RunStatusExpired
		case hitl.TerminalFailed:
			status = store.RunStatusFailed
		default:
			if errors.Is(terminalErr, context.Canceled) {
				status = store.RunStatusCanceled
			} else {
				status = store.RunStatusFailed
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(current.ctx), 5*time.Second)
	defer cancel()
	_ = r.runs.RunFinishUsage(ctx, current.id, status, answer, message, turns,
		usage.SaturatingInt64(total.PromptTokens), usage.SaturatingInt64(total.CompletionTokens),
		usage.SaturatingInt64(total.CacheCreationTokens), usage.SaturatingInt64(uint64(max(total.LatencyMS, 0))))
}

func (r *Runner) updateRunStatus(current *job, status string) {
	if r.runs == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(current.ctx), 5*time.Second)
	defer cancel()
	_ = r.runs.RunUpdateStatus(ctx, current.id, status)
}

func (r *Runner) wrapStream(stream Stream, jobID string) Stream {
	if r.runs != nil {
		return WithRunJournal(stream, r.runs, jobID)
	}
	return WithEventSequence(stream)
}

func (r *Runner) reportSteerLeftover(current *job) {
	leftover := current.steer.Drain()
	if len(leftover) == 0 {
		return
	}
	parent := current.deliveryCtx
	if parent == nil {
		parent = context.WithoutCancel(current.ctx)
	}

	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	for _, message := range leftover {
		_ = current.emit(ctx, Event{Type: "steerDropped", Text: message.Content})
	}
}

func (r *Runner) cleanup(current *job) {
	r.mu.Lock()
	if r.jobs[current.id] == current {
		delete(r.jobs, current.id)
	}
	r.mu.Unlock()
	current.cancel()
	r.releaseJobID(current.id)
	if current.forceCancel != nil {
		current.forceCancel()
	}
	if r.config.Tools != nil {
		r.config.Tools.Release(current.id)
	}
	if deleter, ok := r.checkpoints.(adk.CheckPointDeleter); ok {
		_ = deleter.Delete(context.Background(), current.id)
	}
	r.wg.Done()
}

func (r *Runner) Close() error {
	return r.CloseContext(context.Background())
}

func (r *Runner) CloseContext(ctx context.Context) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	jobs := make([]*job, 0, len(r.jobs))
	for _, current := range r.jobs {
		jobs = append(jobs, current)
	}
	r.mu.Unlock()
	for _, current := range jobs {
		running, cancelFn := current.state()
		if !running {
			r.parkForShutdown(current)
			continue
		}
		if current.forceCancel != nil {
			current.forceCancel()
		}
		current.cancel()
		if cancelFn != nil {
			_, _ = cancelFn(adk.WithAgentCancelMode(adk.CancelImmediate))
		}

		_, _ = r.hitl.Cancel(current.id)
	}
	closeErr := r.hitl.Close()
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		if r.config.Memory != nil {
			if err := r.config.Memory.Close(); closeErr == nil {
				closeErr = err
			}
		}
		return closeErr
	case <-ctx.Done():
		if r.config.Memory != nil {
			_ = r.config.Memory.Close()
		}
		return ctx.Err()
	}
}
