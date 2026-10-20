package watches

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock {
	return &testClock{now: time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newTestEngine(t *testing.T, clock *testClock) (*Engine, *store.Store) {
	t.Helper()
	backend, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatalf("open in-memory store: %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	engine, err := New(backend, Options{Now: clock.Now})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	return engine, backend
}

func ownerCtx(owner string) context.Context {
	return ipc.WithUserID(context.Background(), owner)
}

func mustCreate(t *testing.T, engine *Engine, ctx context.Context, target string, condition Condition) Watch {
	t.Helper()
	if err := engine.Attach(ctx, target, "identity-1"); err != nil {
		t.Fatal(err)
	}
	watch, err := engine.Create(ctx, CreateParams{TargetID: target, Condition: condition})
	if err != nil {
		t.Fatal(err)
	}
	return watch
}

func containsCondition(pattern string) Condition {
	return Condition{Type: ConditionContains, Pattern: pattern}
}

func TestCreateRequiresAttachedTarget(t *testing.T) {
	engine, _ := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	if _, err := engine.Create(ctx, CreateParams{TargetID: "target-1", Condition: containsCondition("ready")}); !errors.Is(err, ErrDetached) {
		t.Fatalf("create without attach = %v; want ErrDetached", err)
	}
	if err := engine.Attach(ctx, "target-1", "identity-1"); err != nil {
		t.Fatal(err)
	}
	watch, err := engine.Create(ctx, CreateParams{TargetID: "target-1", Name: "w", Condition: containsCondition("ready")})
	if err != nil {
		t.Fatal(err)
	}
	if watch.Status != StatusArmed || watch.Identity != "identity-1" || watch.Revision != 1 || watch.OwnerID != "owner-a" {
		t.Fatalf("created = %+v", watch)
	}
	if err := engine.Detach(ctx, "target-1", "identity-1", false); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Create(ctx, CreateParams{TargetID: "target-1", Condition: containsCondition("ready")}); !errors.Is(err, ErrDetached) {
		t.Fatalf("create on detached target = %v; want ErrDetached", err)
	}
}

func TestCreateValidatesCondition(t *testing.T) {
	engine, _ := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	if err := engine.Attach(ctx, "target-1", "identity-1"); err != nil {
		t.Fatal(err)
	}
	for name, condition := range map[string]Condition{
		"empty pattern": {Type: ConditionContains},
		"unknown type":  {Type: "magic", Pattern: "x"},
		"bad regex":     {Type: ConditionRegex, Pattern: "("},
	} {
		if _, err := engine.Create(ctx, CreateParams{TargetID: "target-1", Condition: condition}); !errors.Is(err, ErrInvalidCondition) {
			t.Fatalf("%s = %v; want ErrInvalidCondition", name, err)
		}
	}
}

func TestObserveFiresWithFixedIDsAndAdvancesAnchor(t *testing.T) {
	engine, backend := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	watch := mustCreate(t, engine, ctx, "target-1", containsCondition("ready"))

	events, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("not yet\n"))
	if err != nil || len(events) != 0 {
		t.Fatalf("miss observe = %+v, %v", events, err)
	}
	afterMiss, err := engine.Get(ctx, watch.ID)
	if err != nil || afterMiss.AnchorSeq != 4 || afterMiss.Revision != 2 || string(afterMiss.Carry) != "yet\n" {
		t.Fatalf("after miss = %+v, %v", afterMiss, err)
	}

	events, err = engine.Observe(ctx, "target-1", "identity-1", 8, []byte("xxready\n"))
	if err != nil || len(events) != 1 {
		t.Fatalf("fire observe = %+v, %v", events, err)
	}
	event := events[0]
	if event.Kind != EventKind || event.DeliveryID == "" || event.RunID == "" ||
		event.WatchID != watch.ID || event.TargetID != "target-1" || event.Identity != "identity-1" ||
		event.MatchedFrom != 10 || event.MatchedTo != 15 || !strings.Contains(event.Excerpt, "ready") {
		t.Fatalf("event = %+v", event)
	}
	if event.DeliveryID == event.RunID {
		t.Fatal("DeliveryID and RunID must be distinct fixed identities")
	}
	afterFire, err := engine.Get(ctx, watch.ID)
	if err != nil || afterFire.Status != StatusMatched || afterFire.AnchorSeq != 16 || afterFire.Revision != 3 ||
		afterFire.DeliveryID != event.DeliveryID || afterFire.RunID != event.RunID || afterFire.FireSeq != 1 {
		t.Fatalf("after fire = %+v, %v", afterFire, err)
	}
	row, err := backend.AgentWatchEventGet(ctx, event.DeliveryID)
	if err != nil || row.Status != store.AgentWatchEventPending {
		t.Fatalf("outbox row = %+v, %v", row, err)
	}
}

func TestObserveOverlapRedeliveryIsNoop(t *testing.T) {
	engine, _ := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	mustCreate(t, engine, ctx, "target-1", containsCondition("ready"))
	if _, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("ready")); err != nil {
		t.Fatal(err)
	}
	events, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("ready"))
	if err != nil || len(events) != 0 {
		t.Fatalf("redelivered chunk produced %+v, %v; want no events", events, err)
	}
}

