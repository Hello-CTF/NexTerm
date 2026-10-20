package watches

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
)

// Source supplies output chunks for one target with absolute stream
// positions. The executor drives it; a real implementation wraps a terminal
// or session stream once the roster contract lands (M25/M26) — the
// synthetic sources in tests are not external CLI validation.
type Source interface {
	// Chunk returns the next output chunk starting at fromSeq and the
	// position immediately after it. Returning io.EOF marks the stream end
	// (ObserveEOF semantics); any other error is retried with backoff.
	Chunk(ctx context.Context, fromSeq int64) (chunk []byte, nextSeq int64, err error)
	// End returns the current stream end position.
	End(ctx context.Context) (int64, error)
	// Tail returns up to maxBytes of the stream immediately before beforeSeq.
	Tail(ctx context.Context, beforeSeq int64, maxBytes int) ([]byte, error)
	// Close releases the source.
	Close() error
}

// ExecutorOptions carries the engine options plus the loop settings.
type ExecutorOptions struct {
	Now             func() time.Time
	NewID           func() string
	MaxExcerptBytes int
	MaxCarryBytes   int
	// DispatchInterval is the outbox poll interval of the dispatch loop.
	DispatchInterval time.Duration
	// RetryBackoff is the runner's retry delay after source or store errors.
	RetryBackoff time.Duration
}

func (o *ExecutorOptions) withDefaults() {
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
	if o.DispatchInterval <= 0 {
		o.DispatchInterval = time.Second
	}
	if o.RetryBackoff <= 0 {
		o.RetryBackoff = time.Second
	}
}

// Executor wires real output sources into the watch engine: one runner per
// target consumes its source with absolute stream positions, the watch
// lifecycle (anchors, identity, disconnect, cancel, recovery) drives the
// engine, and the dispatch loop delivers pending outbox events as untrusted
// tool data. All durable state lives in the store, so a restart resumes
// delivery with the same fixed DeliveryID/RunID and the runner resumes from
// the watches' own evaluated positions without false gaps.
type Executor struct {
	engine     *Engine
	dispatcher *Dispatcher
	store      Store
	opts       ExecutorOptions
	ownerCtx   context.Context

	lifecycleCtx    context.Context
	lifecycleCancel context.CancelFunc

	mu      sync.Mutex
	targets map[string]*targetRunner
	// pendingDetach keeps the incarnation credential of a failed permanent
	// detach so the retry can complete the durable transition.
	pendingDetach map[string]pendingDetach
	started       bool
	dispatch      sync.WaitGroup
}

type pendingDetach struct {
	identity   string
	generation uint64
}

// NewExecutor builds the engine and dispatcher over the same store. The
// owner context scopes every loop operation; the executor is per-owner.
func NewExecutor(ctx context.Context, store Store, sink Sink, options ExecutorOptions) (*Executor, error) {
	if store == nil {
		return nil, errors.New("watches: executor store is nil")
	}
	options.withDefaults()
	engine, err := New(store, Options{
		Now: options.Now, NewID: options.NewID,
		MaxExcerptBytes: options.MaxExcerptBytes, MaxCarryBytes: options.MaxCarryBytes,
	})
	if err != nil {
		return nil, err
	}
	dispatcher, err := NewDispatcher(store, sink, Options{Now: options.Now})
	if err != nil {
		return nil, err
	}
	lifecycleCtx, cancel := context.WithCancel(context.Background())
	return &Executor{
		engine: engine, dispatcher: dispatcher, store: store, opts: options, ownerCtx: ctx,
		lifecycleCtx: lifecycleCtx, lifecycleCancel: cancel,
		targets: map[string]*targetRunner{}, pendingDetach: map[string]pendingDetach{},
	}, nil
}

