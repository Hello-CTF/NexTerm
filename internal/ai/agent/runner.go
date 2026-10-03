package agent

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"sync"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/guard"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/usage"
	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/cloudwego/eino/adk"
)

type Runner struct {
	config      Config
	store       ConversationStore
	checkpoints adk.CheckPointStore
	mu          sync.Mutex
	jobs        map[string]*job
	closed      bool
	wg          sync.WaitGroup
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
	return &Runner{config: config, store: config.Store, checkpoints: config.Checkpoints, jobs: make(map[string]*job)}
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
	jobContext, cancel := context.WithCancel(context.WithoutCancel(ctx))
	deliveryContext, forceCancel := context.WithCancel(context.WithoutCancel(jobContext))
	stream, err := factory(ctx, args.ChannelID, jobID)
	if err != nil {
		cancel()
		forceCancel()
		return StartResponse{}, err
	}
	if stream == nil {
		cancel()
		forceCancel()
		return StartResponse{}, errors.New("AI 事件流为空")
	}
	stream = WithEventSequence(stream)
	current := &job{id: jobID, args: args, ctx: jobContext, cancel: cancel, deliveryCtx: deliveryContext, forceCancel: forceCancel, stream: stream, memory: guard.NewMemory(), running: true}
	r.mu.Lock()
	if r.closed || r.jobs[jobID] != nil {
		r.mu.Unlock()
		cancel()
		forceCancel()
		_ = stream.Close()
		return StartResponse{}, errors.New("AI runner 已关闭或 job ID 重复")
	}
	r.jobs[jobID] = current
	r.wg.Add(1)
	r.mu.Unlock()
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
	if current.forceCancel != nil {
		current.forceCancel()
	}
	current.cancel()
	current.pendingMu.Lock()
	running := current.running
	cancelFn := current.cancelFn
	if !running {
		current.pending = nil
	}
	current.pendingMu.Unlock()
	if cancelFn != nil && running {
		_, _ = cancelFn(adk.WithAgentCancelMode(adk.CancelImmediate))
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
	return r.resume(current, "confirm", confirmation.CallID, confirmation.Nonce, confirmation.Decision)
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
	return r.resume(current, "question", answer.CallID, answer.Nonce, answer.Text)
}

func (r *Runner) resume(current *job, kind, callID, nonce, value string) error {
	current.pendingMu.Lock()
	if current.ctx.Err() != nil {
		current.pendingMu.Unlock()
		return ErrJobNotFound
	}
	pending := current.pending
	if pending == nil || pending.kind != kind || pending.callID != callID || pending.nonce != nonce || current.eino == nil {
		current.pendingMu.Unlock()
		return ErrConfirmationStale
	}
	start := !current.running
	current.pending = nil
	current.eino.resume = &adk.ResumeParams{Targets: map[string]any{nonce: value}}
	if start {
		current.running = true
	}
	current.pendingMu.Unlock()
	if start {
		go r.runJob(current)
	}
	return nil
}

func (r *Runner) complete(current *job, answer string, turns int, total usage.Usage, terminalErr error) {
	current.completeOnce.Do(func() {
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
		_, running, cancelFn := current.state()
		if cancelFn != nil && running {
			_, _ = cancelFn(adk.WithAgentCancelMode(adk.CancelImmediate))
		}
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
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
