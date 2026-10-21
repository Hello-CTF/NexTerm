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
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
)

// Store is the persistence port the engine and dispatcher need;
// *store.Store satisfies it.
type Store interface {
	AgentWatchCreate(ctx context.Context, row model.AgentWatchRow, generation uint64) (model.AgentWatchRow, error)
	AgentWatchGet(ctx context.Context, watchID string) (model.AgentWatchRow, error)
	AgentWatchListByTarget(ctx context.Context, targetID string) ([]model.AgentWatchRow, error)
	AgentWatchAdvance(ctx context.Context, watchID string, expectedRevision uint64, identity string, anchorSeq int64, carry, context []byte, carryLive, now int64) (model.AgentWatchRow, error)
	AgentWatchConsume(ctx context.Context, watchID string, expectedRevision uint64, identity string, anchorSeq int64, carry, context []byte, carryLive, now int64) (model.AgentWatchRow, error)
	AgentWatchFire(ctx context.Context, params store.AgentWatchFireParams) (model.AgentWatchOutboxRow, error)
	AgentWatchCancel(ctx context.Context, watchID string, expectedRevision uint64, now int64) (model.AgentWatchRow, error)
	AgentWatchMarkUnverifiable(ctx context.Context, watchID string, expectedRevision uint64, cause string, now int64) (model.AgentWatchRow, error)
	AgentWatchExpireDue(ctx context.Context, now int64) (int64, error)
	AgentWatchTargetAttach(ctx context.Context, targetID, identity string, expectedGeneration uint64, now int64) (model.AgentWatchTargetRow, error)
	AgentWatchTargetDetach(ctx context.Context, targetID, identity string, generation uint64, now int64) (bool, error)
	AgentWatchTargetGet(ctx context.Context, targetID string) (model.AgentWatchTargetRow, error)
	AgentWatchEventPendingList(ctx context.Context, limit int) ([]model.AgentWatchOutboxRow, error)
	AgentWatchEventAck(ctx context.Context, deliveryID string, expectedRevision uint64, now int64) (model.AgentWatchOutboxRow, error)
}

// maxWatchTransitionAttempts bounds the reload/re-evaluation loop used when a
// CAS race (for example an acknowledgement landing mid-Observe) moves a watch
// between the listing and its transition.
const maxWatchTransitionAttempts = 3

// watchContextSize is how many true stream bytes are persisted immediately
// before the anchor to ground line- and word-boundary assertions at the
// evaluation seam; one byte already grounds ^ and \b, the rest is slack.
const watchContextSize = 8

type Options struct {
	Now             func() time.Time
	NewID           func() string
	MaxExcerptBytes int
	// MaxCarryBytes bounds the durable partial-match overlap retained per
	// watch; conditions whose partial participation exceeds it may miss
	// matches split beyond the window, so it trades memory for split
	// coverage. The store enforces a hard ceiling; incomplete trailing
	// UTF-8 sequences are always preserved on top of this bound.
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
	if o.MaxCarryBytes > store.AgentWatchMaxCarryBytes-4 {
		o.MaxCarryBytes = store.AgentWatchMaxCarryBytes - 4
	}
}

// targetKey scopes an in-memory mirror to one owner and target; durable rows
// are owner-scoped, so two owners sharing a target ID never share mirror
// state.
type targetKey struct {
	owner  string
	target string
}

// targetBinding is an immutable snapshot of one target's local mirror: the
// durable identity/generation cached at the last explicit Attach, plus the
// process-local transport attachment. Values are copied in and out under the
// engine lock, so readers never share mutable state with writers, and the
// mirror only advances through a successful Attach — never through Observe.
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
	targets map[targetKey]targetBinding

	// prefixCache maps a condition pattern to its compiled prefix regex, or
	// to nil when the prefix language is not finitely expressible.
	prefixCache sync.Map
}

