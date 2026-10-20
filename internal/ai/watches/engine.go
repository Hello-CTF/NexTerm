package watches

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
)

// Store is the persistence port the engine and dispatcher need;
// *store.Store satisfies it.
type Store interface {
	AgentWatchCreate(ctx context.Context, row model.AgentWatchRow) (model.AgentWatchRow, error)
	AgentWatchGet(ctx context.Context, watchID string) (model.AgentWatchRow, error)
	AgentWatchListByTarget(ctx context.Context, targetID string) ([]model.AgentWatchRow, error)
	AgentWatchAdvance(ctx context.Context, watchID string, expectedRevision uint64, identity string, anchorSeq int64, carry []byte, now int64) (model.AgentWatchRow, error)
	AgentWatchConsume(ctx context.Context, watchID string, expectedRevision uint64, identity string, anchorSeq, now int64) (model.AgentWatchRow, error)
	AgentWatchFire(ctx context.Context, params store.AgentWatchFireParams) (model.AgentWatchOutboxRow, error)
	AgentWatchCancel(ctx context.Context, watchID string, expectedRevision uint64, now int64) (model.AgentWatchRow, error)
	AgentWatchMarkUnverifiable(ctx context.Context, watchID string, expectedRevision uint64, cause string, now int64) (model.AgentWatchRow, error)
	AgentWatchExpireDue(ctx context.Context, now int64) (int64, error)
	AgentWatchTargetAttach(ctx context.Context, targetID, identity string, now int64) (model.AgentWatchTargetRow, error)
	AgentWatchTargetDetach(ctx context.Context, targetID, identity string, now int64) (bool, error)
	AgentWatchTargetGet(ctx context.Context, targetID string) (model.AgentWatchTargetRow, error)
	AgentWatchEventPendingList(ctx context.Context, limit int) ([]model.AgentWatchOutboxRow, error)
	AgentWatchEventAck(ctx context.Context, deliveryID string, expectedRevision uint64, now int64) (model.AgentWatchOutboxRow, error)
}

type Options struct {
	Now             func() time.Time
	NewID           func() string
	MaxExcerptBytes int
	// MaxCarryBytes bounds the durable partial-match overlap retained per
	// watch; conditions whose partial participation exceeds it may miss
	// matches split beyond the window, so it trades memory for split
	// coverage. The store enforces a hard ceiling.
	MaxCarryBytes int
}

func (o *Options) withDefaults() {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.NewID == nil {
		o.NewID = ids.New
	}
	if o.MaxExcerptBytes <= 0 {
		o.MaxExcerptBytes = DefaultMaxExcerptBytes
	}
	if o.MaxCarryBytes <= 0 {
		o.MaxCarryBytes = DefaultMaxCarryBytes
	}
}

// targetBinding is an immutable snapshot of one target's local mirror: the
// durable identity/generation cached at the last successful attach or
// detach, plus the process-local transport attachment. Values are copied in
// and out under the engine lock, so readers never share mutable state with
// writers.
type targetBinding struct {
	identity   string
	generation uint64
	attached   bool
}

// Engine mirrors the durable target lifecycle state in memory for fast
// chunk-time checks; every watch condition, anchor, carry overlap and outbox
// event is durable in the Store, and target incarnation changes only through
// the store's serialized transactions, so a restart or a concurrent engine
// cannot resurrect stale identities or lose invalidations.
type Engine struct {
	store Store
	opts  Options

	mu      sync.Mutex
	targets map[string]targetBinding
}

func New(store Store, options Options) (*Engine, error) {
	if store == nil {
		return nil, errors.New("watches: store is nil")
	}
	options.withDefaults()
	return &Engine{store: store, opts: options, targets: map[string]targetBinding{}}, nil
}

func (e *Engine) binding(targetID string) (targetBinding, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	binding, ok := e.targets[targetID]
	return binding, ok
}

func (e *Engine) setBinding(targetID string, binding targetBinding) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.targets[targetID] = binding
}

