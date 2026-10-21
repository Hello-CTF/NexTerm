package production

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/watches"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
)

// watchService owns the production watch runtime: one executor per owner over
// the durable store, real session sources for live tabs, the sink fan-out to
// the agent runtime (durable tool-data consumer) and the UI topic, and the
// lifecycle reconcile that re-attaches live targets after a restart and
// settles replaced incarnations on session events. Every query stays
// owner-scoped through the caller's context; a target whose tab is missing is
// never auto-settled, because a durable tab can return with the same
// incarnation.
type watchService struct {
	store    *store.Store
	sessions *session.Manager
	events   ipc.Emitter
	sink     *agent.WatchConsumer
	logger   *slog.Logger

	dispatchInterval time.Duration
	pollInterval     time.Duration

	mu        sync.Mutex
	executors map[string]*watches.Executor
	closed    bool
}

func newWatchService(storage *store.Store, sessions *session.Manager, events ipc.Emitter, roster *agent.Roster, logger *slog.Logger) *watchService {
	if logger == nil {
		logger = slog.Default()
	}
	var consumer *agent.WatchConsumer
	if roster != nil {
		consumer = agent.NewWatchConsumer(storage, roster)
	}
	return &watchService{
		store: storage, sessions: sessions, events: events, sink: consumer, logger: logger,
		dispatchInterval: time.Second, pollInterval: 200 * time.Millisecond,
		executors: map[string]*watches.Executor{},
	}
}

// ownerCtx returns the background owner context the per-owner executor loops
// run under; request contexts cancel, durable loops must not.
func (s *watchService) ownerCtx(owner string) context.Context {
	return ownerContext(context.Background(), owner)
}

// executorFor returns the owner's executor, creating and starting it lazily.
// The first creation reconciles the owner's durable targets against live tabs
// (restart recovery): live targets re-attach from their watches' evaluated
// positions and pending outbox events resume delivery.
func (s *watchService) executorFor(ctx context.Context, owner string) (*watches.Executor, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errors.New("watch service 已关闭")
	}
	if executor, ok := s.executors[owner]; ok {
		s.mu.Unlock()
		return executor, nil
	}
	ownerCtx := s.ownerCtx(owner)
	executor, err := watches.NewExecutor(ownerCtx, s.store, watchSink{service: s}, watches.ExecutorOptions{DispatchInterval: s.dispatchInterval})
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if err := executor.Start(ownerCtx); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	s.executors[owner] = executor
	s.mu.Unlock()
	go s.reconcileOwner(owner)
	return executor, nil
}

// openSource binds a real session source for the tab's current evidenced
// episode; the error is already translated for the watch executor contract.
func (s *watchService) openSource(ctx context.Context, targetID, episode string) (watches.Source, session.WatchStreamState, error) {
	state, ok := s.sessions.WatchStreamState(ctx, targetID)
	if !ok {
		return nil, state, session.ErrWatchTargetGone
	}
	source, err := s.sessions.OpenWatchSource(ctx, targetID, episode, session.WatchSourceOptions{PollInterval: s.pollInterval})
	if err != nil {
		return nil, state, err
	}
	return watchSourceAdapter{source: source}, state, nil
}

// ensureAttached attaches the target to its current evidenced episode: a
// no-op when the executor already mirrors that episode, a replacement attach
// when the episode advanced (settling the old incarnation's watches), or a
// fresh attach. The target must exist for the caller and its episode must be
// evidenced; terminals without sequence tracking are honestly unsupported.
func (s *watchService) ensureAttached(ctx context.Context, executor *watches.Executor, targetID string) error {
	state, ok := s.sessions.WatchStreamState(ctx, targetID)
	if !ok {
		return ipc.NewError(ipc.CodeNotFound, "watch 目标不存在")
	}
	if !state.EpisodeOK {
		return ipc.NewError(ipc.CodeUnsupported, "watch 目标身份不可验证")
	}
	if identity, attached := executor.AttachedIdentity(ctx, targetID); attached && identity == state.Episode {
		return nil
	}
	source, _, err := s.openSource(ctx, targetID, state.Episode)
	if err != nil {
		return mapWatchSourceError(err)
	}
	if err := executor.AttachTarget(ctx, targetID, state.Episode, source); err != nil {
		// The startup reconcile may attach the same episode concurrently;
		// losing that race is not a failure when the mirror landed identically.
		if identity, attached := executor.AttachedIdentity(ctx, targetID); attached && identity == state.Episode {
			return nil
		}
		return err
	}
	return nil
}

