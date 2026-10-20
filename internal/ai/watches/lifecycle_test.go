package watches

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
)

// faultStore injects store failures into engine lifecycle paths to prove
// errors propagate as retryable failures instead of silent success with
// watches left armed.
type faultStore struct {
	Store
	mu        sync.Mutex
	detachErr error
	markErr   error
}

func (s *faultStore) AgentWatchTargetDetach(ctx context.Context, targetID, identity string, generation uint64, now int64) (bool, error) {
	s.mu.Lock()
	err := s.detachErr
	s.mu.Unlock()
	if err != nil {
		return false, err
	}
	return s.Store.AgentWatchTargetDetach(ctx, targetID, identity, generation, now)
}

func (s *faultStore) AgentWatchMarkUnverifiable(ctx context.Context, watchID string, expectedRevision uint64, cause string, now int64) (model.AgentWatchRow, error) {
	s.mu.Lock()
	err := s.markErr
	s.mu.Unlock()
	if err != nil {
		return model.AgentWatchRow{}, err
	}
	return s.Store.AgentWatchMarkUnverifiable(ctx, watchID, expectedRevision, cause, now)
}

func (s *faultStore) setDetachErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.detachErr = err
}

func (s *faultStore) setMarkErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.markErr = err
}

func TestObserveSplitContainsAcrossChunksFires(t *testing.T) {
	engine, _ := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	mustCreate(t, engine, ctx, "target-1", containsCondition("ready"))
	// Byte-at-a-time delivery: every chunk is a partial pattern.
	for index := 0; index < len("ready"); index++ {
		events, err := engine.Observe(ctx, "target-1", "identity-1", int64(index), []byte("ready"[index:index+1]))
		if err != nil {
			t.Fatal(err)
		}
		if index < len("ready")-1 && len(events) != 0 {
			t.Fatalf("chunk %d fired early: %+v", index, events)
		}
		if index == len("ready")-1 {
			if len(events) != 1 || events[0].MatchedFrom != 0 || events[0].MatchedTo != 5 {
				t.Fatalf("final chunk = %+v; want one match 0..5", events)
			}
		}
	}
}

func TestObserveSplitRegexAcrossChunksFires(t *testing.T) {
	engine, _ := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	if err := engine.Attach(ctx, "target-1", "identity-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Create(ctx, CreateParams{TargetID: "target-1",
		Condition: Condition{Type: ConditionRegex, Pattern: `error \d+`}}); err != nil {
		t.Fatal(err)
	}
	events, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("err"))
	if err != nil || len(events) != 0 {
		t.Fatalf("partial chunk = %+v, %v", events, err)
	}
	events, err = engine.Observe(ctx, "target-1", "identity-1", 3, []byte("or 42"))
	if err != nil || len(events) != 1 {
		t.Fatalf("completed chunk = %+v, %v", events, err)
	}
	if events[0].MatchedFrom != 0 || events[0].MatchedTo != 8 {
		t.Fatalf("match = %d..%d; want 0..8", events[0].MatchedFrom, events[0].MatchedTo)
	}
}

func TestObserveOverlapRedeliveryDoesNotDoubleCountSeam(t *testing.T) {
	engine, _ := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	mustCreate(t, engine, ctx, "target-1", containsCondition("ab"))
	if _, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("a")); err != nil {
		t.Fatal(err)
	}
	// The chunk [0,2) overlaps the evaluated [0,1): only byte 1 is new, so
	// the buffer is "a"+"b" — never "aab" — and the match fires exactly once.
	events, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("ab"))
	if err != nil || len(events) != 1 {
		t.Fatalf("overlap observe = %+v, %v", events, err)
	}
	if events[0].MatchedFrom != 0 || events[0].MatchedTo != 2 {
		t.Fatalf("overlap match = %d..%d; want 0..2", events[0].MatchedFrom, events[0].MatchedTo)
	}
}

