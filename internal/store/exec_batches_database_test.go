package store_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func execBatchDatabaseRow(id string, startedAt int64, status string) store.ExecBatchRow {
	row := store.ExecBatchRow{
		ID: id, CommandHash: strings.Repeat("a", 64), TargetSetHash: strings.Repeat("b", 64),
		Status: status, TargetCount: 1, StartedAt: startedAt,
	}
	if status != store.ExecBatchStatusRunning {
		finishedAt := startedAt + 10
		row.FinishedAt = &finishedAt
	}
	return row
}

func execBatchDatabaseResult(sessionID string, position int32, status string) store.ExecBatchResultRow {
	startedAt := int64(1000)
	finishedAt := int64(1005)
	result := store.ExecBatchResultRow{
		SessionID: sessionID, Position: position, SessionName: "name-" + sessionID,
		Status: status, StartedAt: &startedAt, FinishedAt: &finishedAt, DurationMS: 5,
	}
	if status != "success" {
		result.ErrorKind = store.ExecBatchErrorTransport
	}
	return result
}

func execBatchDatabaseContext(owner string) context.Context {
	return ipc.WithUserID(context.Background(), owner)
}

func requireExecBatchHistoryMissing(t *testing.T, ctx context.Context, database *store.Store, id string, scopes ...string) {
	t.Helper()
	_, err := database.ExecBatchHistoryGet(ctx, id, scopes...)
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != ipc.CodeNotFound {
		t.Fatalf("history %s error = %v, want not found", id, err)
	}
}

