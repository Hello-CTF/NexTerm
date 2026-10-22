package store_test

import (
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func saveAssetHealthHistory(t *testing.T, database *store.Store, owner, batchID, assetID, status string, startedAt int64) {
	t.Helper()
	finishedAt := startedAt + 100
	row := store.ExecBatchRow{
		ID: batchID, CommandHash: strings.Repeat("a", 64), TargetSetHash: strings.Repeat("b", 64),
		Status: store.ExecBatchStatusSuccess, TargetCount: 1, StartedAt: startedAt, FinishedAt: &finishedAt,
	}
	if status != "success" {
		row.Status = store.ExecBatchStatusFailed
	}
	errorKind := ""
	if status != "success" {
		errorKind = store.ExecBatchErrorRemoteExit
	}
	results := []store.ExecBatchResultRow{{
		SessionID: batchID + "-session", Position: 0, SessionName: assetID, AssetID: assetID,
		Status: status, ErrorKind: errorKind, StartedAt: &startedAt, FinishedAt: &finishedAt,
	}}
	ctx := ipc.WithUserID(t.Context(), owner)
	if err := database.ExecBatchHistorySave(ctx, row, results); err != nil {
		t.Fatal(err)
	}
}

func TestExecBatchHistoryLatestByAssetsIsReadOnlyAndOwnerScoped(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	saveAssetHealthHistory(t, database, "owner-a", "old", "asset-a", "success", 1000)
	saveAssetHealthHistory(t, database, "owner-a", "latest", "asset-a", "exit_error", 3000)
	saveAssetHealthHistory(t, database, "owner-b", "foreign", "asset-a", "success", 9000)

	ctx := ipc.WithUserID(t.Context(), "owner-a")
	rows, err := database.ExecBatchHistoryLatestByAssets(ctx, []string{"asset-a", "asset-without-history"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].AssetID != "asset-a" || rows[0].BatchID != "latest" || rows[0].Status != "exit_error" || rows[0].StartedAt != 3000 {
		t.Fatalf("latest rows = %+v", rows)
	}
	page, err := database.ExecBatchHistoryList(ctx, store.ExecBatchHistoryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 {
		t.Fatalf("read changed owner history: %+v", page)
	}
}
