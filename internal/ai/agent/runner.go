package agent

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/guard"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/hitl"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/usage"
	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/cloudwego/eino/adk"
)

type Runner struct {
	config       Config
	store        ConversationStore
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
		var err error
		manager, err = hitl.NewManager(hitl.Config{Checkpoints: config.Checkpoints})
		if err != nil {
			panic(fmt.Sprintf("agent: hitl manager: %v", err))
		}
	}
	return &Runner{config: config, store: config.Store, checkpoints: config.Checkpoints, hitl: manager, jobs: make(map[string]*job), reservedJobs: make(map[string]struct{})}
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
	stream = WithEventSequence(stream)
	current := &job{id: jobID, args: args, ctx: jobContext, cancel: cancel, deliveryCtx: deliveryContext, forceCancel: forceCancel, stream: stream, memory: guard.NewMemory(), running: true}
	r.mu.Lock()
	if r.closed || r.jobs[jobID] != nil {
		r.mu.Unlock()
		r.releaseJobID(jobID)
		cancel()
		forceCancel()
		_ = stream.Close()
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

func (r *Runner) Cancel(jobID string) error {
	r.mu.Lock()
	current := r.jobs[jobID]
	r.mu.Unlock()
	if current == nil {
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
	if _, err := r.hitl.Cancel(jobID); err != nil && !errors.Is(err, hitl.ErrRunNotFound) {
		return err
	}
	if !running {
		r.complete(current, "", 0, usage.Usage{}, context.Canceled)
	}
	return nil
}

func (r *Runner) Confirm(confirmation Confirmation) error {
	if confirmation.CallID == "" || confirmation.Nonce == "" {
		return ErrInvalidConfirmation
	}
	switch confirmation.Decision {
	case "allow", "allow_session", "deny":
	default:
		return ErrInvalidConfirmation
	}
	r.mu.Lock()
	current := r.jobs[confirmation.JobID]
	r.mu.Unlock()
	if current == nil {
		if r.config.FallbackConfirm != nil {
			return r.config.FallbackConfirm(confirmation)
		}
		return ErrJobNotFound
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
	if answer.CallID == "" || answer.Nonce == "" || strings.TrimSpace(answer.Text) == "" {
		return ErrInvalidConfirmation
	}
	r.mu.Lock()
	current := r.jobs[answer.JobID]
	r.mu.Unlock()
	if current == nil || current.ctx.Err() != nil {
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

// resumeWithAnswer hands a validated answer to the HITL manager and makes
// sure exactly one consume loop drives the resumed event stream: the parked
// loop picks it up when one is still winding down, otherwise a fresh loop
// starts here.
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

// HITLSnapshot exposes the reconnect surface for one run: pending interrupt
// requests with their stable request IDs plus the terminal event, if any.
func (r *Runner) HITLSnapshot(jobID string) (hitl.Snapshot, error) {
	return r.hitl.Snapshot(jobID)
}

// HITLEvents replays the per-run HITL event log strictly after the given
// sequence so a reconnected client can resume mid-stream without guessing.
func (r *Runner) HITLEvents(jobID string, after uint64) ([]hitl.Event, error) {
	return r.hitl.Events(jobID, after)
}

func (r *Runner) complete(current *job, answer string, turns int, total usage.Usage, terminalErr error) {
	current.completeOnce.Do(func() {
		_, _ = r.hitl.FinishError(current.id, terminalErr)
		current.finish(answer, turns, total, terminalErr)
		r.cleanup(current)
	})
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
		if current.forceCancel != nil {
			current.forceCancel()
		}
		current.cancel()
		running, cancelFn := current.state()
		if cancelFn != nil && running {
			_, _ = cancelFn(adk.WithAgentCancelMode(adk.CancelImmediate))
		}
		// After a HITL resume the active execution runs on the manager's run
		// context with a manager-held cancel function, so neither cancellation
		// above reaches it; cancel every run through the manager before
		// waiting, mirroring Runner.Cancel. Close is best-effort: a run that
		// already finished or fails cleanup must not block the others.
		_, _ = r.hitl.Cancel(current.id)
		if !running {
			r.complete(current, "", 0, usage.Usage{}, context.Canceled)
		}
	}
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return r.hitl.Close()
	case <-ctx.Done():
		_ = r.hitl.Close()
		return ctx.Err()
	}
}
