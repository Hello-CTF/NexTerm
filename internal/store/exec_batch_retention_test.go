package store

import (
	"fmt"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func requireExecBatchRetentionRows(t *testing.T, database *Store, owner string, batches, tombstones int64) {
	t.Helper()
	var batchCount int64
	if err := database.GORM().Model(&ExecBatchRow{}).Where("owner_id = ?", owner).Count(&batchCount).Error; err != nil {
		t.Fatal(err)
	}
	if batchCount != batches {
		t.Fatalf("exec batches = %d, want %d", batchCount, batches)
	}
	var tombstoneCount int64
	if err := database.GORM().Model(&SyncTombstone{}).
		Where("owner_id = ? AND kind = ?", owner, execBatchTombstoneKind).Count(&tombstoneCount).Error; err != nil {
		t.Fatal(err)
	}
	if tombstoneCount != tombstones {
		t.Fatalf("exec batch tombstones = %d, want %d", tombstoneCount, tombstones)
	}
}

func requireExecBatchRetentionTombstone(t *testing.T, database *Store, owner, id string, want bool) {
	t.Helper()
	var count int64
	if err := database.GORM().Model(&SyncTombstone{}).
		Where("owner_id = ? AND id = ? AND kind = ?", owner, id, execBatchTombstoneKind).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if (count == 1) != want {
		t.Fatalf("tombstone for %s exists = %v, want %v", id, count == 1, want)
	}
}

func TestExecBatchRetentionSecondBatchFailureCommitsIndependently(t *testing.T) {
	database := testStore(t)
	ctx := ipc.WithUserID(t.Context(), "owner-a")
	const total = 2*retentionBatchSize + 17
	old := time.Now().Add(-2 * time.Hour).UnixMilli()
	rows := make([]ExecBatchRow, 0, total)
	for index := range total {
		finishedAt := old
		rows = append(rows, ExecBatchRow{
			OwnerID: "owner-a", ID: fmt.Sprintf("age-%03d", index),
			CommandHash:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			TargetSetHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			Status:        ExecBatchStatusSuccess, TargetCount: 1, StartedAt: old - 1, FinishedAt: &finishedAt,
			SyncOptIn: index%2 == 0,
		})
	}
	if err := database.GORM().CreateInBatches(rows, 64).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB().Exec(`CREATE TRIGGER fail_second_exec_batch_retention BEFORE DELETE ON exec_batch
WHEN OLD.owner_id = 'owner-a' AND OLD.id = 'age-128'
BEGIN SELECT RAISE(ABORT, 'injected second exec batch retention failure'); END`); err != nil {
		t.Fatal(err)
	}
	policy := RetentionPolicy{ExecBatchMaxAge: time.Hour}
	if _, err := database.EnforceRetention(ctx, policy); err == nil {
		t.Fatal("expected second exec batch retention failure")
	}
	requireExecBatchRetentionRows(t, database, "owner-a", total-retentionBatchSize, retentionBatchSize/2)
	requireExecBatchRetentionTombstone(t, database, "owner-a", "age-000", true)
	requireExecBatchRetentionTombstone(t, database, "owner-a", "age-128", false)

	if _, err := database.DB().Exec("DROP TRIGGER fail_second_exec_batch_retention"); err != nil {
		t.Fatal(err)
	}
	result, err := database.EnforceRetention(ctx, policy)
	if err != nil || result.ExecBatchesDeleted != total-retentionBatchSize {
		t.Fatalf("resumed retention = %+v, %v", result, err)
	}
	requireExecBatchRetentionRows(t, database, "owner-a", 0, 137)
	requireExecBatchRetentionTombstone(t, database, "owner-a", "age-128", true)
	repeated, err := database.EnforceRetention(ctx, policy)
	if err != nil || repeated != (RetentionResult{}) {
		t.Fatalf("repeated retention = %+v, %v", repeated, err)
	}
	requireExecBatchRetentionRows(t, database, "owner-a", 0, 137)
}
