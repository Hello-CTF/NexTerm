package agent

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/store"
)

// gatedRosterCASStore blocks the roster conditional-write entry until
// released, so a scope or lifecycle change can land after the check but
// before the guarded write.
type gatedRosterCASStore struct {
	*store.Store
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *gatedRosterCASStore) AgentRosterCompareAndSwapState(ctx context.Context, id string, identity store.AgentRosterIdentity, expectedState string, updates map[string]any) (store.AgentRosterRow, error) {
	s.once.Do(func() { close(s.entered); <-s.release })
	return s.Store.AgentRosterCompareAndSwapState(ctx, id, identity, expectedState, updates)
}

// gatedRunFinishStore blocks the run conditional-finish entry until
// released, so a status change can land after the scan but before the
// guarded write.
type gatedRunFinishStore struct {
	*store.Store
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *gatedRunFinishStore) RunFinishUsageIfStatus(ctx context.Context, runID, expectedStatus, status, answer, errMsg string, turns int, tokensIn, tokensOut, cacheCreationTokens, latencyMS int64) (bool, error) {
	s.once.Do(func() { close(s.entered); <-s.release })
	return s.Store.RunFinishUsageIfStatus(ctx, runID, expectedStatus, status, answer, errMsg, turns, tokensIn, tokensOut, cacheCreationTokens, latencyMS)
}

func rosterRealStore(t *testing.T) (*store.Store, context.Context) {
	t.Helper()
	db, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, context.Background()
}

func provisionActivateRoster(t *testing.T, db *store.Store, ctx context.Context, owner, handle, jobID string) RosterEntry {
	t.Helper()
	roster := NewRoster(db, nil)
	entry, err := roster.Provision(ctx, "conv-1", handle, "", "tab-"+handle, nil)
	if err != nil {
		t.Fatal(err)
	}
	activated, err := roster.Activate(ctx, entry.ID, RosterIdentity{CreatedAt: entry.CreatedAt, Incarnation: entry.Incarnation}, jobID)
	if err != nil {
		t.Fatal(err)
	}
	return activated
}

func TestRosterAttachIncarnationReplacedAfterCheckDoesNotOverwrite(t *testing.T) {
	db, ctx := rosterRealStore(t)
	entry := provisionActivateRoster(t, db, ctx, "owner-a", "handle-a", "job-0")
	gated := &gatedRosterCASStore{Store: db, entered: make(chan struct{}), release: make(chan struct{})}
	probe := newFakeRosterProbe()
	probe.set("handle-a", RosterRuntimeConnected, "")
	roster := newTestRoster(gated, probe)

	done := make(chan RosterAttachment, 1)
	go func() {
		attachment, err := roster.Attach(ctx, "handle-a", "job-new")
		if err != nil {
			t.Error(err)
		}
		done <- attachment
	}()
	// The attach is inside the conditional-write entry: the incarnation is
	// replaced out-of-band after the check but before the guarded write.
	<-gated.entered
	if err := db.GORM().Model(&store.AgentRosterRow{}).Where("id = ?", entry.ID).
		Updates(map[string]any{"incarnation": "inc-out-of-band"}).Error; err != nil {
		t.Fatal(err)
	}
	close(gated.release)
	attachment := <-done

	if attachment.Resolution != RosterResolutionReplaced {
		t.Fatalf("resolution=%s", attachment.Resolution)
	}
	// The out-of-band identity is preserved and the new job is not bound.
	row, err := db.AgentRosterGetByHandle(ctx, "handle-a")
	if err != nil {
		t.Fatal(err)
	}
	if row.Incarnation != "inc-out-of-band" || row.JobID != "job-0" || row.State != string(RosterStateActive) {
		t.Fatalf("replacement was overwritten: %+v", row)
	}
}

func TestRosterAttachLifecycleReplacedAfterCheckDoesNotOverwrite(t *testing.T) {
	db, ctx := rosterRealStore(t)
	entry := provisionActivateRoster(t, db, ctx, "owner-a", "handle-a", "job-0")
	gated := &gatedRosterCASStore{Store: db, entered: make(chan struct{}), release: make(chan struct{})}
	probe := newFakeRosterProbe()
	probe.set("handle-a", RosterRuntimeConnected, "")
	roster := newTestRoster(gated, probe)

	done := make(chan RosterAttachment, 1)
	go func() {
		attachment, err := roster.Attach(ctx, "handle-a", "job-new")
		if err != nil {
			t.Error(err)
		}
		done <- attachment
	}()
	// The lifecycle is authoritatively changed to gone after the check but
	// before the guarded write.
	<-gated.entered
	if err := db.GORM().Model(&store.AgentRosterRow{}).Where("id = ?", entry.ID).
		Updates(map[string]any{"state": string(RosterStateGone)}).Error; err != nil {
		t.Fatal(err)
	}
	close(gated.release)
	attachment := <-done

	if attachment.Resolution != RosterResolutionGone {
		t.Fatalf("resolution=%s", attachment.Resolution)
	}
	row, err := db.AgentRosterGetByHandle(ctx, "handle-a")
	if err != nil {
		t.Fatal(err)
	}
	if row.State != string(RosterStateGone) || row.JobID != "job-0" {
		t.Fatalf("lifecycle was overwritten: %+v", row)
	}
}