// Attach durably binds the target to its current identity incarnation and
// mirrors the result locally. Reattaching with a different identity — or
// after a completed permanent detach — settles the previous incarnation's
// watches in the store transaction, so the invalidation is restart-safe.
func (e *Engine) Attach(ctx context.Context, targetID, identity string) error {
	targetID = strings.TrimSpace(targetID)
	identity = strings.TrimSpace(identity)
	if targetID == "" || identity == "" {
		return fmt.Errorf("%w: 目标与身份不能为空", ErrIdentityMismatch)
	}
	row, err := e.store.AgentWatchTargetAttach(ctx, targetID, identity, e.opts.Now().UnixMilli())
	if err != nil {
		if errors.Is(err, store.ErrAgentWatchConflict) {
			return fmt.Errorf("target %s: %w", targetID, ErrConflict)
		}
		return err
	}
	e.setBinding(targetID, targetBinding{identity: row.Identity, generation: row.Generation, attached: true})
	return nil
}

// Detach records that a target went away. A reversible detach (transport
// drop) is process-local state: evaluation pauses while watches stay armed
// for the same durable identity. A permanent detach is durable — the store
// settles the incarnation's watches and records the gone state — so it stays
// effective after a restart, and a stale detach carrying an older identity is
// rejected as a no-op. Store failures propagate as retryable errors.
func (e *Engine) Detach(ctx context.Context, targetID, identity string, permanent bool) error {
	if permanent {
		if _, err := e.store.AgentWatchTargetDetach(ctx, targetID, identity, e.opts.Now().UnixMilli()); err != nil {
			return err
		}
		e.mu.Lock()
		// A stale detach must not tear down a newer local attachment.
		if binding, ok := e.targets[targetID]; !ok || binding.identity == identity {
			delete(e.targets, targetID)
		}
		e.mu.Unlock()
		return nil
	}
	e.mu.Lock()
	if binding, ok := e.targets[targetID]; ok && binding.identity == identity {
		binding.attached = false
		e.targets[targetID] = binding
	}
	e.mu.Unlock()
	return nil
}

// CreateParams registers a new armed watch on an attached target.
type CreateParams struct {
	ID        string
	TargetID  string
	Name      string
	Condition Condition
	Repeat    bool
	AnchorSeq int64
	ExpiresAt time.Time
}

func (e *Engine) Create(ctx context.Context, params CreateParams) (Watch, error) {
	if err := params.Condition.Validate(); err != nil {
		return Watch{}, err
	}
	params.TargetID = strings.TrimSpace(params.TargetID)
	if params.TargetID == "" {
		return Watch{}, fmt.Errorf("%w: 目标不能为空", ErrInvalidCondition)
	}
	binding, ok := e.binding(params.TargetID)
	if !ok || !binding.attached {
		return Watch{}, ErrDetached
	}
	if params.ID == "" {
		params.ID = e.opts.NewID()
	}
	conditionJSON, err := json.Marshal(params.Condition)
	if err != nil {
		return Watch{}, fmt.Errorf("%w: 条件编码失败: %v", ErrInvalidCondition, err)
	}
	now := e.opts.Now()
	row, err := e.store.AgentWatchCreate(ctx, model.AgentWatchRow{
		ID: params.ID, TargetID: params.TargetID,
		Name: strings.TrimSpace(params.Name), ConditionJSON: string(conditionJSON),
		Repeat: params.Repeat, AnchorSeq: params.AnchorSeq,
		CreatedAt: now.UnixMilli(), UpdatedAt: now.UnixMilli(),
		ExpiresAt: optionalMillis(params.ExpiresAt),
	})
	if err != nil {
		if errors.Is(err, store.ErrAgentWatchDetached) {
			return Watch{}, ErrDetached
		}
		if errors.Is(err, store.ErrAgentWatchConflict) {
			return Watch{}, fmt.Errorf("watch %s: %w", params.ID, ErrConflict)
		}
		return Watch{}, err
	}
	return watchFromRow(row), nil
}

func (e *Engine) Get(ctx context.Context, watchID string) (Watch, error) {
	row, err := e.store.AgentWatchGet(ctx, watchID)
	if err != nil {
		if errors.Is(err, store.ErrAgentWatchNotFound) {
			return Watch{}, ErrNotFound
		}
		return Watch{}, err
	}
	return watchFromRow(row), nil
}

func (e *Engine) ListByTarget(ctx context.Context, targetID string) ([]Watch, error) {
	rows, err := e.store.AgentWatchListByTarget(ctx, targetID)
	if err != nil {
		return nil, err
	}
	watches := make([]Watch, 0, len(rows))
	for _, row := range rows {
		watches = append(watches, watchFromRow(row))
	}
	return watches, nil
}