// reconcileOwner re-attaches the owner's live durable targets after a
// restart or a lifecycle event. Targets whose tab is missing keep their
// watches armed (a durable tab can return with the same incarnation); a tab
// whose episode advanced re-attaches with the new identity, settling the old
// incarnation.
func (s *watchService) reconcileOwner(owner string) {
	ctx := s.ownerCtx(owner)
	executor, err := s.executorFor(ctx, owner)
	if err != nil {
		return
	}
	rows, err := s.store.AgentWatchTargetListByOwner(ctx)
	if err != nil {
		s.logger.Warn("watch target reconcile list failed", "error", err)
		return
	}
	for _, row := range rows {
		if row.Gone {
			continue
		}
		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()
		if closed {
			return
		}
		state, ok := s.sessions.WatchStreamState(ctx, row.TargetID)
		if !ok || !state.EpisodeOK {
			continue
		}
		if identity, attached := executor.AttachedIdentity(ctx, row.TargetID); attached && identity == state.Episode {
			continue
		}
		source, _, err := s.openSource(ctx, row.TargetID, state.Episode)
		if err != nil {
			continue
		}
		if err := executor.AttachTarget(ctx, row.TargetID, state.Episode, source); err != nil {
			s.logger.Warn("watch target reconcile attach failed", "target", row.TargetID, "error", err)
		}
	}
}

// onSessionEvent reconciles the event owner's watch targets on session
// status changes and terminal exits; the event context carries the owner.
// Owners without any durable watch target never get an executor: a busy
// server's session churn must not spawn idle dispatch loops.
func (s *watchService) onSessionEvent(ctx context.Context, event session.Event) {
	if event.Topic != session.TopicSessionStatus && event.Topic != session.TopicTerminalExit {
		return
	}
	owner := ipc.EventOwnerID(ctx)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	_, running := s.executors[owner]
	s.mu.Unlock()
	if !running {
		rows, err := s.store.AgentWatchTargetListByOwner(s.ownerCtx(owner))
		if err != nil || len(rows) == 0 {
			return
		}
	}
	go s.reconcileOwner(owner)
}