func New(store Store, options Options) (*Engine, error) {
	if store == nil {
		return nil, errors.New("watches: store is nil")
	}
	options.withDefaults()
	return &Engine{store: store, opts: options, targets: map[targetKey]targetBinding{}}, nil
}

func (e *Engine) binding(key targetKey) (targetBinding, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	binding, ok := e.targets[key]
	return binding, ok
}

func (e *Engine) setBinding(key targetKey, binding targetBinding) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.targets[key] = binding
}

// Attach durably binds the target to its current identity incarnation and
// mirrors the result locally; an explicit successful Attach is the only way
// a local mirror advances. The expected generation is the incarnation fence
// the store checks against the locked durable row in the same transaction —
// a stale presenter fails instead of rolling a newer incarnation back.
// Reattaching with a different identity — or after a completed permanent
// detach — settles the previous incarnation's watches in the store
// transaction, so the invalidation is restart-safe.
func (e *Engine) Attach(ctx context.Context, targetID, identity string, expectedGeneration uint64) error {
	targetID = strings.TrimSpace(targetID)
	identity = strings.TrimSpace(identity)
	if targetID == "" || identity == "" {
		return fmt.Errorf("%w: 目标与身份不能为空", ErrIdentityMismatch)
	}
	row, err := e.store.AgentWatchTargetAttach(ctx, targetID, identity, expectedGeneration, e.opts.Now().UnixMilli())
	if err != nil {
		if errors.Is(err, store.ErrAgentWatchConflict) {
			return fmt.Errorf("target %s: %w", targetID, ErrConflict)
		}
		return err
	}
	e.setBinding(targetKey{owner: ipc.OwnerID(ctx), target: targetID}, targetBinding{identity: row.Identity, generation: row.Generation, attached: true})
	return nil
}