func TestRunRecoveryStatusReplacedAfterCheckDoesNotWrite(t *testing.T) {
	db, ctx := rosterRealStore(t)
	conversation, err := db.ConvCreate(ctx, "recover", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RunInsert(ctx, store.RunRow{ID: "run-x", ConversationID: conversation.ID, Status: store.RunStatusInterrupted}); err != nil {
		t.Fatal(err)
	}
	gated := &gatedRunFinishStore{Store: db, entered: make(chan struct{}), release: make(chan struct{})}
	probe := newFakeRunProbe()
	probe.set("run-x", RosterRuntimeGone, "")

	done := make(chan RunRecoverySummary, 1)
	go func() {
		summary, err := NewRunRecovery(gated).Recover(ctx, probe)
		if err != nil {
			t.Error(err)
		}
		done <- summary
	}()
	// The run is resumed to running after the scan but before the guarded
	// write: the conditional finish must not land.
	<-gated.entered
	if err := db.RunUpdateStatus(ctx, "run-x", store.RunStatusRunning); err != nil {
		t.Fatal(err)
	}
	close(gated.release)
	summary := <-done

	if len(summary.Entries) != 1 || summary.Entries[0].Wrote {
		t.Fatalf("stale status wrote the run: %+v", summary.Entries)
	}
	row, err := db.RunGet(ctx, "run-x")
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != store.RunStatusRunning || row.FinishedAt != nil {
		t.Fatalf("resumed run was finished: %+v", row)
	}
	events, _ := db.RunEventsAfter(ctx, "run-x", 0)
	if len(events) != 0 {
		t.Fatalf("no-op appended an event: %+v", events)
	}
}

// TestOutcomeLedgerOrderingMonotonicityContract pins the ledger's
// ordering and monotonic completion contract: records are keyed by
// owner+operation ID, the first final write wins, and a later completion
// never regresses the recorded outcome. The ledger does not execute or
// bind assets: execution-time asset revalidation is the mutation tools'
// (M26) and wiring's (M15/M60) unfinished boundary, not this contract.
func TestOutcomeLedgerOrderingMonotonicityContract(t *testing.T) {
	ledger, _, ctx := outcomeLedgerForTest(t)
	if _, err := ledger.Begin(ctx, "op-1", "terminal_create"); err != nil {
		t.Fatal(err)
	}
	first, err := ledger.Succeed(ctx, "op-1", map[string]any{"tabId": "tab-1"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := ledger.Fail(ctx, "op-1", "connect", errTestOutcome, OutcomeResidual{})
	if err != nil {
		t.Fatal(err)
	}
	if second.State != OutcomeStateSucceeded || string(second.Result) != string(first.Result) {
		t.Fatalf("completion regressed: %+v", second)
	}
	record, err := ledger.Result(ctx, "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if record.State != OutcomeStateSucceeded || string(record.Result) != `{"tabId":"tab-1"}` {
		t.Fatalf("record = %+v", record)
	}
}

var errTestOutcome = errors.New("dial failed")

func TestRosterLegacyNonpersistentFallbackStaysUnverifiable(t *testing.T) {
	ctx := rosterOwnerContext("owner-a")
	fake := newFakeRosterStore()
	probe := newFakeRosterProbe()
	probe.set("ephemeral", RosterRuntimeUnverifiable, "")
	roster := newTestRoster(fake, probe)

	attachment, err := roster.Attach(ctx, "ephemeral", "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if attachment.Resolution != RosterResolutionUnverifiable {
		t.Fatalf("fallback resolution=%s", attachment.Resolution)
	}
	if _, err := fake.AgentRosterGetByHandle(ctx, "ephemeral"); err == nil {
		t.Fatal("fallback created a roster row")
	}
	rows, err := fake.AgentRosterList(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("fallback wrote rows: %+v", rows)
	}
}

func TestOwnerScopeWritesDoNotCrossOwners(t *testing.T) {
	fake := newFakeRosterStore()
	probe := newFakeRosterProbe()
	roster := newTestRoster(fake, probe)
	ctxA := rosterOwnerContext("owner-a")
	ctxB := rosterOwnerContext("owner-b")
	entryA := mustProvision(t, roster, ctxA, "conv-1", "handle-a")
	entryB := mustProvision(t, roster, ctxB, "conv-1", "handle-b")
	if _, err := roster.Activate(ctxA, entryA.ID, rosterIdentityOf(entryA), "job-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := roster.Activate(ctxB, entryB.ID, rosterIdentityOf(entryB), "job-b"); err != nil {
		t.Fatal(err)
	}
	before, err := fake.AgentRosterGetByHandle(ctxB, "handle-b")
	if err != nil {
		t.Fatal(err)
	}
	probe.set("handle-a", RosterRuntimeReplaced, "inc-new")
	if _, err := roster.Attach(ctxA, "handle-a", "job-new"); err != nil {
		t.Fatal(err)
	}
	after, err := fake.AgentRosterGetByHandle(ctxB, "handle-b")
	if err != nil {
		t.Fatal(err)
	}
	// The full row is byte-identical: no field of owner B's row moved.
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("cross-owner write: before=%+v after=%+v", before, after)
	}
}