// Start begins the dispatch loop.
func (e *Executor) Start(ctx context.Context) error {
	e.mu.Lock()
	if e.started {
		e.mu.Unlock()
		return errors.New("watches: executor already started")
	}
	e.started = true
	e.mu.Unlock()
	e.dispatch.Add(1)
	go func() {
		defer e.dispatch.Done()
		ticker := time.NewTicker(e.opts.DispatchInterval)
		defer ticker.Stop()
		for {
			select {
			case <-e.lifecycleCtx.Done():
				return
			case <-ticker.C:
				// A failed pass leaves rows pending; the next tick retries,
				// keeping delivery at-least-once.
				_, _ = e.DispatchOnce(e.ownerCtx)
			}
		}
	}()
	return nil
}

// Stop halts the dispatch loop and every runner, closing their sources. It
// does not detach targets durably: the durable lifecycle stays with the
// engine, and pending outbox events await the next executor.
func (e *Executor) Stop(ctx context.Context) error {
	e.lifecycleCancel()
	e.dispatch.Wait()
	e.mu.Lock()
	runners := make([]*targetRunner, 0, len(e.targets))
	for id, runner := range e.targets {
		runners = append(runners, runner)
		delete(e.targets, id)
	}
	e.mu.Unlock()
	for _, runner := range runners {
		runner.cancel()
		<-runner.done
		_ = runner.source.Close()
	}
	return nil
}

// DispatchOnce runs one outbox pass.
func (e *Executor) DispatchOnce(ctx context.Context) (int, error) {
	return e.dispatcher.DispatchOnce(ctx, 64)
}

type targetRunner struct {
	targetID string
	identity string
	source   Source
	cancel   context.CancelFunc
	done     chan struct{}
	// ownerCtx carries the attaching owner's identity for engine calls; the
	// lifecycle context only cancels the source.
	ownerCtx context.Context

	// observeMu serializes Watch creation with the consume loop, so a new
	// watch never sees a gap between reading the runner position and the
	// row landing.
	observeMu sync.Mutex
	pos       int64
}

// AttachTarget binds the target to an identity incarnation and starts its
// runner. The runner resumes from the watches' own evaluated positions when
// they exist (restart recovery) and from the source end otherwise.
func (e *Executor) AttachTarget(ctx context.Context, targetID, identity string, source Source) error {
	if source == nil {
		return errors.New("watches: executor source is nil")
	}
	if err := e.engine.Attach(ctx, targetID, identity); err != nil {
		return err
	}
	pos, err := e.resumePosition(ctx, targetID, source)
	if err != nil {
		return err
	}
	runnerCtx, cancel := context.WithCancel(e.lifecycleCtx)
	runner := &targetRunner{
		targetID: targetID, identity: identity, source: source,
		cancel: cancel, done: make(chan struct{}), pos: pos, ownerCtx: e.ownerCtx,
	}
	e.mu.Lock()
	if _, exists := e.targets[targetID]; exists {
		e.mu.Unlock()
		cancel()
		return fmt.Errorf("watches: target %s already attached", targetID)
	}
	e.targets[targetID] = runner
	// A fresh attach supersedes any failed detach attempt.
	delete(e.pendingDetach, targetID)
	e.mu.Unlock()
	go runner.run(runnerCtx, e)
	return nil
}

// DetachTarget stops the runner and detaches the target durably. A failed
// permanent detach keeps the incarnation credential as a pending record, so
// the retry can complete the durable transition instead of reporting a
// false success.
func (e *Executor) DetachTarget(ctx context.Context, targetID string, permanent bool) error {
	e.mu.Lock()
	runner, ok := e.targets[targetID]
	if ok {
		delete(e.targets, targetID)
	}
	e.mu.Unlock()
	if runner != nil {
		runner.cancel()
		<-runner.done
		_ = runner.source.Close()
	}
	if !permanent {
		if runner == nil {
			return nil
		}
		return e.engine.Detach(ctx, targetID, runner.identity, false)
	}
	// Permanent detach needs the incarnation credential: from the live
	// mirror when present, else from the pending record of a failed attempt.
	identity, generation, attached := e.engine.Credential(ctx, targetID)
	if !attached {
		e.mu.Lock()
		pending, ok := e.pendingDetach[targetID]
		e.mu.Unlock()
		if !ok {
			return ErrDetached
		}
		identity, generation = pending.identity, pending.generation
	}
	settled, err := e.store.AgentWatchTargetDetach(ctx, targetID, identity, generation, e.opts.Now().UnixMilli())
	if err != nil {
		e.mu.Lock()
		e.pendingDetach[targetID] = pendingDetach{identity: identity, generation: generation}
		e.mu.Unlock()
		return err
	}
	if !settled {
		return ErrDetached
	}
	e.mu.Lock()
	delete(e.pendingDetach, targetID)
	e.mu.Unlock()
	return nil
}