// Detach records that a target went away. A reversible detach (transport
// drop) is process-local state: evaluation pauses while watches stay armed
// for the same durable identity. A permanent detach is durable — the store
// settles the incarnation's watches and records the gone state — so it stays
// effective after a restart, and a stale detach carrying an older identity is
// rejected as a no-op. Store failures propagate as retryable errors.
func (e *Engine) Detach(ctx context.Context, targetID, identity string, permanent bool) error {
	key := targetKey{owner: ipc.OwnerID(ctx), target: targetID}
	if permanent {
		binding, ok := e.binding(key)
		if !ok || !binding.attached {
			// Without a live attachment there is no credential to present;
			// the presenter must explicitly re-Attach first.
			return ErrDetached
		}
		if binding.identity != identity {
			// A detach for an identity this mirror does not hold is a no-op.
			return nil
		}
		settled, err := e.store.AgentWatchTargetDetach(ctx, targetID, identity, binding.generation, e.opts.Now().UnixMilli())
		if err != nil {
			return err
		}
		if !settled {
			// The credential went stale while the mirror looked attached:
			// the presenter must re-Attach before acting on the target.
			return ErrDetached
		}
		e.mu.Lock()
		delete(e.targets, key)
		e.mu.Unlock()
		return nil
	}
	e.mu.Lock()
	if binding, ok := e.targets[key]; ok && binding.identity == identity {
		binding.attached = false
		e.targets[key] = binding
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
	// LeftContext is the true stream history immediately before AnchorSeq
	// (exactly min(watchContextSize, AnchorSeq) bytes, empty only when
	// AnchorSeq is zero). It grounds ^/\b assertions at the evaluation seam.
	LeftContext []byte
	ExpiresAt   time.Time
}

func (e *Engine) Create(ctx context.Context, params CreateParams) (Watch, error) {
	if err := params.Condition.Validate(); err != nil {
		return Watch{}, err
	}
	params.TargetID = strings.TrimSpace(params.TargetID)
	if params.TargetID == "" {
		return Watch{}, fmt.Errorf("%w: 目标不能为空", ErrInvalidCondition)
	}
	if params.AnchorSeq == 0 && len(params.LeftContext) != 0 || params.AnchorSeq > 0 && len(params.LeftContext) != min(watchContextSize, int(params.AnchorSeq)) {
		return Watch{}, fmt.Errorf("%w: 左上下文与锚点不一致", ErrInvalidCondition)
	}
	// A left context ending mid-rune seeds the carry with the incomplete
	// sequence and anchors at the rune start, so the first chunk completes
	// it instead of tripping the anchor guard.
	anchorSeq := params.AnchorSeq
	carry := []byte(nil)
	context := params.LeftContext
	if tail := incompleteUTF8Tail(context); tail > 0 {
		carry = append(carry, context[len(context)-tail:]...)
		context = context[:len(context)-tail]
		anchorSeq -= int64(tail)
	}
	if anchorSeq == 0 && len(context) != 0 {
		return Watch{}, fmt.Errorf("%w: 左上下文与锚点不一致", ErrInvalidCondition)
	}
	binding, ok := e.binding(targetKey{owner: ipc.OwnerID(ctx), target: params.TargetID})
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
		Repeat: params.Repeat, AnchorSeq: anchorSeq, ContextBlob: context, CarryBlob: carry,
		CreatedAt: now.UnixMilli(), UpdatedAt: now.UnixMilli(),
		ExpiresAt: optionalMillis(params.ExpiresAt),
	}, binding.generation)
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
// a future match — including an incomplete trailing UTF-8 sequence, which is
// never evaluated until it completes — and the overlap is re-evaluated
// together with the new bytes, so redelivered chunks never double-count the
// seam and a restart resumes mid-pattern. Each watch advances independently:
// a match fires the watch with caller-fixed DeliveryID/RunID (watch
// transition and outbox row commit in one transaction), a miss slides the
// anchor and overlap forward, and a lost output gap settles the watch as
// unverifiable so it never matches unverifiable data. A watch that moved
// between the listing and its transition (for example an acknowledgement
// re-arming a repeat watch mid-Observe) is reloaded and the same chunk is
// re-evaluated, so concurrent transitions never silently drop output. Store
// failures propagate as retryable errors; the anchor/overlap make retried
// chunks safe.
func (e *Engine) Observe(ctx context.Context, targetID, identity string, fromSeq int64, data []byte) ([]Event, error) {
	if len(data) == 0 {
		return nil, nil
	}
	binding, ok := e.binding(targetKey{owner: ipc.OwnerID(ctx), target: targetID})
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
	for _, row := range rows {
		if row.Generation > binding.generation {
			// The mirror only advances through an explicit Attach: a revoked
			// or superseded presenter must not adopt the newer incarnation.
			return nil, ErrDetached
		}
	}
	toSeq := fromSeq + int64(len(data))
	now := e.opts.Now()
	var fired []Event
	for _, row := range rows {
		if row.Generation < binding.generation {
			if Status(row.Status) == StatusArmed {
				if err := e.markUnverifiable(ctx, row, "superseded by a newer target generation"); err != nil {
					return fired, err
				}
			}
			continue
		}
		event, err := e.processWatch(ctx, row, binding.generation, identity, fromSeq, toSeq, data, now)
		if err != nil {
			return fired, err
		}
		if event != nil {
			fired = append(fired, *event)
		}
	}
	return fired, nil
}

// Credential returns the mirror's current identity and generation for a
// target, and whether it is attached. It lets the executor present the same
// incarnation credential for a durable detach retry.
func (e *Engine) Credential(ctx context.Context, targetID string) (identity string, generation uint64, attached bool) {
	binding, ok := e.binding(targetKey{owner: ipc.OwnerID(ctx), target: targetID})
	if !ok {
		return "", 0, false
	}
	return binding.identity, binding.generation, binding.attached
}

