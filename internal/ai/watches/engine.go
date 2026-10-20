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
	AgentWatchAdvance(ctx context.Context, watchID string, expectedRevision uint64, identity string, anchorSeq, now int64) (model.AgentWatchRow, error)
	AgentWatchFire(ctx context.Context, params store.AgentWatchFireParams) (model.AgentWatchOutboxRow, error)
	AgentWatchCancel(ctx context.Context, watchID string, expectedRevision uint64, now int64) (model.AgentWatchRow, error)
	AgentWatchMarkUnverifiable(ctx context.Context, watchID string, expectedRevision uint64, cause string, now int64) (model.AgentWatchRow, error)
	AgentWatchExpireDue(ctx context.Context, now int64) (int64, error)
	AgentWatchEventPendingList(ctx context.Context, limit int) ([]model.AgentWatchOutboxRow, error)
	AgentWatchEventAck(ctx context.Context, deliveryID string, expectedRevision uint64, now int64) (model.AgentWatchOutboxRow, error)
}

type Options struct {
	Now             func() time.Time
	NewID           func() string
	MaxExcerptBytes int
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
}

type targetBinding struct {
	identity string
	attached bool
}

// Engine owns the in-memory target identity bindings; every watch condition,
// anchor and outbox event is durable in the Store so a restart resumes from
// persisted state without re-firing or skipping output. An Engine belongs to
// one owner runtime: bindings describe that runtime's targets, while every
// persisted query is owner-scoped through the context.
type Engine struct {
	store Store
	opts  Options

	mu      sync.Mutex
	targets map[string]*targetBinding
}

func New(store Store, options Options) (*Engine, error) {
	if store == nil {
		return nil, errors.New("watches: store is nil")
	}
	options.withDefaults()
	return &Engine{store: store, opts: options, targets: map[string]*targetBinding{}}, nil
}

// Attach binds a target to its current identity. Reattaching with a different
// identity means the previous incarnation is gone: watches bound to the old
// identity become unverifiable instead of matching output they cannot vouch
// for, and new watches bind to the new identity.
func (e *Engine) Attach(ctx context.Context, targetID, identity string) error {
	targetID = strings.TrimSpace(targetID)
	identity = strings.TrimSpace(identity)
	if targetID == "" || identity == "" {
		return fmt.Errorf("%w: 目标与身份不能为空", ErrIdentityMismatch)
	}
	e.mu.Lock()
	previous := e.targets[targetID]
	if previous != nil && previous.identity == identity {
		previous.attached = true
		e.mu.Unlock()
		return nil
	}
	e.targets[targetID] = &targetBinding{identity: identity, attached: true}
	e.mu.Unlock()
	if previous != nil && previous.identity != "" {
		e.invalidateTargetWatches(ctx, targetID, previous.identity, "target identity replaced")
	}
	return nil
}

// Detach records that a target went away. A reversible detach (transport
// drop) pauses evaluation while keeping watches armed for the same identity;
// a permanent detach (replaced or gone) settles the target's watches as
// unverifiable. A detach carrying a stale identity is a no-op so a late
// disconnect cannot tear down a newer attachment.
func (e *Engine) Detach(ctx context.Context, targetID, identity string, permanent bool) error {
	e.mu.Lock()
	binding := e.targets[targetID]
	if binding == nil || binding.identity != identity {
		e.mu.Unlock()
		return nil
	}
	if permanent {
		delete(e.targets, targetID)
	} else {
		binding.attached = false
	}
	e.mu.Unlock()
	if permanent {
		e.invalidateTargetWatches(ctx, targetID, identity, "target disconnected permanently")
	}
	return nil
}