func TestObserveGapMarksUnverifiable(t *testing.T) {
	engine, _ := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	watch := mustCreate(t, engine, ctx, "target-1", containsCondition("ready"))
	if _, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("12345678")); err != nil {
		t.Fatal(err)
	}
	events, err := engine.Observe(ctx, "target-1", "identity-1", 20, []byte("ready"))
	if err != nil || len(events) != 0 {
		t.Fatalf("gap observe = %+v, %v", events, err)
	}
	after, err := engine.Get(ctx, watch.ID)
	if err != nil || after.Status != StatusUnverifiable || !strings.Contains(after.LastError, "gap") {
		t.Fatalf("after gap = %+v, %v", after, err)
	}
	events, err = engine.Observe(ctx, "target-1", "identity-1", 25, []byte("ready"))
	if err != nil || len(events) != 0 {
		t.Fatalf("unverifiable watch fired: %+v, %v", events, err)
	}
}

func TestObserveRejectsUnknownIdentityAndDetachedTarget(t *testing.T) {
	engine, _ := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	watch := mustCreate(t, engine, ctx, "target-1", containsCondition("ready"))
	if _, err := engine.Observe(ctx, "target-1", "identity-2", 0, []byte("ready")); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("foreign identity observe = %v; want ErrIdentityMismatch", err)
	}
	if err := engine.Detach(ctx, "target-1", "identity-1", false); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("ready")); !errors.Is(err, ErrDetached) {
		t.Fatalf("detached observe = %v; want ErrDetached", err)
	}
	after, err := engine.Get(ctx, watch.ID)
	if err != nil || after.Status != StatusArmed || after.AnchorSeq != 0 {
		t.Fatalf("watch changed by rejected observe: %+v, %v", after, err)
	}
}

func TestDetachReversibleResumesFromAnchor(t *testing.T) {
	engine, _ := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	watch := mustCreate(t, engine, ctx, "target-1", containsCondition("ready"))
	if _, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("1234")); err != nil {
		t.Fatal(err)
	}
	if err := engine.Detach(ctx, "target-1", "identity-1", false); err != nil {
		t.Fatal(err)
	}
	if err := engine.Attach(ctx, "target-1", "identity-1"); err != nil {
		t.Fatal(err)
	}
	events, err := engine.Observe(ctx, "target-1", "identity-1", 4, []byte("ready"))
	if err != nil || len(events) != 1 {
		t.Fatalf("resumed observe = %+v, %v", events, err)
	}
	if events[0].MatchedFrom != 4 {
		t.Fatalf("resumed match from %d; want 4", events[0].MatchedFrom)
	}
	after, err := engine.Get(ctx, watch.ID)
	if err != nil || after.Status != StatusMatched {
		t.Fatalf("after resume = %+v, %v", after, err)
	}
}