// ObserveEOF marks the target stream as ended at fromSeq. Armed watches
// evaluate their final buffered bytes with end-of-input semantics, so
// right-edge assertions (ready$, ready\b) fire when they hold at the true
// stream end instead of waiting for bytes that will never arrive; a
// deferred match that the stream end refutes stays unfired, and the final
// incomplete UTF-8 sequence, if any, is evaluated as the data it is.
func (e *Engine) ObserveEOF(ctx context.Context, targetID, identity string, fromSeq int64) ([]Event, error) {
	binding, ok := e.binding(targetKey{owner: ipc.OwnerID(ctx), target: targetID})
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
	for _, row := range rows {
		if row.Generation > binding.generation {
			return nil, ErrDetached
		}
	}
	now := e.opts.Now()
	var fired []Event
	for _, row := range rows {
		if row.Generation < binding.generation {
			if Status(row.Status) == StatusArmed {
				if err := e.markUnverifiable(ctx, row, "superseded by a newer target generation"); err != nil {
					return fired, err
				}
			}
			continue
		}
		if Status(row.Status) != StatusArmed {
			continue
		}
		event, err := e.observeWatch(ctx, row, identity, fromSeq, fromSeq, nil, now, true)
		if err != nil {
			return fired, err
		}
		if event != nil {
			fired = append(fired, *event)
		}
	}
	return fired, nil
}

// processWatch evaluates one watch row, reloading and re-evaluating the same
// chunk when a CAS race moved the watch between the listing and its
// transition; only genuine store failures and a persistently conflicting
// watch surface as errors.
func (e *Engine) processWatch(ctx context.Context, row model.AgentWatchRow, current uint64, identity string, fromSeq, toSeq int64, data []byte, now time.Time) (*Event, error) {
	for attempt := 0; attempt < maxWatchTransitionAttempts; attempt++ {
		if row.Generation < current {
			return nil, nil
		}
		if row.Generation > current {
			return nil, ErrDetached
		}
		var err error
		switch Status(row.Status) {
		case StatusArmed:
			var event *Event
			event, err = e.observeWatch(ctx, row, identity, fromSeq, toSeq, data, now, false)
			if err == nil {
				return event, nil
			}
		case StatusMatched:
			// A repeat watch awaiting acknowledgement skips matching but
			// consumes the stream so re-arming never sees a false gap.
			if !row.Repeat {
				return nil, nil
			}
			err = e.consumeWatch(ctx, row, identity, fromSeq, toSeq, data, now)
			if err == nil {
				return nil, nil
			}
		default:
			return nil, nil
		}
		if !errors.Is(err, store.ErrAgentWatchConflict) {
			if isBenignWatchRace(err) {
				return nil, nil
			}
			return nil, err
		}
		// The watch moved between the listing and the transition: reload the
		// true current state and re-evaluate the same chunk; anchors make the
		// retry idempotent and an Ack won race resolves to the re-armed row.
		fresh, err := e.store.AgentWatchGet(ctx, row.ID)
		if err != nil {
			if errors.Is(err, store.ErrAgentWatchNotFound) {
				return nil, nil
			}
			return nil, err
		}
		row = fresh
	}
	return nil, fmt.Errorf("watch %s keeps conflicting after %d attempts: %w", row.ID, maxWatchTransitionAttempts, store.ErrAgentWatchConflict)
}

// prefixBody returns the prefix-regex body for a condition pattern, or false
// when the prefix language is not finitely expressible.
func (e *Engine) prefixBody(pattern string) (string, bool) {
	if cached, ok := e.prefixCache.Load(pattern); ok {
		if cached == nil {
			return "", false
		}
		return cached.(string), true
	}
	body, ok := regexPrefixPattern(pattern)
	if !ok {
		e.prefixCache.Store(pattern, nil)
		return "", false
	}
	e.prefixCache.Store(pattern, body)
	return body, true
}