func TestCarryStaysBoundedAndAnchorAdvances(t *testing.T) {
	clock := newTestClock()
	backend, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	engine, err := New(backend, Options{Now: clock.Now, MaxCarryBytes: 8})
	if err != nil {
		t.Fatal(err)
	}
	ctx := ownerCtx("owner-a")
	watch := mustCreate(t, engine, ctx, "target-1", Condition{Type: ConditionRegex, Pattern: `never-match-\d+`})
	chunk := []byte("0123456789abcdef")
	for index := 0; index < 20; index++ {
		if _, err := engine.Observe(ctx, "target-1", "identity-1", int64(index*len(chunk)), chunk); err != nil {
			t.Fatal(err)
		}
		after, err := engine.Get(ctx, watch.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(after.Carry) > 8 {
			t.Fatalf("carry grew to %d bytes; want <= 8", len(after.Carry))
		}
		wantAnchor := int64((index + 1) * len(chunk))
		if after.Carry != nil {
			wantAnchor -= int64(len(after.Carry))
		}
		if after.AnchorSeq != wantAnchor {
			t.Fatalf("chunk %d anchor = %d; want %d", index, after.AnchorSeq, wantAnchor)
		}
	}
}

func TestPermanentDetachStoreFailurePropagatesAndRetries(t *testing.T) {
	clock := newTestClock()
	backend, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	wrapped := &faultStore{Store: backend, detachErr: errors.New("injected detach failure")}
	engine, err := New(wrapped, Options{Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	ctx := ownerCtx("owner-a")
	watch := mustCreate(t, engine, ctx, "target-1", containsCondition("ready"))
	if err := engine.Detach(ctx, "target-1", "identity-1", true); err == nil {
		t.Fatal("permanent detach reported success while the store failed")
	}
	after, err := engine.Get(ctx, watch.ID)
	if err != nil || after.Status != StatusArmed {
		t.Fatalf("watch after failed detach = %+v, %v; want still armed and retryable", after, err)
	}
	wrapped.setDetachErr(nil)
	if err := engine.Detach(ctx, "target-1", "identity-1", true); err != nil {
		t.Fatalf("retried detach: %v", err)
	}
	after, err = engine.Get(ctx, watch.ID)
	if err != nil || after.Status != StatusUnverifiable {
		t.Fatalf("watch after retried detach = %+v, %v", after, err)
	}
}

func TestObserveMarkUnverifiableFailurePropagates(t *testing.T) {
	clock := newTestClock()
	backend, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	wrapped := &faultStore{Store: backend, markErr: errors.New("injected mark failure")}
	engine, err := New(wrapped, Options{Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	ctx := ownerCtx("owner-a")
	watch := mustCreate(t, engine, ctx, "target-1", containsCondition("ready"))
	if _, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("12345678")); err != nil {
		t.Fatal(err)
	}
	// A gap must surface the store failure instead of silently continuing
	// with the watch armed.
	if _, err := engine.Observe(ctx, "target-1", "identity-1", 100, []byte("x")); err == nil {
		t.Fatal("gap observe reported success while the settle failed")
	}
	after, err := engine.Get(ctx, watch.ID)
	if err != nil || after.Status != StatusArmed {
		t.Fatalf("watch after failed settle = %+v, %v", after, err)
	}
	wrapped.setMarkErr(nil)
	if _, err := engine.Observe(ctx, "target-1", "identity-1", 100, []byte("x")); err != nil {
		t.Fatalf("retried gap observe: %v", err)
	}
	after, err = engine.Get(ctx, watch.ID)
	if err != nil || after.Status != StatusUnverifiable {
		t.Fatalf("watch after retried settle = %+v, %v", after, err)
	}
}

func TestStalePresenterDoesNotSettleNewIncarnation(t *testing.T) {
	engine, backend := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	stale, err := New(backend, Options{Now: newTestClock().Now})
	if err != nil {
		t.Fatal(err)
	}
	old := mustCreate(t, engine, ctx, "target-1", containsCondition("ready"))
	if err := stale.Attach(ctx, "target-1", "identity-1"); err != nil {
		t.Fatal(err)
	}
	// A new incarnation replaces the identity and settles the old watch.
	if err := engine.Attach(ctx, "target-1", "identity-2"); err != nil {
		t.Fatal(err)
	}
	if after, err := engine.Get(ctx, old.ID); err != nil || after.Status != StatusUnverifiable {
		t.Fatalf("old watch after replacement = %+v, %v", after, err)
	}
	fresh, err := engine.Create(ctx, CreateParams{TargetID: "target-1", Condition: containsCondition("ready")})
	if err != nil || fresh.Identity != "identity-2" {
		t.Fatalf("fresh watch = %+v, %v", fresh, err)
	}
	// The stale engine mirrors the old incarnation: its Observe must be
	// rejected without settling the fresh watch.
	if _, err := stale.Observe(ctx, "target-1", "identity-1", 0, []byte("ready")); !errors.Is(err, ErrDetached) {
		t.Fatalf("stale observe = %v; want ErrDetached", err)
	}
	if after, err := engine.Get(ctx, fresh.ID); err != nil || after.Status != StatusArmed {
		t.Fatalf("fresh watch after stale observe = %+v, %v", after, err)
	}
	// The stale engine can re-attach and then evaluate normally.
	if err := stale.Attach(ctx, "target-1", "identity-2"); err != nil {
		t.Fatal(err)
	}
	events, err := stale.Observe(ctx, "target-1", "identity-2", 0, []byte("ready"))
	if err != nil || len(events) != 1 {
		t.Fatalf("reattached observe = %+v, %v", events, err)
	}
}

func TestRepeatWatchConsumesStreamWhileMatched(t *testing.T) {
	engine, backend := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	if err := engine.Attach(ctx, "target-1", "identity-1"); err != nil {
		t.Fatal(err)
	}
	watch, err := engine.Create(ctx, CreateParams{TargetID: "target-1", Condition: containsCondition("ready"), Repeat: true})
	if err != nil {
		t.Fatal(err)
	}
	events, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("ready"))
	if err != nil || len(events) != 1 {
		t.Fatalf("fire = %+v, %v", events, err)
	}
	// Output keeps arriving while the fire awaits acknowledgement; the
	// suspended watch consumes it without matching.
	if _, err := engine.Observe(ctx, "target-1", "identity-1", 5, []byte("noise-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Observe(ctx, "target-1", "identity-1", 11, []byte("noise-2")); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.AgentWatchEventAck(ctx, events[0].DeliveryID, 1, 5000); err != nil {
		t.Fatal(err)
	}
	rearmed, err := engine.Get(ctx, watch.ID)
	if err != nil || rearmed.Status != StatusArmed || rearmed.AnchorSeq != 18 {
		t.Fatalf("re-armed = %+v, %v", rearmed, err)
	}
	// Resuming from the consumed anchor is not a gap; the next match fires.
	events, err = engine.Observe(ctx, "target-1", "identity-1", 18, []byte("ready"))
	if err != nil || len(events) != 1 {
		t.Fatalf("resumed observe = %+v, %v", events, err)
	}
	if events[0].MatchedFrom != 18 {
		t.Fatalf("resumed match from %d; want 18", events[0].MatchedFrom)
	}
}

func TestConcurrentLifecycleHammer(t *testing.T) {
	engine, _ := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	const workers = 4
	var wg sync.WaitGroup
	errs := make([]error, 0, workers*64)
	var errsMu sync.Mutex
	record := func(err error) {
		if err == nil || errors.Is(err, ErrDetached) || errors.Is(err, ErrIdentityMismatch) || errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
			return
		}
		errsMu.Lock()
		errs = append(errs, err)
		errsMu.Unlock()
	}
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			target := "target-1"
			identity := []string{"identity-1", "identity-2"}[worker%2]
			for round := 0; round < 32; round++ {
				record(engine.Attach(ctx, target, identity))
				_, err := engine.Create(ctx, CreateParams{TargetID: target, Condition: containsCondition("ready")})
				record(err)
				_, err = engine.Observe(ctx, target, identity, 0, []byte("ready"))
				record(err)
				record(engine.Detach(ctx, target, identity, round%3 == 0))
				_, err = engine.Observe(ctx, target, identity, 0, []byte("more output"))
				record(err)
			}
		}(worker)
	}
	wg.Wait()
	if len(errs) != 0 {
		t.Fatalf("unexpected lifecycle errors: %v", errs)
	}
	// Quiesce: attach once and verify persisted state stays coherent.
	if err := engine.Attach(ctx, "target-1", "identity-final"); err != nil {
		t.Fatal(err)
	}
	watches, err := engine.ListByTarget(ctx, "target-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, watch := range watches {
		if watch.Generation == 0 || watch.Identity == "" {
			t.Fatalf("watch %+v lost its durable binding", watch)
		}
		if watch.Status == StatusArmed && watch.Identity != "identity-final" {
			t.Fatalf("watch %s survived as armed under outdated identity %s", watch.ID, watch.Identity)
		}
		if strings.Contains(watch.LastError, "identity replaced") && watch.Status != StatusUnverifiable {
			t.Fatalf("watch %s carries a settle cause without being settled", watch.ID)
		}
	}
}