// resumePosition picks the runner's start position: the smallest evaluated
// position across armed watches and matched repeat watches that can still
// re-arm, so a restart never shows them a gap, or the source end when no
// such watch exists.
func (e *Executor) resumePosition(ctx context.Context, targetID string, source Source) (int64, error) {
	watches, err := e.engine.ListByTarget(ctx, targetID)
	if err != nil {
		return 0, err
	}
	resume := int64(-1)
	for _, watch := range watches {
		if watch.Status != StatusArmed && !(watch.Status == StatusMatched && watch.Repeat) {
			continue
		}
		evaluated := watch.AnchorSeq + int64(len(watch.Carry))
		if resume < 0 || evaluated < resume {
			resume = evaluated
		}
	}
	if resume >= 0 {
		return resume, nil
	}
	return source.End(ctx)
}

// WatchParams mirrors CreateParams except the stream position and left
// context: the executor owns those from the target's live stream.
type WatchParams struct {
	ID        string
	TargetID  string
	Name      string
	Condition Condition
	Repeat    bool
	ExpiresAt time.Time
}

// Watch creates an armed watch anchored at the target's current stream
// position with the true left context from the source tail.
func (e *Executor) Watch(ctx context.Context, params WatchParams) (Watch, error) {
	e.mu.Lock()
	runner, ok := e.targets[params.TargetID]
	e.mu.Unlock()
	if !ok {
		return Watch{}, ErrDetached
	}
	runner.observeMu.Lock()
	defer runner.observeMu.Unlock()
	tail, err := runner.source.Tail(ctx, runner.pos, watchContextSize)
	if err != nil {
		return Watch{}, err
	}
	return e.engine.Create(ctx, CreateParams{
		ID: params.ID, TargetID: params.TargetID, Name: params.Name,
		Condition: params.Condition, Repeat: params.Repeat,
		AnchorSeq: runner.pos, LeftContext: tail, ExpiresAt: params.ExpiresAt,
	})
}

// Cancel settles a watch.
func (e *Executor) Cancel(ctx context.Context, watchID string, expectedRevision uint64) (Watch, error) {
	return e.engine.Cancel(ctx, watchID, expectedRevision)
}

func (r *targetRunner) run(ctx context.Context, e *Executor) {
	defer close(r.done)
	for {
		r.observeMu.Lock()
		pos := r.pos
		r.observeMu.Unlock()
		chunk, next, err := r.source.Chunk(ctx, pos)
		if err == io.EOF {
			// EOF evaluation gets the same retry discipline as chunk
			// observations: a transient store failure must not silently
			// drop the deferred events.
			for {
				_, observeErr := e.engine.ObserveEOF(r.ownerCtx, r.targetID, r.identity, pos)
				if observeErr == nil {
					return
				}
				if errors.Is(observeErr, ErrDetached) || errors.Is(observeErr, ErrIdentityMismatch) {
					return
				}
				if ctx.Err() != nil {
					return
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(e.opts.RetryBackoff):
				}
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(e.opts.RetryBackoff):
			}
			continue
		}
		if len(chunk) == 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(e.opts.RetryBackoff):
			}
			continue
		}
		r.observeMu.Lock()
		_, observeErr := e.engine.Observe(r.ownerCtx, r.targetID, r.identity, pos, chunk)
		if observeErr == nil {
			r.pos = next
		}
		r.observeMu.Unlock()
		if observeErr != nil {
			if errors.Is(observeErr, ErrDetached) || errors.Is(observeErr, ErrIdentityMismatch) {
				return
			}
			if ctx.Err() != nil {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(e.opts.RetryBackoff):
			}
			continue
		}
	}
}