// liveCarryPlan returns the live-suffix length to persist for the next
// round, reporting overflow when the condition's partial participation
// exceeds the configured carry capacity: an over-capacity condition is
// settled as unverifiable instead of silently dropping possible matches.
func (e *Engine) liveCarryPlan(condition Condition, valid []byte, contextLen int, row model.AgentWatchRow) (int, bool, error) {
	// The incomplete sequence may span the context boundary, leaving the
	// valid prefix shorter than the left context; never index past it.
	region := valid[min(contextLen, len(valid)):]
	avail := len(region)
	switch condition.Type {
	case ConditionContains:
		live := exactLiveSuffix(region, condition.Pattern)
		if live > e.opts.MaxCarryBytes {
			return 0, true, nil
		}
		return live, false, nil
	case ConditionRegex:
		static, finite := regexPrefixParticipation(condition.Pattern, int64(e.opts.MaxCarryBytes)+1)
		if finite && static <= int64(e.opts.MaxCarryBytes) {
			return condition.carryBound(e.opts.MaxCarryBytes, avail), false, nil
		}
		prefixBody, ok := e.prefixBody(condition.Pattern)
		if !ok {
			return 0, true, nil
		}
		live := scanLiveSuffix(row.ContextBlob, region, e.opts.MaxCarryBytes, prefixBody)
		if live > e.opts.MaxCarryBytes {
			return 0, true, nil
		}
		return live, false, nil
	default:
		return 0, false, fmt.Errorf("unknown condition type %q", condition.Type)
	}
}