// Shutdown stops every executor. Durable state stays in the store: pending
// outbox events and armed watches resume with the next service.
func (s *watchService) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	executors := make([]*watches.Executor, 0, len(s.executors))
	for owner, executor := range s.executors {
		executors = append(executors, executor)
		delete(s.executors, owner)
	}
	s.mu.Unlock()
	var errs []error
	for _, executor := range executors {
		if err := executor.Stop(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Start satisfies the production component contract; executors start lazily
// per owner.
func (s *watchService) Start(context.Context) error { return nil }

// watchSink is the production sink: the agent-runtime consumer accepts the
// event durably first; the UI topic is best-effort afterwards, so a missed
// notification never loses the durable delivery.
type watchSink struct {
	service *watchService
}

func (s watchSink) DeliverWatchToolData(ctx context.Context, data watches.ToolData) error {
	if s.service.sink != nil {
		if err := s.service.sink.DeliverWatchToolData(ctx, data); err != nil {
			return err
		}
	}
	if err := ipc.Emit(ctx, s.service.events, ipc.TopicAIWatchToolData, watches.ToolDataEvent{OwnerID: ipc.OwnerID(ctx), Data: data}); err != nil && !errors.Is(err, ipc.ErrEventsUnavailable) {
		s.service.logger.Warn("watch tool data UI notification failed", "delivery", data.Event.DeliveryID, "error", err)
	}
	return nil
}

// watchSourceAdapter translates the session source's error vocabulary into
// the watch executor contract.
type watchSourceAdapter struct {
	source *session.WatchSource
}

func (a watchSourceAdapter) Chunk(ctx context.Context, fromSeq int64) ([]byte, int64, error) {
	chunk, next, err := a.source.Chunk(ctx, fromSeq)
	return chunk, next, mapWatchSourceError(err)
}

func (a watchSourceAdapter) End(ctx context.Context) (int64, error) {
	end, err := a.source.End(ctx)
	return end, mapWatchSourceError(err)
}

func (a watchSourceAdapter) Tail(ctx context.Context, beforeSeq int64, maxBytes int) ([]byte, error) {
	tail, err := a.source.Tail(ctx, beforeSeq, maxBytes)
	return tail, mapWatchSourceError(err)
}

func (a watchSourceAdapter) Close() error { return a.source.Close() }

func mapWatchSourceError(err error) error {
	if err == nil {
		return nil
	}
	var gap *session.WatchGapError
	if errors.As(err, &gap) {
		return &watches.GapError{FromSeq: gap.FromSeq}
	}
	var replaced *session.WatchEpisodeReplacedError
	if errors.As(err, &replaced) {
		return &watches.TerminalError{Err: err}
	}
	if errors.Is(err, session.ErrWatchTargetGone) {
		return &watches.TerminalError{Err: err}
	}
	if errors.Is(err, session.ErrWatchEpisodeUnevidenced) {
		return ipc.NewError(ipc.CodeUnsupported, "watch 目标身份不可验证")
	}
	if errors.Is(err, session.ErrUnsupported) {
		return ipc.NewError(ipc.CodeUnsupported, "watch 目标终端不支持序列跟踪")
	}
	return err
}

// WatchConditionDTO mirrors the persisted condition over IPC.
type WatchConditionDTO struct {
	Type    string `json:"type"`
	Pattern string `json:"pattern"`
}

// WatchSummary is the IPC projection of one watch row; it matches the
// reviewed frontend contract field for field.
type WatchSummary struct {
	ID         string            `json:"id"`
	TargetID   string            `json:"targetId"`
	Name       string            `json:"name"`
	Condition  WatchConditionDTO `json:"condition"`
	Status     string            `json:"status"`
	Repeat     bool              `json:"repeat"`
	AnchorSeq  int64             `json:"anchorSeq"`
	Generation uint64            `json:"generation"`
	Revision   uint64            `json:"revision"`
	LastError  string            `json:"lastError,omitempty"`
	UpdatedAt  int64             `json:"updatedAt"`
}

// WatchTargetSummary is the IPC projection of one durable target row enriched
// with live runtime evidence; evidence-bearing states always carry the
// evidence they were classified from.
type WatchTargetSummary struct {
	TargetID string `json:"targetId"`
	Identity string `json:"identity"`
	State    string `json:"state"`
	Evidence string `json:"evidence,omitempty"`
}

func watchSummaryFromRow(row model.AgentWatchRow) (WatchSummary, error) {
	var condition watches.Condition
	if err := json.Unmarshal([]byte(row.ConditionJSON), &condition); err != nil {
		return WatchSummary{}, fmt.Errorf("%w: watch %s 条件损坏", watches.ErrEventCorrupt, row.ID)
	}
	return WatchSummary{
		ID: row.ID, TargetID: row.TargetID, Name: row.Name,
		Condition: WatchConditionDTO{Type: condition.Type, Pattern: condition.Pattern},
		Status:    row.Status, Repeat: row.Repeat, AnchorSeq: row.AnchorSeq,
		Generation: row.Generation, Revision: row.Revision,
		LastError: row.LastError, UpdatedAt: row.UpdatedAt,
	}, nil
}

// targetSummary classifies one durable target row against the live snapshot.
// The snapshot is the evidence: a mirrored attachment only counts when the
// live tab confirms the same episode. Evidence-bearing states are only
// reported with their evidence; a missing tab is disconnected, never gone,
// because a durable tab can return.
func (s *watchService) targetSummary(ctx context.Context, executor *watches.Executor, row model.AgentWatchTargetRow) WatchTargetSummary {
	summary := WatchTargetSummary{TargetID: row.TargetID, Identity: row.Identity}
	state, ok := s.sessions.WatchStreamState(ctx, row.TargetID)
	identity, attached := executor.AttachedIdentity(ctx, row.TargetID)
	if ok && state.EpisodeOK && state.Episode == row.Identity && attached && identity == row.Identity && !state.Exited && state.Status == session.StatusConnected {
		summary.State = "attached"
		summary.Evidence = identity
		return summary
	}
	if row.Gone {
		summary.State = "gone"
		summary.Evidence = "durable detach recorded"
		return summary
	}
	if !ok {
		summary.State = "disconnected"
		return summary
	}
	if !state.EpisodeOK {
		summary.State = "unverifiable"
		summary.Evidence = "runtime episode cannot be evidenced"
		return summary
	}
	if state.Episode != row.Identity {
		summary.State = "replaced"
		summary.Evidence = state.Episode
		return summary
	}
	if state.Exited {
		summary.State = "exited"
		summary.Evidence = state.Episode
		return summary
	}
	summary.State = "disconnected"
	summary.Evidence = string(state.Status)
	return summary
}

func watchIPCError(err error) error {
	if err == nil {
		return nil
	}
	var appErr *ipc.Error
	if errors.As(err, &appErr) {
		return err
	}
	switch {
	case errors.Is(err, watches.ErrNotFound):
		return ipc.WrapError(ipc.CodeNotFound, watches.ErrNotFound.Error(), err)
	case errors.Is(err, watches.ErrConflict):
		return ipc.WrapError(ipc.Code("conflict"), watches.ErrConflict.Error(), err)
	case errors.Is(err, watches.ErrDetached), errors.Is(err, watches.ErrIdentityMismatch):
		return ipc.WrapError(ipc.Code("conflict"), err.Error(), err)
	case errors.Is(err, watches.ErrInvalidCondition):
		return ipc.WrapError(ipc.CodeBadParam, err.Error(), err)
	default:
		return err
	}
}

// registerCommands exposes the production watch surface through the single
// IPC owner: every handler is owner-scoped through the caller's context, and
// the executor it drives is the same per-owner runtime the lifecycle
// reconcile uses.
func (s *watchService) registerCommands(dispatcher *ipc.Dispatcher) error {
	registrations := []func() error{
		func() error {
			return ipc.Register(dispatcher, "ai_watch_create", func(ctx context.Context, _ *ipc.Call, args struct {
				TargetID  string            `json:"targetId"`
				Name      string            `json:"name"`
				Condition WatchConditionDTO `json:"condition"`
				Repeat    bool              `json:"repeat"`
				ExpiresAt int64             `json:"expiresAt"`
			}) (WatchSummary, error) {
				condition := watches.Condition{Type: args.Condition.Type, Pattern: args.Condition.Pattern}
				if err := condition.Validate(); err != nil {
					return WatchSummary{}, watchIPCError(err)
				}
				owner := ipc.OwnerID(ctx)
				executor, err := s.executorFor(ctx, owner)
				if err != nil {
					return WatchSummary{}, err
				}
				if err := s.ensureAttached(ctx, executor, args.TargetID); err != nil {
					return WatchSummary{}, watchIPCError(err)
				}
				params := watches.WatchParams{TargetID: args.TargetID, Name: args.Name, Condition: condition, Repeat: args.Repeat}
				if args.ExpiresAt != 0 {
					params.ExpiresAt = time.UnixMilli(args.ExpiresAt).UTC()
				}
				watch, err := executor.Watch(ctx, params)
				if err != nil {
					return WatchSummary{}, watchIPCError(err)
				}
				row, err := s.store.AgentWatchGet(ctx, watch.ID)
				if err != nil {
					return WatchSummary{}, watchIPCError(err)
				}
				summary, err := watchSummaryFromRow(row)
				return summary, watchIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_watch_list", func(ctx context.Context, _ *ipc.Call, _ struct{}) ([]WatchSummary, error) {
				if _, err := s.executorFor(ctx, ipc.OwnerID(ctx)); err != nil {
					return nil, err
				}
				rows, err := s.store.AgentWatchListByOwner(ctx)
				if err != nil {
					return nil, watchIPCError(err)
				}
				summaries := make([]WatchSummary, 0, len(rows))
				for _, row := range rows {
					summary, err := watchSummaryFromRow(row)
					if err != nil {
						return nil, watchIPCError(err)
					}
					summaries = append(summaries, summary)
				}
				return summaries, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_watch_targets", func(ctx context.Context, _ *ipc.Call, _ struct{}) ([]WatchTargetSummary, error) {
				executor, err := s.executorFor(ctx, ipc.OwnerID(ctx))
				if err != nil {
					return nil, err
				}
				rows, err := s.store.AgentWatchTargetListByOwner(ctx)
				if err != nil {
					return nil, watchIPCError(err)
				}
				summaries := make([]WatchTargetSummary, 0, len(rows))
				for _, row := range rows {
					summaries = append(summaries, s.targetSummary(ctx, executor, row))
				}
				return summaries, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "ai_watch_cancel", func(ctx context.Context, _ *ipc.Call, args struct {
				WatchID          string `json:"watchId"`
				ExpectedRevision uint64 `json:"expectedRevision"`
			}) (WatchSummary, error) {
				if _, err := s.executorFor(ctx, ipc.OwnerID(ctx)); err != nil {
					return WatchSummary{}, err
				}
				watch, err := s.store.AgentWatchCancel(ctx, args.WatchID, args.ExpectedRevision, time.Now().UnixMilli())
				if err != nil {
					if errors.Is(err, store.ErrAgentWatchNotFound) {
						return WatchSummary{}, watchIPCError(watches.ErrNotFound)
					}
					if errors.Is(err, store.ErrAgentWatchConflict) {
						return WatchSummary{}, watchIPCError(watches.ErrConflict)
					}
					return WatchSummary{}, watchIPCError(err)
				}
				summary, err := watchSummaryFromRow(watch)
				return summary, watchIPCError(err)
			})
		},
	}
	for _, register := range registrations {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}
