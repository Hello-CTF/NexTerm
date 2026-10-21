package production

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/runbook"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// seedAccountSessionRow inserts the durable rows the cleanup generation-bound
// read needs; revokedAt <= 0 leaves the session unrevoked.
func seedAccountSessionRow(t *testing.T, database *store.Store, ownerID, sessionID string, expiresAt, revokedAt int64) {
	t.Helper()
	db := database.GORM()
	if err := db.Exec(`INSERT INTO app_user(id, username, role, password_hash, state, created_at, updated_at)
		VALUES (?, ?, 'user', 'hash', 'active', ?, ?)`, ownerID, "user-"+ownerID, expiresAt, expiresAt).Error; err != nil {
		t.Fatal(err)
	}
	var revoked any
	if revokedAt > 0 {
		revoked = revokedAt
	}
	if err := db.Exec(`INSERT INTO user_session(id, user_id, token_hash, created_at, touched_at, expires_at, revoked_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, sessionID, ownerID, "hash-"+sessionID, expiresAt, expiresAt, expiresAt, revoked).Error; err != nil {
		t.Fatal(err)
	}
}

func cleanupStateEvent(t *testing.T, application *Production, ownerID, eventID string) *accountCleanupEvent {
	t.Helper()
	application.accountCleanup.mu.Lock()
	defer application.accountCleanup.mu.Unlock()
	return application.accountCleanup.events[accountCleanupKey{ownerID: ownerID, eventID: eventID}]
}

// A replay of a committed cleanup event dedups on the marker after the device
// payload was dropped at commit: exactly-once close counts hold and a new
// generation started after the commit is not re-snapshotted.
func TestAccountCleanupReplayAfterDropKeepsExactlyOnceCloseCounts(t *testing.T) {
	rig := newAccountCleanupRig(t)
	application := &Production{Services: ProductionServices{Sessions: rig.manager}}
	owner := ipc.WithUserID(t.Context(), "owner-a")
	connected, tab := rig.open(t, owner, session.Asset{ID: "device/a", Kind: session.KindDevice})

	commit, _, err := application.PrepareAccountCleanup(context.Background(), "owner-a", "login-a")
	if err != nil {
		t.Fatal(err)
	}
	commit("login-a")
	requireAccountCleanupTab(t, owner, rig.manager, tab.ID, false)
	requireAccountCleanupStatus(t, owner, rig.manager, connected.ID, session.StatusDisconnected)
	if got := rig.transport("device/a").channels[0].closeCount.Load(); got != 1 {
		t.Fatalf("terminal close count = %d, want 1", got)
	}
	if got := rig.transport("device/a").closeCount.Load(); got != 1 {
		t.Fatalf("transport close count = %d, want 1", got)
	}
	event := cleanupStateEvent(t, application, "owner-a", "login-a")
	if event == nil || !event.committed || event.deviceSessions != nil {
		t.Fatalf("committed event = %+v, want committed with the device payload dropped", event)
	}

	newSession, newTab := rig.open(t, owner, session.Asset{ID: "device/new", Kind: session.KindDevice})
	if err := application.CleanupAccount(context.Background(), "owner-a", "login-a", "login-a"); err != nil {
		t.Fatal(err)
	}
	if got := rig.transport("device/a").channels[0].closeCount.Load(); got != 1 {
		t.Fatalf("replay re-closed the terminal: close count = %d, want 1", got)
	}
	requireAccountCleanupTab(t, owner, rig.manager, newTab.ID, true)
	requireAccountCleanupStatus(t, owner, rig.manager, newSession.ID, session.StatusConnected)
}

func TestAccountCleanupCommittedEventPurgesAfterTTL(t *testing.T) {
	clock := newAccountCleanupClock()
	rig := newAccountCleanupRig(t, func(config *session.Config) { config.Now = clock.Now })
	application := &Production{Services: ProductionServices{Sessions: rig.manager}}
	application.accountCleanup.now = clock.Now
	if err := application.CleanupAccount(context.Background(), "owner-a", "login-old", "session-old"); err != nil {
		t.Fatal(err)
	}
	clock.Advance(accountCleanupEventTTL + time.Millisecond)
	if err := application.CleanupAccount(context.Background(), "owner-a", "login-new", "session-new"); err != nil {
		t.Fatal(err)
	}
	if event := cleanupStateEvent(t, application, "owner-a", "login-old"); event != nil {
		t.Fatal("committed event outlived the retention TTL")
	}
	if event := cleanupStateEvent(t, application, "owner-a", "login-new"); event == nil {
		t.Fatal("fresh committed event was purged")
	}
	application.accountCleanup.mu.Lock()
	defer application.accountCleanup.mu.Unlock()
	if _, exists := application.accountCleanup.aliases[accountCleanupKey{ownerID: "owner-a", eventID: "session-old"}]; exists {
		t.Fatal("alias of the purged event outlived the retention TTL")
	}
}

func TestAccountCleanupPerOwnerCapKeepsNewestCommitted(t *testing.T) {
	clock := newAccountCleanupClock()
	rig := newAccountCleanupRig(t, func(config *session.Config) { config.Now = clock.Now })
	application := &Production{Services: ProductionServices{Sessions: rig.manager}}
	application.accountCleanup.now = clock.Now
	for index := 0; index < maxAccountCleanupEventsPerOwner; index++ {
		clock.Advance(time.Millisecond)
		if err := application.CleanupAccount(context.Background(), "owner-a", fmt.Sprintf("login-%02d", index), fmt.Sprintf("session-%02d", index)); err != nil {
			t.Fatal(err)
		}
	}
	clock.Advance(time.Millisecond)
	if err := application.CleanupAccount(context.Background(), "owner-b", "login-b", "session-b"); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Millisecond)
	if err := application.CleanupAccount(context.Background(), "owner-a", "login-overflow", "session-overflow"); err != nil {
		t.Fatal(err)
	}
	// The overflow is committed after that event's own prepare; the next
	// prepare applies the cap.
	clock.Advance(time.Millisecond)
	commit, rollback, err := application.PrepareAccountCleanup(context.Background(), "owner-a", "login-trigger")
	if err != nil {
		t.Fatal(err)
	}
	defer commit("session-trigger")
	defer rollback()

	if event := cleanupStateEvent(t, application, "owner-a", "login-00"); event != nil {
		t.Fatal("oldest committed event was not evicted at the per-owner cap")
	}
	if event := cleanupStateEvent(t, application, "owner-a", "login-01"); event == nil {
		t.Fatal("per-owner cap evicted more than the oldest committed event")
	}
	if event := cleanupStateEvent(t, application, "owner-a", "login-overflow"); event == nil {
		t.Fatal("newest committed event missing after the per-owner cap purge")
	}
	if event := cleanupStateEvent(t, application, "owner-b", "login-b"); event == nil {
		t.Fatal("per-owner cap evicted another owner's event")
	}
	application.accountCleanup.mu.Lock()
	defer application.accountCleanup.mu.Unlock()
	ownerCounts := make(map[string]int)
	for key, event := range application.accountCleanup.events {
		if event.committed {
			ownerCounts[key.ownerID]++
		}
	}
	if ownerCounts["owner-a"] != maxAccountCleanupEventsPerOwner || ownerCounts["owner-b"] != 1 {
		t.Fatalf("committed events per owner = %v, want owner-a=%d owner-b=1", ownerCounts, maxAccountCleanupEventsPerOwner)
	}
	if _, exists := application.accountCleanup.aliases[accountCleanupKey{ownerID: "owner-a", eventID: "session-00"}]; exists {
		t.Fatal("alias of the evicted event survived")
	}
	if _, exists := application.accountCleanup.aliases[accountCleanupKey{ownerID: "owner-a", eventID: "session-01"}]; !exists {
		t.Fatal("alias of a retained event was evicted")
	}
}

func TestAccountCleanupUncommittedEventNeverEvicted(t *testing.T) {
	clock := newAccountCleanupClock()
	rig := newAccountCleanupRig(t, func(config *session.Config) { config.Now = clock.Now })
	application := &Production{Services: ProductionServices{Sessions: rig.manager}}
	application.accountCleanup.now = clock.Now
	if err := application.CleanupAccount(context.Background(), "owner-a", "login-old", "session-old"); err != nil {
		t.Fatal(err)
	}
	commit, rollback, err := application.PrepareAccountCleanup(context.Background(), "owner-a", "login-pending")
	if err != nil {
		t.Fatal(err)
	}
	defer commit("session-pending")
	defer rollback()
	clock.Advance(accountCleanupEventTTL + time.Millisecond)
	if err := application.CleanupAccount(context.Background(), "owner-a", "login-new", "session-new"); err != nil {
		t.Fatal(err)
	}
	if event := cleanupStateEvent(t, application, "owner-a", "login-old"); event != nil {
		t.Fatal("committed event outlived the retention TTL")
	}
	event := cleanupStateEvent(t, application, "owner-a", "login-pending")
	if event == nil || event.committed {
		t.Fatalf("uncommitted event = %+v, want it retained and uncommitted", event)
	}
}

// The generation cutoff is same-millisecond inclusive: a device created in
// the expiry millisecond belongs to the terminal generation and is cleaned.
func TestAccountCleanupSameMillisecondCutoffIsInclusive(t *testing.T) {
	clock := newAccountCleanupClock()
	rig := newAccountCleanupRig(t, func(config *session.Config) { config.Now = clock.Now })
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	application := &Production{Services: ProductionServices{Sessions: rig.manager, Store: database}}
	owner := ipc.WithUserID(t.Context(), "owner-a")
	sessionID := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	seedAccountSessionRow(t, database, "owner-a", sessionID, clock.NowMS(), 0)

	sameMS, sameMSTab := rig.open(t, owner, session.Asset{ID: "device/same-ms", Kind: session.KindDevice})
	clock.Advance(time.Millisecond)
	later, laterTab := rig.open(t, owner, session.Asset{ID: "device/later", Kind: session.KindDevice})

	if err := application.CleanupAccount(context.Background(), "owner-a", sessionID, sessionID); err != nil {
		t.Fatal(err)
	}
	requireAccountCleanupTab(t, owner, rig.manager, sameMSTab.ID, false)
	requireAccountCleanupStatus(t, owner, rig.manager, sameMS.ID, session.StatusDisconnected)
	requireAccountCleanupTab(t, owner, rig.manager, laterTab.ID, true)
	requireAccountCleanupStatus(t, owner, rig.manager, later.ID, session.StatusConnected)
}

// The logout-all event key is normalized back to the canonical session ID for
// the cutoff read: a late prefixed cleanup stays bounded by the durable
// revocation instant instead of degrading to an unbounded fresh snapshot.
func TestAccountCleanupLogoutAllKeyNormalizesForCutoff(t *testing.T) {
	clock := newAccountCleanupClock()
	rig := newAccountCleanupRig(t, func(config *session.Config) { config.Now = clock.Now })
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	application := &Production{Services: ProductionServices{Sessions: rig.manager, Store: database}}
	owner := ipc.WithUserID(t.Context(), "owner-a")
	sessionID := "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	seedAccountSessionRow(t, database, "owner-a", sessionID, clock.NowMS()+3600_000, clock.NowMS()+2)

	before, beforeTab := rig.open(t, owner, session.Asset{ID: "device/before", Kind: session.KindDevice})
	clock.Advance(3 * time.Millisecond)
	after, afterTab := rig.open(t, owner, session.Asset{ID: "device/after", Kind: session.KindDevice})

	if err := application.CleanupAccount(context.Background(), "owner-a", accountCleanupLogoutAllPrefix+sessionID, sessionID); err != nil {
		t.Fatal(err)
	}
	requireAccountCleanupTab(t, owner, rig.manager, beforeTab.ID, false)
	requireAccountCleanupStatus(t, owner, rig.manager, before.ID, session.StatusDisconnected)
	requireAccountCleanupTab(t, owner, rig.manager, afterTab.ID, true)
	requireAccountCleanupStatus(t, owner, rig.manager, after.ID, session.StatusConnected)
}

// Post-eviction late cleanup derives its terminal no-op from the durable
// revoked session row: the fresh snapshot is bounded by the revocation
// instant, so new-generation devices and batches survive.
func TestAccountCleanupPostEvictionLateCleanupSurvivesNewGeneration(t *testing.T) {
	clock := newAccountCleanupClock()
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	rig := newAccountCleanupRig(t, func(config *session.Config) { config.Now = clock.Now })
	batches := execbatch.New(execbatch.Config{
		Resolver: execbatch.ResolverFunc(func(ctx context.Context, sessionID string) (execbatch.Target, error) {
			connected, err := rig.manager.SessionFor(ctx, sessionID)
			if err != nil {
				return execbatch.Target{}, err
			}
			transport, err := rig.manager.TransportFor(ctx, sessionID)
			if err != nil {
				return execbatch.Target{}, err
			}
			return execbatch.Target{SessionID: connected.ID, SessionName: connected.ID, Transport: transport, Generation: transport.Generation()}, nil
		}),
		Now: clock.Now,
	})
	application := &Production{Services: ProductionServices{Sessions: rig.manager, Store: database, ExecBatches: batches}}
	application.accountCleanup.now = clock.Now
	owner := ipc.WithUserID(t.Context(), "owner-a")
	sessionID := "01ARZ3NDEKTSV4RRFFQ69G5FAX"
	seedAccountSessionRow(t, database, "owner-a", sessionID, clock.NowMS()+3600_000, 0)

	commit, _, err := application.PrepareAccountCleanup(context.Background(), "owner-a", sessionID)
	if err != nil {
		t.Fatal(err)
	}
	commit(sessionID)
	if err := database.GORM().Exec("UPDATE user_session SET revoked_at = ? WHERE id = ?", clock.NowMS(), sessionID).Error; err != nil {
		t.Fatal(err)
	}
	clock.Advance(accountCleanupEventTTL + time.Millisecond)
	// The retention purge is lazy: a probe prepare applies it.
	_, probeRollback, err := application.PrepareAccountCleanup(context.Background(), "owner-a", "login-probe")
	if err != nil {
		t.Fatal(err)
	}
	probeRollback()
	if event := cleanupStateEvent(t, application, "owner-a", sessionID); event != nil {
		t.Fatal("committed event outlived the retention TTL")
	}

	batchStarted := make(chan struct{}, 1)
	rig.setExecHook("asset-ssh", func(ctx context.Context) (base.ExecResult, error) {
		batchStarted <- struct{}{}
		<-ctx.Done()
		return base.ExecResult{}, ctx.Err()
	})
	newSession, newTab := rig.open(t, owner, session.Asset{ID: "device/new", Kind: session.KindDevice})
	sshSession, _ := rig.open(t, owner, session.Asset{ID: "asset-ssh", Kind: session.KindSSH})
	batchDone := make(chan struct{}, 1)
	go func() {
		defer func() { batchDone <- struct{}{} }()
		_, _ = batches.Execute(ipc.WithUserID(t.Context(), "owner-a"), "owner-a", execbatch.Request{BatchID: "new-batch", SessionIDs: []string{sshSession.ID}, Command: "wait"})
	}()
	select {
	case <-batchStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("new generation batch did not start")
	}

	if err := application.CleanupAccount(context.Background(), "owner-a", sessionID, sessionID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-batchDone:
		t.Fatal("late cleanup of the evicted event canceled the new generation batch")
	case <-time.After(50 * time.Millisecond):
	}
	requireAccountCleanupTab(t, owner, rig.manager, newTab.ID, true)
	requireAccountCleanupStatus(t, owner, rig.manager, newSession.ID, session.StatusConnected)
	if event := cleanupStateEvent(t, application, "owner-a", sessionID); event == nil || !event.committed {
		t.Fatalf("late cleanup event = %+v, want a fresh committed event", event)
	}

	if _, err := batches.Cancel(t.Context(), "owner-a", "new-batch"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-batchDone:
	case <-time.After(2 * time.Second):
		t.Fatal("new generation batch did not finish")
	}
}

// The composition root wires the real runbook service into the account
// cleanup lifecycle: an owner cleanup cancels that owner's live runs and
// leaves every other owner's runs alone.
func TestAccountCleanupCompositionWiresRunbookCleanup(t *testing.T) {
	rig := newAccountCleanupRig(t)
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	application, err := NewProductionWithServices(Config{}, ProductionServices{Sessions: rig.manager, Store: database})
	if err != nil {
		t.Fatal(err)
	}
	if application.Services.Runbook == nil {
		t.Fatal("composition root did not build the runbook service")
	}
	application.accountCleanup.mu.Lock()
	wired := application.accountCleanup.runbook != nil
	application.accountCleanup.mu.Unlock()
	if !wired {
		t.Fatal("composition root did not wire the runbook service into the account cleanup lifecycle")
	}
	start := func(ownerID, runID string) {
		t.Helper()
		if _, err := application.Services.Runbook.Start(context.Background(), ownerID, runbook.StartRequest{
			RunID: runID, RunbookID: "runbook-1", RunbookName: "runbook",
			Steps:      []runbook.Step{{ID: "step-1", Name: "step", Command: "cmd"}},
			SessionIDs: []string{"asset-1"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	start("owner-a", "run-a1")
	start("owner-a", "run-a2")
	start("owner-b", "run-b1")

	if err := application.CleanupAccount(context.Background(), "owner-a", "logout-a", "logout-a"); err != nil {
		t.Fatal(err)
	}
	for _, runID := range []string{"run-a1", "run-a2"} {
		if _, err := application.Services.Runbook.Get(context.Background(), "owner-a", runID); !errors.Is(err, runbook.ErrNotFound) {
			t.Fatalf("owner A run %s after cleanup: err = %v, want %v", runID, err, runbook.ErrNotFound)
		}
	}
	state, err := application.Services.Runbook.Get(context.Background(), "owner-b", "run-b1")
	if err != nil {
		t.Fatalf("owner B run was affected by A's cleanup: %v", err)
	}
	if state.Status == runbook.RunCanceled {
		t.Fatal("owner B's run was canceled by A's cleanup")
	}
}