func TestDetachPermanentMarksUnverifiable(t *testing.T) {
	engine, _ := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	watch := mustCreate(t, engine, ctx, "target-1", containsCondition("ready"))
	if err := engine.Detach(ctx, "target-1", "identity-1", true); err != nil {
		t.Fatal(err)
	}
	after, err := engine.Get(ctx, watch.ID)
	if err != nil || after.Status != StatusUnverifiable {
		t.Fatalf("after permanent detach = %+v, %v", after, err)
	}
	if _, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("ready")); !errors.Is(err, ErrDetached) {
		t.Fatalf("observe after permanent detach = %v; want ErrDetached", err)
	}
}

func TestStaleDetachDoesNotTearDownNewerAttachment(t *testing.T) {
	engine, _ := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	if err := engine.Attach(ctx, "target-1", "identity-1"); err != nil {
		t.Fatal(err)
	}
	if err := engine.Attach(ctx, "target-1", "identity-2"); err != nil {
		t.Fatal(err)
	}
	if err := engine.Detach(ctx, "target-1", "identity-1", true); err != nil {
		t.Fatal(err)
	}
	watch, err := engine.Create(ctx, CreateParams{TargetID: "target-1", Condition: containsCondition("ready")})
	if err != nil || watch.Identity != "identity-2" {
		t.Fatalf("create after stale detach = %+v, %v", watch, err)
	}
	events, err := engine.Observe(ctx, "target-1", "identity-2", 0, []byte("ready"))
	if err != nil || len(events) != 1 {
		t.Fatalf("observe after stale detach = %+v, %v", events, err)
	}
}

func TestAttachReplacementInvalidatesOldIdentityWatches(t *testing.T) {
	engine, _ := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	old := mustCreate(t, engine, ctx, "target-1", containsCondition("ready"))
	if err := engine.Attach(ctx, "target-1", "identity-2"); err != nil {
		t.Fatal(err)
	}
	after, err := engine.Get(ctx, old.ID)
	if err != nil || after.Status != StatusUnverifiable {
		t.Fatalf("old-identity watch = %+v, %v", after, err)
	}
	events, err := engine.Observe(ctx, "target-1", "identity-2", 0, []byte("ready"))
	if err != nil || len(events) != 0 {
		t.Fatalf("old watch fired under new identity: %+v, %v", events, err)
	}
}

func TestCancelPreventsFire(t *testing.T) {
	engine, backend := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	watch := mustCreate(t, engine, ctx, "target-1", containsCondition("ready"))
	cancelled, err := engine.Cancel(ctx, watch.ID, watch.Revision)
	if err != nil || cancelled.Status != StatusCancelled {
		t.Fatalf("cancel = %+v, %v", cancelled, err)
	}
	events, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("ready"))
	if err != nil || len(events) != 0 {
		t.Fatalf("cancelled watch fired: %+v, %v", events, err)
	}
	pending, err := backend.AgentWatchEventPendingList(ctx, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after cancel = %+v, %v", pending, err)
	}
	if _, err := engine.Cancel(ctx, watch.ID, 99); err != nil {
		t.Fatalf("idempotent cancel = %v", err)
	}
}

func TestCancelPendingEventStopsDelivery(t *testing.T) {
	engine, backend := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	watch := mustCreate(t, engine, ctx, "target-1", containsCondition("ready"))
	events, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("ready"))
	if err != nil || len(events) != 1 {
		t.Fatalf("fire = %+v, %v", events, err)
	}
	afterFire, err := engine.Get(ctx, watch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Cancel(ctx, watch.ID, afterFire.Revision); err != nil {
		t.Fatal(err)
	}
	pending, err := backend.AgentWatchEventPendingList(ctx, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after cancel = %+v, %v; want empty", pending, err)
	}
	event, err := backend.AgentWatchEventGet(ctx, events[0].DeliveryID)
	if err != nil || event.Status != store.AgentWatchEventCancelled {
		t.Fatalf("event after cancel = %+v, %v", event, err)
	}
}

