package execbatch

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestCancelOwnerCancelsOnlyThatOwnersActiveBatches(t *testing.T) {
	started := map[string]chan context.Context{
		"a-one": make(chan context.Context, 1),
		"a-two": make(chan context.Context, 1),
		"b":     make(chan context.Context, 1),
		"open":  make(chan context.Context, 1),
	}
	resolver := &fakeResolver{targets: make(map[string]Target)}
	for id := range started {
		id := id
		resolver.targets[id] = targetFor(id, newFakeTransport(func(ctx context.Context, _ string, _ base.ExecOptions) (base.ExecResult, error) {
			started[id] <- ctx
			<-ctx.Done()
			return base.ExecResult{}, ctx.Err()
		}))
	}
	service := New(Config{Resolver: resolver})
	type outcome struct {
		batch Batch
		err   error
	}
	done := map[string]chan outcome{
		"a-one": make(chan outcome, 1),
		"a-two": make(chan outcome, 1),
		"b":     make(chan outcome, 1),
		"open":  make(chan outcome, 1),
	}
	owners := map[string]string{"a-one": "owner-a", "a-two": "owner-a", "b": "owner-b", "open": ""}
	for id := range done {
		id := id
		go func() {
			batch, err := service.Execute(t.Context(), owners[id], Request{BatchID: id, SessionIDs: []string{id}, Command: "wait"})
			done[id] <- outcome{batch: batch, err: err}
		}()
	}
	observed := make(map[string]context.Context, len(started))
	for id, ch := range started {
		select {
		case observed[id] = <-ch:
		case <-time.After(2 * time.Second):
			t.Fatalf("%s did not start", id)
		}
	}

	service.CancelOwner(t.Context(), "", "open-session", nil)
	service.CancelOwner(t.Context(), "unknown", "unknown-session", nil)
	for id, ctx := range observed {
		if ctx.Err() != nil {
			t.Fatalf("empty or unknown owner cleanup canceled %s", id)
		}
	}

	service.CancelOwner(t.Context(), "owner-a", "session-a", nil)
	for _, id := range []string{"a-one", "a-two"} {
		select {
		case result := <-done[id]:
			if result.err != nil || len(result.batch.Results) != 1 || result.batch.Results[0].Status != StatusCanceled {
				t.Fatalf("%s outcome = %+v, %v", id, result.batch, result.err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s did not finish after owner cleanup", id)
		}
	}
	for _, id := range []string{"b", "open"} {
		if observed[id].Err() != nil {
			t.Fatalf("owner A cleanup canceled %s", id)
		}
	}

	service.CancelOwner(t.Context(), "owner-b", "session-b", nil)
	select {
	case result := <-done["b"]:
		if result.err != nil || result.batch.Results[0].Status != StatusCanceled {
			t.Fatalf("owner B outcome = %+v, %v", result.batch, result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("owner B batch did not finish")
	}
	if _, err := service.Cancel(t.Context(), "", "open"); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done["open"]:
		if result.err != nil || result.batch.Results[0].Status != StatusCanceled {
			t.Fatalf("open batch outcome = %+v, %v", result.batch, result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("open batch did not finish")
	}
}

func TestCancelOwnerDeduplicatesConcurrentAndLateSessionCleanup(t *testing.T) {
	oldStarted := make(chan context.Context, 1)
	newStarted := make(chan context.Context, 1)
	allowOldReturn := make(chan struct{})
	resolver := &fakeResolver{targets: map[string]Target{
		"old": targetFor("old", newFakeTransport(func(ctx context.Context, _ string, _ base.ExecOptions) (base.ExecResult, error) {
			oldStarted <- ctx
			<-ctx.Done()
			<-allowOldReturn
			return base.ExecResult{}, ctx.Err()
		})),
		"new": targetFor("new", newFakeTransport(func(ctx context.Context, _ string, _ base.ExecOptions) (base.ExecResult, error) {
			newStarted <- ctx
			<-ctx.Done()
			return base.ExecResult{}, ctx.Err()
		})),
	}}
	service := New(Config{Resolver: resolver})
	type outcome struct {
		batch Batch
		err   error
	}
	oldDone := make(chan outcome, 1)
	go func() {
		batch, err := service.Execute(t.Context(), "owner-a", Request{BatchID: "old-batch", SessionIDs: []string{"old"}, Command: "wait"})
		oldDone <- outcome{batch: batch, err: err}
	}()
	var oldCtx context.Context
	select {
	case oldCtx = <-oldStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("old batch did not start")
	}
	firstCleanup := make(chan struct{})
	go func() {
		service.CancelOwner(t.Context(), "owner-a", "logout-all:session-s1", nil, "session-s1", "session-s2")
		close(firstCleanup)
	}()
	select {
	case <-oldCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("first cleanup did not cancel the old Exec")
	}

	newDone := make(chan outcome, 1)
	go func() {
		batch, err := service.Execute(t.Context(), "owner-a", Request{BatchID: "new-batch", SessionIDs: []string{"new"}, Command: "wait"})
		newDone <- outcome{batch: batch, err: err}
	}()
	var newCtx context.Context
	select {
	case newCtx = <-newStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("new batch did not start after the first snapshot")
	}
	secondCalling := make(chan struct{})
	secondCleanup := make(chan struct{})
	go func() {
		close(secondCalling)
		service.CancelOwner(t.Context(), "owner-a", "session-s2", nil)
		close(secondCleanup)
	}()
	<-secondCalling
	close(allowOldReturn)
	for name, done := range map[string]chan struct{}{"first": firstCleanup, "second": secondCleanup} {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("%s cleanup did not finish", name)
		}
	}
	select {
	case result := <-oldDone:
		if result.err != nil || len(result.batch.Results) != 1 || result.batch.Results[0].Status != StatusCanceled {
			t.Fatalf("old batch outcome = %+v, %v", result.batch, result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("old batch did not finish")
	}

	service.CancelOwner(t.Context(), "owner-a", "session-s1", nil)
	if newCtx.Err() != nil {
		t.Fatal("concurrent or late cleanup of the old session canceled the new batch")
	}
	service.CancelOwner(t.Context(), "owner-a", "session-new", nil)
	select {
	case result := <-newDone:
		if result.err != nil || len(result.batch.Results) != 1 || result.batch.Results[0].Status != StatusCanceled {
			t.Fatalf("new session cleanup outcome = %+v, %v", result.batch, result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("different session cleanup did not cancel the new batch")
	}
}

type cleanupTestClock struct{ now atomic.Int64 }

func newCleanupTestClock() *cleanupTestClock {
	clock := &cleanupTestClock{}
	clock.now.Store(time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC).UnixMilli())
	return clock
}

func (c *cleanupTestClock) Now() time.Time { return time.UnixMilli(c.now.Load()) }

func (c *cleanupTestClock) Advance(duration time.Duration) { c.now.Add(duration.Milliseconds()) }

func cleanupEvent(t *testing.T, service *Service, ownerID, eventID string) *accountCleanupEvent {
	t.Helper()
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.accountCleanups[accountCleanupKey{ownerID: ownerID, eventID: eventID}]
}

// A replay of a committed cleanup event must dedup on the committed marker
// after the batch payload was dropped at commit: it neither pins batches nor
// cancels a new generation.
func TestOwnerCleanupReplayAfterDropDoesNotCancelNewGeneration(t *testing.T) {
	oldStarted := make(chan context.Context, 1)
	newStarted := make(chan context.Context, 1)
	resolver := &fakeResolver{targets: map[string]Target{
		"old": targetFor("old", newFakeTransport(func(ctx context.Context, _ string, _ base.ExecOptions) (base.ExecResult, error) {
			oldStarted <- ctx
			<-ctx.Done()
			return base.ExecResult{}, ctx.Err()
		})),
		"new": targetFor("new", newFakeTransport(func(ctx context.Context, _ string, _ base.ExecOptions) (base.ExecResult, error) {
			newStarted <- ctx
			<-ctx.Done()
			return base.ExecResult{}, ctx.Err()
		})),
	}}
	service := New(Config{Resolver: resolver})
	oldDone := make(chan struct{}, 1)
	go func() {
		defer func() { oldDone <- struct{}{} }()
		_, _ = service.Execute(t.Context(), "owner-a", Request{BatchID: "old-batch", SessionIDs: []string{"old"}, Command: "wait"})
	}()
	var oldCtx context.Context
	select {
	case oldCtx = <-oldStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("old batch did not start")
	}
	service.CancelOwner(t.Context(), "owner-a", "logout-all:s1", nil, "session-s1")
	select {
	case <-oldDone:
	case <-time.After(2 * time.Second):
		t.Fatal("old batch did not finish after the cleanup commit")
	}
	if oldCtx.Err() == nil {
		t.Fatal("cleanup did not cancel the old batch")
	}
	event := cleanupEvent(t, service, "owner-a", "logout-all:s1")
	if event == nil || !event.committed || event.batches != nil {
		t.Fatalf("committed event = %+v, want committed with the batch payload dropped", event)
	}

	newDone := make(chan struct{}, 1)
	go func() {
		defer func() { newDone <- struct{}{} }()
		_, _ = service.Execute(t.Context(), "owner-a", Request{BatchID: "new-batch", SessionIDs: []string{"new"}, Command: "wait"})
	}()
	var newCtx context.Context
	select {
	case newCtx = <-newStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("new batch did not start")
	}
	service.CancelOwner(t.Context(), "owner-a", "logout-all:s1", nil, "session-s1")
	if newCtx.Err() != nil {
		t.Fatal("replay of the committed cleanup canceled the new generation batch")
	}
	if _, err := service.Cancel(t.Context(), "owner-a", "new-batch"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-newDone:
	case <-time.After(2 * time.Second):
		t.Fatal("new batch did not finish")
	}
}

// The cleanup snapshot is bounded by the durable generation cutoff: batches
// started before it (and exactly at it, same-millisecond inclusive) are
// canceled; batches started after it survive.
func TestOwnerCleanupSnapshotBoundedByCutoff(t *testing.T) {
	clock := newCleanupTestClock()
	started := map[string]chan context.Context{
		"old":     make(chan context.Context, 1),
		"same-ms": make(chan context.Context, 1),
		"new":     make(chan context.Context, 1),
	}
	resolver := &fakeResolver{targets: make(map[string]Target)}
	for id := range started {
		id := id
		resolver.targets[id] = targetFor(id, newFakeTransport(func(ctx context.Context, _ string, _ base.ExecOptions) (base.ExecResult, error) {
			started[id] <- ctx
			<-ctx.Done()
			return base.ExecResult{}, ctx.Err()
		}))
	}
	service := New(Config{Resolver: resolver, Now: clock.Now})
	launch := func(batchID, targetID string) context.Context {
		done := make(chan struct{}, 1)
		go func() {
			defer func() { done <- struct{}{} }()
			_, _ = service.Execute(t.Context(), "owner-a", Request{BatchID: batchID, SessionIDs: []string{targetID}, Command: "wait"})
		}()
		select {
		case ctx := <-started[targetID]:
			return ctx
		case <-time.After(2 * time.Second):
			t.Fatalf("batch %s did not start", batchID)
			return nil
		}
	}
	oldCtx := launch("batch-old", "old")
	clock.Advance(time.Millisecond)
	sameMSCtx := launch("batch-same-ms", "same-ms")
	cutoff := clock.Now()
	clock.Advance(time.Millisecond)
	newCtx := launch("batch-new", "new")

	service.CancelOwner(t.Context(), "owner-a", "session-s1", &cutoff)
	for name, ctx := range map[string]context.Context{"old": oldCtx, "same-ms": sameMSCtx} {
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
			t.Fatalf("%s batch (started at or before the cutoff) was not canceled", name)
		}
	}
	if newCtx.Err() != nil {
		t.Fatal("batch started after the cutoff was canceled")
	}
	if _, err := service.Cancel(t.Context(), "owner-a", "batch-new"); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerCleanupCommittedEventPurgesAfterTTL(t *testing.T) {
	clock := newCleanupTestClock()
	service := New(Config{Resolver: &fakeResolver{targets: map[string]Target{}}, Now: clock.Now})
	service.CancelOwner(t.Context(), "owner-a", "session-old", nil, "session-old-alias")
	clock.Advance(accountCleanupEventTTL + time.Millisecond)
	service.CancelOwner(t.Context(), "owner-a", "session-new", nil)
	if event := cleanupEvent(t, service, "owner-a", "session-old"); event != nil {
		t.Fatal("committed event outlived the retention TTL")
	}
	if event := cleanupEvent(t, service, "owner-a", "session-new"); event == nil {
		t.Fatal("fresh committed event was purged")
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if _, exists := service.accountCleanupAliases[accountCleanupKey{ownerID: "owner-a", eventID: "session-old-alias"}]; exists {
		t.Fatal("alias of the purged event outlived the retention TTL")
	}
}

func TestOwnerCleanupPerOwnerCapKeepsNewestCommitted(t *testing.T) {
	clock := newCleanupTestClock()
	service := New(Config{Resolver: &fakeResolver{targets: map[string]Target{}}, Now: clock.Now})
	for index := 0; index < maxAccountCleanupEventsPerOwner; index++ {
		clock.Advance(time.Millisecond)
		service.CancelOwner(t.Context(), "owner-a", fmt.Sprintf("event-%02d", index), nil, fmt.Sprintf("alias-%02d", index))
	}
	clock.Advance(time.Millisecond)
	service.CancelOwner(t.Context(), "owner-b", "event-b", nil)
	clock.Advance(time.Millisecond)
	service.CancelOwner(t.Context(), "owner-a", "event-overflow", nil)
	// The overflow is committed after that event's own prepare; the next
	// prepare applies the cap.
	clock.Advance(time.Millisecond)
	commit, rollback := service.PrepareOwnerCleanup(t.Context(), "owner-a", "event-trigger", nil)
	defer commit("session-trigger")
	defer rollback()

	if event := cleanupEvent(t, service, "owner-a", "event-00"); event != nil {
		t.Fatal("oldest committed event was not evicted at the per-owner cap")
	}
	if event := cleanupEvent(t, service, "owner-a", "event-01"); event == nil {
		t.Fatal("per-owner cap evicted more than the oldest committed event")
	}
	if event := cleanupEvent(t, service, "owner-a", "event-overflow"); event == nil {
		t.Fatal("newest committed event missing after the per-owner cap purge")
	}
	if event := cleanupEvent(t, service, "owner-b", "event-b"); event == nil {
		t.Fatal("per-owner cap evicted another owner's event")
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	ownerCounts := make(map[string]int)
	for key, event := range service.accountCleanups {
		if event.committed {
			ownerCounts[key.ownerID]++
		}
	}
	if ownerCounts["owner-a"] != maxAccountCleanupEventsPerOwner || ownerCounts["owner-b"] != 1 {
		t.Fatalf("committed events per owner = %v, want owner-a=%d owner-b=1", ownerCounts, maxAccountCleanupEventsPerOwner)
	}
	if _, exists := service.accountCleanupAliases[accountCleanupKey{ownerID: "owner-a", eventID: "alias-00"}]; exists {
		t.Fatal("alias of the evicted event survived")
	}
	if _, exists := service.accountCleanupAliases[accountCleanupKey{ownerID: "owner-a", eventID: "alias-01"}]; !exists {
		t.Fatal("alias of a retained event was evicted")
	}
}

func TestOwnerCleanupUncommittedEventNeverEvicted(t *testing.T) {
	clock := newCleanupTestClock()
	service := New(Config{Resolver: &fakeResolver{targets: map[string]Target{}}, Now: clock.Now})
	service.CancelOwner(t.Context(), "owner-a", "event-old", nil)
	commit, rollback := service.PrepareOwnerCleanup(t.Context(), "owner-a", "event-pending", nil)
	defer commit("session-pending")
	defer rollback()
	clock.Advance(accountCleanupEventTTL + time.Millisecond)
	service.CancelOwner(t.Context(), "owner-a", "event-new", nil)
	if event := cleanupEvent(t, service, "owner-a", "event-old"); event != nil {
		t.Fatal("committed event outlived the retention TTL")
	}
	event := cleanupEvent(t, service, "owner-a", "event-pending")
	if event == nil || event.committed {
		t.Fatalf("uncommitted event = %+v, want it retained and uncommitted", event)
	}
}

func TestOwnerCleanupGlobalCapEvictsOldestCommittedFirst(t *testing.T) {
	clock := newCleanupTestClock()
	service := New(Config{Resolver: &fakeResolver{targets: map[string]Target{}}, Now: clock.Now})
	owners := maxAccountCleanupEvents / maxAccountCleanupEventsPerOwner
	for owner := 0; owner < owners; owner++ {
		for index := 0; index < maxAccountCleanupEventsPerOwner; index++ {
			clock.Advance(time.Millisecond)
			service.CancelOwner(t.Context(), fmt.Sprintf("owner-%02d", owner), fmt.Sprintf("event-%02d", index), nil)
		}
	}
	clock.Advance(time.Millisecond)
	service.CancelOwner(t.Context(), "owner-00", "event-global-overflow", nil)
	// The overflow is committed after that event's own prepare; the next
	// prepare applies the cap.
	clock.Advance(time.Millisecond)
	commit, rollback := service.PrepareOwnerCleanup(t.Context(), "owner-00", "event-global-trigger", nil)
	defer commit("session-global-trigger")
	defer rollback()
	if event := cleanupEvent(t, service, "owner-00", "event-00"); event != nil {
		t.Fatal("globally oldest committed event was not evicted at the global cap")
	}
	if event := cleanupEvent(t, service, "owner-00", "event-01"); event == nil {
		t.Fatal("global cap evicted more than the oldest committed event")
	}
	if event := cleanupEvent(t, service, "owner-00", "event-global-overflow"); event == nil {
		t.Fatal("newest committed event missing after the global cap purge")
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	committed := 0
	for _, event := range service.accountCleanups {
		if event.committed {
			committed++
		}
	}
	if committed != maxAccountCleanupEvents {
		t.Fatalf("committed events = %d, want %d", committed, maxAccountCleanupEvents)
	}
}

func TestOwnerCleanupAliasCapEvictsOldestFirst(t *testing.T) {
	clock := newCleanupTestClock()
	service := New(Config{Resolver: &fakeResolver{targets: map[string]Target{}}, Now: clock.Now})
	clock.Advance(time.Millisecond)
	service.CancelOwner(t.Context(), "owner-a", "event", nil, "alias-000")
	for index := 1; index <= maxAccountCleanupAliasesPerOwner; index++ {
		service.CancelOwner(t.Context(), "owner-a", "event", nil, fmt.Sprintf("alias-%03d", index))
	}
	// The alias trim is lazy: the next prepare applies the cap.
	service.CancelOwner(t.Context(), "owner-a", "event-next", nil)
	service.mu.Lock()
	defer service.mu.Unlock()
	if len(service.accountCleanupAliases) != maxAccountCleanupAliasesPerOwner {
		t.Fatalf("aliases = %d, want %d", len(service.accountCleanupAliases), maxAccountCleanupAliasesPerOwner)
	}
	if _, exists := service.accountCleanupAliases[accountCleanupKey{ownerID: "owner-a", eventID: "alias-000"}]; exists {
		t.Fatal("oldest alias was not evicted at the per-owner alias cap")
	}
	if _, exists := service.accountCleanupAliases[accountCleanupKey{ownerID: "owner-a", eventID: "alias-001"}]; !exists {
		t.Fatal("alias cap evicted more than the oldest alias")
	}
}