func (e *Engine) observeWatch(ctx context.Context, row model.AgentWatchRow, identity string, fromSeq, toSeq int64, data []byte, now time.Time, eof bool) (*Event, error) {
	lastEvaluated := row.AnchorSeq + int64(len(row.CarryBlob))
	if fromSeq > lastEvaluated {
		return nil, e.markUnverifiable(ctx, row, fmt.Sprintf("output gap: evaluated %d, chunk starts %d", lastEvaluated, fromSeq))
	}
	if toSeq <= lastEvaluated && !eof {
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
	// overlap already covers [anchor, lastEvaluated), and the persisted left
	// context grounds assertions at the seam.
	contextLen := len(row.ContextBlob)
	buffer := make([]byte, 0, contextLen+len(row.CarryBlob)+int(toSeq-lastEvaluated))
	buffer = append(buffer, row.ContextBlob...)
	buffer = append(buffer, row.CarryBlob...)
	buffer = append(buffer, data[lastEvaluated-fromSeq:]...)
	// An incomplete trailing UTF-8 sequence is not data yet: it is excluded
	// from evaluation so patterns cannot fire on a partial rune, and it is
	// always carried into the next chunk regardless of the condition's
	// participation bound. At EOF the final bytes are data as they are.
	tail := 0
	if !eof {
		tail = incompleteUTF8Tail(buffer)
	}
	valid := buffer[:len(buffer)-tail]
	matched, relFrom, relTo, excerpt := condition.Evaluate(valid, contextLen, e.opts.MaxExcerptBytes, eof)
	if matched {
		// relFrom is relative to the buffer start, which sits contextLen
		// bytes before the anchor. The fired match consumed the evaluated
		// region; only an incomplete trailing rune and the true pre-anchor
		// context carry over, so a repeat watch re-armed later resumes with
		// the real stream context.
		deliveryID := e.opts.NewID()
		runID := e.opts.NewID()
		event := Event{
			Kind: EventKind, DeliveryID: deliveryID, RunID: runID,
			WatchID: row.ID, TargetID: row.TargetID, Identity: identity, Name: row.Name,
			Condition:   condition,
			MatchedFrom: row.AnchorSeq - int64(contextLen) + relFrom,
			MatchedTo:   row.AnchorSeq - int64(contextLen) + relTo,
			Excerpt:     excerpt,
			FiredAt:     now.UnixMilli(),
		}
		eventJSON, err := json.Marshal(event)
		if err != nil {
			return nil, fmt.Errorf("encode watch event: %w", err)
		}
		streamStart := row.AnchorSeq - int64(contextLen)
		newAnchor := toSeq - int64(tail)
		carry := append([]byte(nil), buffer[len(buffer)-tail:]...)
		contextEnd := int(newAnchor - streamStart)
		newContext := buffer[max(0, contextEnd-watchContextSize):contextEnd]
		if _, err := e.store.AgentWatchFire(ctx, store.AgentWatchFireParams{
			WatchID: row.ID, ExpectedRevision: row.Revision, Identity: identity,
			DeliveryID: deliveryID, RunID: runID,
			AnchorSeq: newAnchor, Carry: carry, Context: newContext, CarryLive: 0,
			MatchedFrom: event.MatchedFrom, MatchedTo: event.MatchedTo,
			Excerpt: excerpt, EventKind: EventKind, EventJSON: string(eventJSON),
			Now: now.UnixMilli(),
		}); err != nil {
			return nil, err
		}
		return &event, nil
	}
	live, overflow, err := e.liveCarryPlan(condition, valid, contextLen, row)
	if err != nil {
		return nil, e.markUnverifiable(ctx, row, "carry analysis failed")
	}
	if overflow {
		return nil, e.markUnverifiable(ctx, row, "condition participation exceeds carry capacity")
	}
	// Align the carry start to a rune boundary: matches never start inside a
	// rune, so trimming continuation bytes loses nothing and keeps the next
	// buffer from decoding a partial rune as RuneError data.
	start := len(valid) - live
	for start < len(valid) && valid[start] >= 0x80 && valid[start] <= 0xBF {
		start++
	}
	carry := make([]byte, 0, len(valid)-start+tail)
	carry = append(carry, valid[start:]...)
	carry = append(carry, buffer[len(buffer)-tail:]...)
	newAnchor := toSeq - int64(len(carry))
	// The new left context is the true stream window immediately before the
	// new anchor, taken from the same combined stream view.
	streamStart := row.AnchorSeq - int64(contextLen)
	contextEnd := int(newAnchor - streamStart)
	newContext := buffer[max(0, contextEnd-watchContextSize):contextEnd]
	if _, err := e.store.AgentWatchAdvance(ctx, row.ID, row.Revision, identity, newAnchor, carry, newContext, int64(len(valid)-start), now.UnixMilli()); err != nil {
		return nil, err
	}
	return nil, nil
}

func (e *Engine) consumeWatch(ctx context.Context, row model.AgentWatchRow, identity string, fromSeq, toSeq int64, data []byte, now time.Time) error {
	lastEvaluated := row.AnchorSeq + int64(len(row.CarryBlob))
	if fromSeq > lastEvaluated {
		return e.markUnverifiable(ctx, row, fmt.Sprintf("output gap: evaluated %d, chunk starts %d", lastEvaluated, fromSeq))
	}
	if toSeq <= lastEvaluated {
		return nil
	}
	// The suspended watch skips matching but not stream bookkeeping: the
	// still-incomplete rune is preserved with the anchor retreating to the
	// rune start, and the true pre-anchor context is kept, so the re-armed
	// watch neither misses the completed rune nor fires on continuation
	// bytes misread as RuneError.
	newBytes := data[lastEvaluated-fromSeq:]
	combined := make([]byte, 0, len(row.CarryBlob)+len(newBytes))
	combined = append(combined, row.CarryBlob...)
	combined = append(combined, newBytes...)
	tail := incompleteUTF8Tail(combined)
	newAnchor := toSeq - int64(tail)
	carry := append([]byte(nil), combined[len(combined)-tail:]...)
	stream := make([]byte, 0, len(row.ContextBlob)+len(combined))
	stream = append(stream, row.ContextBlob...)
	stream = append(stream, combined...)
	contextEnd := len(stream) - tail
	newContext := stream[max(0, contextEnd-watchContextSize):contextEnd]
	if _, err := e.store.AgentWatchConsume(ctx, row.ID, row.Revision, identity, newAnchor, carry, newContext, 0, now.UnixMilli()); err != nil {
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