func (e *Engine) Cancel(ctx context.Context, watchID string, expectedRevision uint64) (Watch, error) {
	row, err := e.store.AgentWatchCancel(ctx, watchID, expectedRevision, e.opts.Now().UnixMilli())
	if err != nil {
		if errors.Is(err, store.ErrAgentWatchNotFound) {
			return Watch{}, ErrNotFound
		}
		if errors.Is(err, store.ErrAgentWatchConflict) {
			return Watch{}, ErrConflict
		}
		return Watch{}, err
	}
	return watchFromRow(row), nil
}

// Observe evaluates a chunk of target output covering stream positions
// [fromSeq, fromSeq+len(data)) against every armed watch of the target.
// Matches split across arbitrary chunk boundaries still fire: each watch
// persists a bounded overlap (carry) of bytes that may still participate in
// a future match, and the overlap is re-evaluated together with the new
// bytes, so redelivered chunks never double-count the seam and a restart
// resumes mid-pattern. Each watch advances independently: a match fires the
// watch with caller-fixed DeliveryID/RunID (watch transition and outbox row
// commit in one transaction), a miss slides the anchor and overlap forward,
// and a lost output gap settles the watch as unverifiable so it never
// matches unverifiable data. Store failures propagate as retryable errors;
// the anchor/overlap make retried chunks safe.
func (e *Engine) Observe(ctx context.Context, targetID, identity string, fromSeq int64, data []byte) ([]Event, error) {
	if len(data) == 0 {
		return nil, nil
	}
	binding, ok := e.binding(targetID)
	if !ok || !binding.attached {
		return nil, ErrDetached
	}
	if binding.identity != identity {
		return nil, ErrIdentityMismatch
	}
	rows, err := e.store.AgentWatchListByTarget(ctx, targetID)
	if err != nil {
		return nil, err
	}
	current, err := e.refreshStaleBinding(ctx, targetID, identity, binding, rows)
	if err != nil {
		return nil, err
	}
	toSeq := fromSeq + int64(len(data))
	now := e.opts.Now()
	var fired []Event
	for _, row := range rows {
		if row.Generation < current {
			if Status(row.Status) == StatusArmed {
				if err := e.markUnverifiable(ctx, row, "superseded by a newer target generation"); err != nil {
					return fired, err
				}
			}
			continue
		}
		switch Status(row.Status) {
		case StatusArmed:
			event, err := e.observeWatch(ctx, row, identity, fromSeq, toSeq, data, now)
			if err != nil {
				return fired, err
			}
			if event != nil {
				fired = append(fired, *event)
			}
		case StatusMatched:
			// A repeat watch awaiting acknowledgement skips matching but
			// consumes the stream so re-arming never sees a false gap.
			if row.Repeat {
				if err := e.consumeWatch(ctx, row, identity, fromSeq, toSeq, now); err != nil {
					return fired, err
				}
			}
		}
	}
	return fired, nil
}

// refreshStaleBinding detects a newer durable generation among the listed
// watches and reconciles the local mirror. A stale presenter never settles
// the newer incarnation's watches: it is rejected with ErrDetached instead.
func (e *Engine) refreshStaleBinding(ctx context.Context, targetID, identity string, binding targetBinding, rows []model.AgentWatchRow) (uint64, error) {
	for _, row := range rows {
		if row.Generation > binding.generation {
			durable, err := e.store.AgentWatchTargetGet(ctx, targetID)
			if err != nil {
				if errors.Is(err, store.ErrAgentWatchNotFound) {
					return 0, ErrDetached
				}
				return 0, err
			}
			if durable.Identity != identity || durable.Gone {
				return 0, ErrDetached
			}
			e.setBinding(targetID, targetBinding{identity: durable.Identity, generation: durable.Generation, attached: true})
			return durable.Generation, nil
		}
	}
	return binding.generation, nil
}