func TestExecBatchHistoryCRUDOwnerIsolationAndPagingDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctxA := execBatchDatabaseContext("owner-a")
	ctxB := execBatchDatabaseContext("owner-b")
	adminA := ipc.WithRole(execBatchDatabaseContext("owner-a"), "superadmin")
	ownerless := context.Background()

	running := execBatchDatabaseRow("shared-id", 3000, store.ExecBatchStatusRunning)
	running.TargetCount = 2
	if err := database.ExecBatchHistorySave(ctxA, running, nil); err != nil {
		t.Fatal(err)
	}
	failed := execBatchDatabaseRow("shared-id", 3000, store.ExecBatchStatusFailed)
	failed.TargetCount = 2
	if err := database.ExecBatchHistorySave(ctxA, failed, []store.ExecBatchResultRow{
		execBatchDatabaseResult("a-second", 1, "transport_error"),
		execBatchDatabaseResult("a-first", 0, "success"),
	}); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		ctx     context.Context
		id      string
		started int64
		session string
	}{
		{ctxA, "a-old", 1000, "a-old"},
		{ctxB, "shared-id", 4000, "b-shared"},
		{ctxB, "b-only", 5000, "b-only"},
		{ownerless, "shared-id", 2500, "ownerless-shared"},
		{ownerless, "local-only", 2000, "local-only"},
	} {
		row := execBatchDatabaseRow(item.id, item.started, store.ExecBatchStatusSuccess)
		if err := database.ExecBatchHistorySave(item.ctx, row, []store.ExecBatchResultRow{
			execBatchDatabaseResult(item.session, 0, "success"),
		}); err != nil {
			t.Fatal(err)
		}
	}

	pageA, err := database.ExecBatchHistoryList(ctxA, store.ExecBatchHistoryQuery{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if pageA.Total != 2 || len(pageA.Items) != 1 || pageA.Items[0].ID != "shared-id" || pageA.Items[0].Status != store.ExecBatchStatusFailed {
		t.Fatalf("owner A first page = %+v", pageA)
	}
	pageA, err = database.ExecBatchHistoryList(ctxA, store.ExecBatchHistoryQuery{Limit: 1, Offset: 1})
	if err != nil || len(pageA.Items) != 1 || pageA.Items[0].ID != "a-old" {
		t.Fatalf("owner A second page = %+v, %v", pageA, err)
	}
	pageB, err := database.ExecBatchHistoryList(ctxB, store.ExecBatchHistoryQuery{})
	if err != nil || pageB.Total != 2 || pageB.Items[0].ID != "b-only" || pageB.Items[1].ID != "shared-id" {
		t.Fatalf("owner B page = %+v, %v", pageB, err)
	}
	adminPage, err := database.ExecBatchHistoryList(adminA, store.ExecBatchHistoryQuery{})
	if err != nil || adminPage.Total != 4 {
		t.Fatalf("superadmin page = %+v, %v", adminPage, err)
	}
	seenScopes := make(map[string]bool)
	for _, row := range adminPage.Items {
		if row.ID == "b-only" {
			t.Fatalf("superadmin read another owner's row: %+v", adminPage.Items)
		}
		seenScopes[row.ID+":"+row.OwnerScope] = true
	}
	for _, key := range []string{"local-only:ownerless", "shared-id:self", "shared-id:ownerless"} {
		if !seenScopes[key] {
			t.Fatalf("superadmin page is missing %s: %+v", key, adminPage.Items)
		}
	}
	localPage, err := database.ExecBatchHistoryList(ownerless, store.ExecBatchHistoryQuery{})
	if err != nil || localPage.Total != 2 || len(localPage.Items) != 2 ||
		localPage.Items[0].ID != "shared-id" || localPage.Items[1].ID != "local-only" {
		t.Fatalf("ownerless page = %+v, %v", localPage, err)
	}

	historyA, err := database.ExecBatchHistoryGet(adminA, "shared-id", store.ExecBatchHistoryScopeSelf)
	if err != nil || len(historyA.Results) != 2 || historyA.Results[0].SessionID != "a-first" || historyA.Results[1].SessionID != "a-second" {
		t.Fatalf("superadmin self matrix = %+v, %v", historyA, err)
	}
	historyLocal, err := database.ExecBatchHistoryGet(adminA, "shared-id", store.ExecBatchHistoryScopeOwnerless)
	if err != nil || len(historyLocal.Results) != 1 || historyLocal.Results[0].SessionID != "ownerless-shared" {
		t.Fatalf("superadmin ownerless same-id matrix = %+v, %v", historyLocal, err)
	}
	historyB, err := database.ExecBatchHistoryGet(ctxB, "shared-id")
	if err != nil || len(historyB.Results) != 1 || historyB.Results[0].SessionID != "b-shared" {
		t.Fatalf("owner B same-id matrix = %+v, %v", historyB, err)
	}
	if _, err := database.ExecBatchHistoryGet(adminA, "local-only", store.ExecBatchHistoryScopeOwnerless); err != nil {
		t.Fatalf("superadmin ownerless get: %v", err)
	}
	if _, err := database.ExecBatchHistoryGet(ownerless, "shared-id"); err != nil {
		t.Fatalf("ownerless self get: %v", err)
	}
	requireExecBatchHistoryMissing(t, ctxA, database, "b-only")
	requireExecBatchHistoryMissing(t, adminA, database, "b-only")
	requireExecBatchHistoryMissing(t, ownerless, database, "a-old")
	requireExecBatchHistoryMissing(t, ctxB, database, "shared-id", store.ExecBatchHistoryScopeOwnerless)
}

func requireExecBatchDatabaseCounts(t *testing.T, database *store.Store, owner string, batches, results, tombstones int64) {
	t.Helper()
	for table, want := range map[string]int64{"exec_batch": batches, "exec_batch_result": results} {
		var count int64
		query := database.GORM().Table(table)
		if owner != "" {
			query = query.Where("owner_id = ?", owner)
		}
		if err := query.Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("%s owner %q count = %d, want %d", table, owner, count, want)
		}
	}
	var count int64
	query := database.GORM().Model(&store.SyncTombstone{}).Where("kind = ?", "exec_batch")
	if owner != "" {
		query = query.Where("owner_id = ?", owner)
	}
	if err := query.Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != tombstones {
		t.Fatalf("exec batch tombstones owner %q = %d, want %d", owner, count, tombstones)
	}
}