func (e *Engine) invalidateTargetWatches(ctx context.Context, targetID, identity, cause string) {
	rows, err := e.store.AgentWatchListByTarget(ctx, targetID)
	if err != nil {
		return
	}
	now := e.opts.Now().UnixMilli()
	for _, row := range rows {
		if row.Identity != identity || Status(row.Status) != StatusArmed {
			continue
		}
		_, _ = e.store.AgentWatchMarkUnverifiable(ctx, row.ID, row.Revision, cause, now)
	}
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
	e.mu.Lock()
	binding := e.targets[params.TargetID]
	e.mu.Unlock()
	if binding == nil || !binding.attached {
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
		ID: params.ID, TargetID: params.TargetID, Identity: binding.identity,
		Name: strings.TrimSpace(params.Name), ConditionJSON: string(conditionJSON),
		Repeat: params.Repeat, AnchorSeq: params.AnchorSeq,
		CreatedAt: now.UnixMilli(), UpdatedAt: now.UnixMilli(),
		ExpiresAt: optionalMillis(params.ExpiresAt),
	})
	if err != nil {
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
// [fromSeq, fromSeq+len(data)) against every armed watch of the target. Each
// watch advances independently: a match fires the watch with caller-fixed
// DeliveryID/RunID (watch transition and outbox row commit in one
// transaction), a miss advances its anchor, and a lost output gap or identity
// change settles it as unverifiable so it never matches unverifiable data.
// Redelivered overlapping chunks are safe: anchors make re-evaluation a
// no-op.
func (e *Engine) Observe(ctx context.Context, targetID, identity string, fromSeq int64, data []byte) ([]Event, error) {
	if len(data) == 0 {
		return nil, nil
	}
	e.mu.Lock()
	binding := e.targets[targetID]
	e.mu.Unlock()
	if binding == nil || !binding.attached {
		return nil, ErrDetached
	}
	if binding.identity != identity {
		return nil, ErrIdentityMismatch
	}
	toSeq := fromSeq + int64(len(data))
	rows, err := e.store.AgentWatchListByTarget(ctx, targetID)
	if err != nil {
		return nil, err
	}
	now := e.opts.Now()
	var fired []Event
	for _, row := range rows {
		if Status(row.Status) != StatusArmed {
			continue
		}
		event, err := e.observeWatch(ctx, row, identity, fromSeq, toSeq, data, now)
		if err != nil {
			return fired, err
		}
		if event != nil {
			fired = append(fired, *event)
		}
	}
	return fired, nil
}

func (e *Engine) observeWatch(ctx context.Context, row model.AgentWatchRow, identity string, fromSeq, toSeq int64, data []byte, now time.Time) (*Event, error) {
	if row.Identity != identity {
		e.markUnverifiable(ctx, row, "watch identity no longer attached")
		return nil, nil
	}
	if row.AnchorSeq < fromSeq {
		e.markUnverifiable(ctx, row, fmt.Sprintf("output gap: anchor %d, chunk starts %d", row.AnchorSeq, fromSeq))
		return nil, nil
	}
	if row.AnchorSeq >= toSeq {
		return nil, nil
	}
	var condition Condition
	if err := json.Unmarshal([]byte(row.ConditionJSON), &condition); err != nil {
		e.markUnverifiable(ctx, row, "stored condition is unreadable")
		return nil, nil
	}
	if err := condition.Validate(); err != nil {
		e.markUnverifiable(ctx, row, "stored condition is invalid")
		return nil, nil
	}
	chunk := data[row.AnchorSeq-fromSeq:]
	matched, relFrom, relTo, excerpt := condition.Evaluate(chunk, e.opts.MaxExcerptBytes)
	if !matched {
		if _, err := e.store.AgentWatchAdvance(ctx, row.ID, row.Revision, identity, toSeq, now.UnixMilli()); err != nil && !isBenignWatchRace(err) {
			return nil, err
		}
		return nil, nil
	}
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

func (e *Engine) markUnverifiable(ctx context.Context, row model.AgentWatchRow, cause string) {
	_, _ = e.store.AgentWatchMarkUnverifiable(ctx, row.ID, row.Revision, cause, e.opts.Now().UnixMilli())
}

func isBenignWatchRace(err error) bool {
	return errors.Is(err, store.ErrAgentWatchConflict) || errors.Is(err, store.ErrAgentWatchNotFound) || errors.Is(err, store.ErrAgentWatchState)
}

// SweepExpired settles armed watches whose expiry has passed.
func (e *Engine) SweepExpired(ctx context.Context) (int64, error) {
	return e.store.AgentWatchExpireDue(ctx, e.opts.Now().UnixMilli())
}