func TestRepeatWatchReArmsOnAck(t *testing.T) {
	engine, backend := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	if err := engine.Attach(ctx, "target-1", "identity-1"); err != nil {
		t.Fatal(err)
	}
	watch, err := engine.Create(ctx, CreateParams{TargetID: "target-1", Condition: containsCondition("ready"), Repeat: true})
	if err != nil {
		t.Fatal(err)
	}
	first, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("ready"))
	if err != nil || len(first) != 1 {
		t.Fatalf("first fire = %+v, %v", first, err)
	}
	if _, err := backend.AgentWatchEventAck(ctx, first[0].DeliveryID, 1, 5000); err != nil {
		t.Fatal(err)
	}
	rearmed, err := engine.Get(ctx, watch.ID)
	if err != nil || rearmed.Status != StatusArmed || rearmed.AnchorSeq != 5 || rearmed.FireSeq != 1 {
		t.Fatalf("re-armed = %+v, %v", rearmed, err)
	}
	second, err := engine.Observe(ctx, "target-1", "identity-1", 5, []byte("ready"))
	if err != nil || len(second) != 1 {
		t.Fatalf("second fire = %+v, %v", second, err)
	}
	if second[0].DeliveryID == first[0].DeliveryID || second[0].RunID == first[0].RunID {
		t.Fatal("repeat fire reused fixed delivery/run IDs")
	}
	afterSecond, err := engine.Get(ctx, watch.ID)
	if err != nil || afterSecond.FireSeq != 2 || afterSecond.Status != StatusMatched {
		t.Fatalf("after second fire = %+v, %v", afterSecond, err)
	}
	if _, err := engine.Cancel(ctx, watch.ID, afterSecond.Revision); err != nil {
		t.Fatal(err)
	}
}

func TestSweepExpired(t *testing.T) {
	clock := newTestClock()
	engine, _ := newTestEngine(t, clock)
	ctx := ownerCtx("owner-a")
	if err := engine.Attach(ctx, "target-1", "identity-1"); err != nil {
		t.Fatal(err)
	}
	expired, err := engine.Create(ctx, CreateParams{TargetID: "target-1", Condition: containsCondition("ready"),
		ExpiresAt: clock.Now().Add(-time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	active, err := engine.Create(ctx, CreateParams{TargetID: "target-1", Condition: containsCondition("ready"),
		ExpiresAt: clock.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	count, err := engine.SweepExpired(ctx)
	if err != nil || count != 1 {
		t.Fatalf("sweep = %d, %v; want 1", count, err)
	}
	after, err := engine.Get(ctx, expired.ID)
	if err != nil || after.Status != StatusExpired {
		t.Fatalf("expired watch = %+v, %v", after, err)
	}
	events, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("ready"))
	if err != nil || len(events) != 1 {
		t.Fatalf("active watch after sweep = %+v, %v", events, err)
	}
	stillActive, err := engine.Get(ctx, active.ID)
	if err != nil || stillActive.Status != StatusMatched {
		t.Fatalf("active watch = %+v, %v", stillActive, err)
	}
}

func TestEngineOwnerIsolation(t *testing.T) {
	engine, _ := newTestEngine(t, newTestClock())
	ctxA := ownerCtx("owner-a")
	ctxB := ownerCtx("owner-b")
	watch := mustCreate(t, engine, ctxA, "target-1", containsCondition("ready"))
	if _, err := engine.Get(ctxB, watch.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner get = %v; want ErrNotFound", err)
	}
	if _, err := engine.Cancel(ctxB, watch.ID, watch.Revision); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner cancel = %v; want ErrNotFound", err)
	}
	watches, err := engine.ListByTarget(ctxB, "target-1")
	if err != nil || len(watches) != 0 {
		t.Fatalf("cross-owner list = %+v, %v", watches, err)
	}
	// B has its own mirror and durable row for the shared target ID: its
	// observe is evaluated only against B's watches and never fires or
	// settles owner A's watch.
	if err := engine.Attach(ctxB, "target-1", "identity-1"); err != nil {
		t.Fatal(err)
	}
	events, err := engine.Observe(ctxB, "target-1", "identity-1", 0, []byte("ready"))
	if err != nil || len(events) != 0 {
		t.Fatalf("cross-owner observe fired owner-a watch: %+v, %v", events, err)
	}
	after, err := engine.Get(ctxA, watch.ID)
	if err != nil || after.Status != StatusArmed || after.AnchorSeq != 0 {
		t.Fatalf("owner-a watch after cross-owner observe = %+v, %v", after, err)
	}
}

func TestObserveEvaluatesWatchesIndependently(t *testing.T) {
	engine, _ := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	if err := engine.Attach(ctx, "target-1", "identity-1"); err != nil {
		t.Fatal(err)
	}
	matching, err := engine.Create(ctx, CreateParams{TargetID: "target-1", Condition: containsCondition("ready")})
	if err != nil {
		t.Fatal(err)
	}
	missing, err := engine.Create(ctx, CreateParams{TargetID: "target-1", Condition: containsCondition("error")})
	if err != nil {
		t.Fatal(err)
	}
	events, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("all ready"))
	if err != nil || len(events) != 1 || events[0].WatchID != matching.ID {
		t.Fatalf("observe = %+v, %v", events, err)
	}
	afterMissing, err := engine.Get(ctx, missing.ID)
	if err != nil || afterMissing.Status != StatusArmed || afterMissing.AnchorSeq != 5 {
		t.Fatalf("non-matching watch = %+v, %v", afterMissing, err)
	}
}

func TestRegexConditionAndBoundedExcerpt(t *testing.T) {
	clock := newTestClock()
	backend, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	engine, err := New(backend, Options{Now: clock.Now, MaxExcerptBytes: 16})
	if err != nil {
		t.Fatal(err)
	}
	ctx := ownerCtx("owner-a")
	if err := engine.Attach(ctx, "target-1", "identity-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Create(ctx, CreateParams{TargetID: "target-1",
		Condition: Condition{Type: ConditionRegex, Pattern: `error \d+`}}); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("a", 40)
	events, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte(long+" error 42\n"))
	if err != nil || len(events) != 1 {
		t.Fatalf("regex observe = %+v, %v", events, err)
	}
	excerpt := events[0].Excerpt
	if len(excerpt) > 16+len("…[truncated]") || !strings.HasSuffix(excerpt, "…[truncated]") {
		t.Fatalf("excerpt not bounded: %q", excerpt)
	}
	if events[0].MatchedFrom != 41 || events[0].MatchedTo != 49 {
		t.Fatalf("match range = %d..%d; want 41..49", events[0].MatchedFrom, events[0].MatchedTo)
	}
}