func TestExecBatchRetentionAgeCascadeAndIdempotenceDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctxA := execBatchDatabaseContext("owner-a")
	const total = 2*128 + 7
	now := time.Now()
	old := now.Add(-2 * time.Hour).UnixMilli()
	batches := make([]store.ExecBatchRow, 0, total+4)
	results := make([]store.ExecBatchResultRow, 0, total+3)
	for index := range total {
		id := fmt.Sprintf("age-%03d", index)
		row := execBatchDatabaseRow(id, old, store.ExecBatchStatusSuccess)
		row.OwnerID = "owner-a"
		row.SyncOptIn = index%2 == 0
		batches = append(batches, row)
		result := execBatchDatabaseResult("session-"+id, 0, "success")
		result.OwnerID = "owner-a"
		result.BatchID = id
		results = append(results, result)
	}
	other := execBatchDatabaseRow("other-old", old, store.ExecBatchStatusSuccess)
	other.OwnerID = "owner-b"
	other.SyncOptIn = true
	batches = append(batches, other)
	otherResult := execBatchDatabaseResult("other-session", 0, "success")
	otherResult.OwnerID = "owner-b"
	otherResult.BatchID = other.ID
	results = append(results, otherResult)
	active := execBatchDatabaseRow("active", old, store.ExecBatchStatusRunning)
	active.OwnerID = "owner-a"
	batches = append(batches, active)
	recent := execBatchDatabaseRow("recent", now.UnixMilli(), store.ExecBatchStatusSuccess)
	recent.OwnerID = "owner-a"
	batches = append(batches, recent)
	recentResult := execBatchDatabaseResult("recent-session", 0, "success")
	recentResult.OwnerID = "owner-a"
	recentResult.BatchID = recent.ID
	results = append(results, recentResult)
	if err := database.GORM().CreateInBatches(batches, 64).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GORM().CreateInBatches(results, 64).Error; err != nil {
		t.Fatal(err)
	}

	policy := store.RetentionPolicy{ExecBatchMaxAge: time.Hour}
	result, err := database.EnforceRetention(ctxA, policy)
	if err != nil || result.ExecBatchesDeleted != total+1 {
		t.Fatalf("age retention = %+v, %v; want %d", result, err, total+1)
	}
	requireExecBatchDatabaseCounts(t, database, "owner-a", 2, 1, 132)
	requireExecBatchDatabaseCounts(t, database, "owner-b", 0, 0, 1)
	if _, err := database.ExecBatchHistoryGet(ctxA, "active"); err != nil {
		t.Fatalf("running batch was removed: %v", err)
	}
	if _, err := database.ExecBatchHistoryGet(ctxA, "recent"); err != nil {
		t.Fatalf("recent batch was removed: %v", err)
	}
	repeated, err := database.EnforceRetention(ctxA, policy)
	if err != nil || repeated != (store.RetentionResult{}) {
		t.Fatalf("repeated age retention = %+v, %v", repeated, err)
	}
	requireExecBatchDatabaseCounts(t, database, "owner-a", 2, 1, 132)
	requireExecBatchDatabaseCounts(t, database, "owner-b", 0, 0, 1)
}

func TestExecBatchRetentionCountPreservesNewestAndRunningDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctxA := execBatchDatabaseContext("owner-a")
	const total = 2*128 + 7
	base := time.Now().Add(-time.Hour)
	batches := make([]store.ExecBatchRow, 0, total+1)
	for index := range total {
		row := execBatchDatabaseRow(fmt.Sprintf("count-%03d", index), base.Add(time.Duration(index)*time.Second).UnixMilli(), store.ExecBatchStatusSuccess)
		row.OwnerID = "owner-a"
		row.SyncOptIn = index%2 == 0
		batches = append(batches, row)
	}
	running := execBatchDatabaseRow("running", base.UnixMilli(), store.ExecBatchStatusRunning)
	running.OwnerID = "owner-a"
	batches = append(batches, running)
	if err := database.GORM().CreateInBatches(batches, 64).Error; err != nil {
		t.Fatal(err)
	}
	policy := store.RetentionPolicy{ExecBatchMaxCount: 4}
	result, err := database.EnforceRetention(ctxA, policy)
	if err != nil || result.ExecBatchesDeleted != total-4 {
		t.Fatalf("count retention = %+v, %v", result, err)
	}
	requireExecBatchDatabaseCounts(t, database, "owner-a", 5, 0, 130)
	for index := total - 4; index < total; index++ {
		if _, err := database.ExecBatchHistoryGet(ctxA, fmt.Sprintf("count-%03d", index)); err != nil {
			t.Fatalf("newest batch %d was removed: %v", index, err)
		}
	}
	requireExecBatchHistoryMissing(t, ctxA, database, "count-000")
	if _, err := database.ExecBatchHistoryGet(ctxA, "running"); err != nil {
		t.Fatalf("running batch counted toward retention: %v", err)
	}
	repeated, err := database.EnforceRetention(ctxA, policy)
	if err != nil || repeated != (store.RetentionResult{}) {
		t.Fatalf("repeated count retention = %+v, %v", repeated, err)
	}
}