func (e *Engine) observeWatch(ctx context.Context, row model.AgentWatchRow, identity string, fromSeq, toSeq int64, data []byte, now time.Time) (*Event, error) {
	lastEvaluated := row.AnchorSeq + int64(len(row.CarryBlob))
	if fromSeq > lastEvaluated {
		return nil, e.markUnverifiable(ctx, row, fmt.Sprintf("output gap: evaluated %d, chunk starts %d", lastEvaluated, fromSeq))
	}
	if toSeq <= lastEvaluated {
		return nil, nil
	}
	var condition Condition
	if err := json.Unmarshal([]byte(row.ConditionJSON), &condition); err != nil {
		return nil, e.markUnverifiable(ctx, row, "stored condition is unreadable")
	}
	if err := condition.Validate(); err != nil {
		return nil, e.markUnverifiable(ctx, row, "stored condition is invalid")
	}
	// Only bytes beyond the evaluated position are new; the persisted
	// overlap already covers [anchor, lastEvaluated).
	buffer := make([]byte, 0, len(row.CarryBlob)+int(toSeq-lastEvaluated))
	buffer = append(buffer, row.CarryBlob...)
	buffer = append(buffer, data[lastEvaluated-fromSeq:]...)
	matched, relFrom, relTo, excerpt := condition.Evaluate(buffer, e.opts.MaxExcerptBytes)
	if matched {
		deliveryID := e.opts.NewID()
		runID := e.opts.NewID()
		event := Event{
			Kind: EventKind, DeliveryID: deliveryID, RunID: runID,
			WatchID: row.ID, TargetID: row.TargetID, Identity: identity, Name: row.Name,
			Condition:   condition,
			MatchedFrom: row.AnchorSeq + relFrom,
			MatchedTo:   row.AnchorSeq + relTo,
			Excerpt:     excerpt,
			FiredAt:     now.UnixMilli(),
		}
		eventJSON, err := json.Marshal(event)
		if err != nil {
			return nil, fmt.Errorf("encode watch event: %w", err)
		}
		if _, err := e.store.AgentWatchFire(ctx, store.AgentWatchFireParams{
			WatchID: row.ID, ExpectedRevision: row.Revision, Identity: identity,
			DeliveryID: deliveryID, RunID: runID,
			AnchorSeq: toSeq, MatchedFrom: event.MatchedFrom, MatchedTo: event.MatchedTo,
			Excerpt: excerpt, EventKind: EventKind, EventJSON: string(eventJSON),
			Now: now.UnixMilli(),
		}); err != nil {
			if isBenignWatchRace(err) || errors.Is(err, store.ErrAgentWatchIdentity) {
				return nil, nil
			}
			return nil, err
		}
		return &event, nil
	}
	carryLen := condition.carryBound(e.opts.MaxCarryBytes, len(buffer))
	newAnchor := toSeq - int64(carryLen)
	newCarry := append([]byte(nil), buffer[len(buffer)-carryLen:]...)
	if _, err := e.store.AgentWatchAdvance(ctx, row.ID, row.Revision, identity, newAnchor, newCarry, now.UnixMilli()); err != nil && !isBenignWatchRace(err) {
		return nil, err
	}
	return nil, nil
}

func (e *Engine) consumeWatch(ctx context.Context, row model.AgentWatchRow, identity string, fromSeq, toSeq int64, now time.Time) error {
	lastEvaluated := row.AnchorSeq + int64(len(row.CarryBlob))
	if fromSeq > lastEvaluated {
		return e.markUnverifiable(ctx, row, fmt.Sprintf("output gap: evaluated %d, chunk starts %d", lastEvaluated, fromSeq))
	}
	if toSeq <= lastEvaluated {
		return nil
	}
	if _, err := e.store.AgentWatchConsume(ctx, row.ID, row.Revision, identity, toSeq, now.UnixMilli()); err != nil && !isBenignWatchRace(err) {
		return err
	}
	return nil
}

// markUnverifiable settles a watch whose data can no longer be verified.
// CAS races mean another worker already settled it; any other store failure
// propagates so the caller retries instead of silently leaving the watch
// armed.
func (e *Engine) markUnverifiable(ctx context.Context, row model.AgentWatchRow, cause string) error {
	if _, err := e.store.AgentWatchMarkUnverifiable(ctx, row.ID, row.Revision, cause, e.opts.Now().UnixMilli()); err != nil && !isBenignWatchRace(err) {
		return err
	}
	return nil
}

func isBenignWatchRace(err error) bool {
	return errors.Is(err, store.ErrAgentWatchConflict) || errors.Is(err, store.ErrAgentWatchNotFound) || errors.Is(err, store.ErrAgentWatchState)
}

// SweepExpired settles armed watches whose expiry has passed.
func (e *Engine) SweepExpired(ctx context.Context) (int64, error) {
	return e.store.AgentWatchExpireDue(ctx, e.opts.Now().UnixMilli())
}