func TestConcurrentObserveFiresExactlyOnce(t *testing.T) {
	engine, backend := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	second, err := New(backend, Options{Now: newTestClock().Now})
	if err != nil {
		t.Fatal(err)
	}
	mustCreate(t, engine, ctx, "target-1", containsCondition("ready"))
	if err := second.Attach(ctx, "target-1", "identity-1"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make([][]Event, 2)
	errs := make([]error, 2)
	for index, eng := range []*Engine{engine, second} {
		wg.Add(1)
		go func(index int, eng *Engine) {
			defer wg.Done()
			events, err := eng.Observe(ctx, "target-1", "identity-1", 0, []byte("ready"))
			results[index] = events
			errs[index] = err
		}(index, eng)
	}
	wg.Wait()
	total := 0
	for index := range results {
		if errs[index] != nil {
			t.Fatalf("concurrent observe %d: %v", index, errs[index])
		}
		total += len(results[index])
	}
	if total != 1 {
		t.Fatalf("concurrent observe produced %d events; want exactly 1", total)
	}
	pending, err := backend.AgentWatchEventPendingList(ctx, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending after concurrent observe = %+v, %v; want exactly 1", pending, err)
	}
}

func TestConditionEvaluate(t *testing.T) {
	condition := containsCondition("ready")
	matched, from, to, excerpt := condition.Evaluate([]byte("xxready\n"), 0, 0)
	if !matched || from != 2 || to != 7 || excerpt != "xxready" {
		t.Fatalf("evaluate = %v %d %d %q", matched, from, to, excerpt)
	}
	if matched, _, _, _ = condition.Evaluate([]byte("nope"), 0, 0); matched {
		t.Fatal("miss evaluated as match")
	}
	if matched, _, _, _ = condition.Evaluate([]byte("ready"), 1, 0); matched {
		t.Fatal("match before minStart evaluated as match")
	}
}
