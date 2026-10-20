package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gen"
	"gorm.io/gen/field"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const execBatchTombstoneKind = "exec_batch"

func (s *Store) enforceExecBatchRetention(ctx context.Context, policy RetentionPolicy, now time.Time) (int64, error) {
	var total int64
	if policy.ExecBatchMaxAge > 0 {
		cutoff := now.Add(-policy.ExecBatchMaxAge).UnixMilli()
		deleted, err := retentionDeleteBatches(ctx, s, "exec_batch", "age",
			func(tx *gorm.DB) ([]ExecBatchRow, error) {
				batch := query.Use(tx).ExecBatchRow
				rows := make([]ExecBatchRow, 0, retentionBatchSize)
				err := batch.WithContext(ctx).
					Where(batch.FinishedAt.IsNotNull(), batch.StartedAt.Lt(cutoff)).
					Order(batch.StartedAt.Asc(), batch.ID.Asc(), batch.OwnerID.Asc()).
					Limit(retentionBatchSize).Scan(&rows)
				return rows, err
			},
			func(tx *gorm.DB, rows []ExecBatchRow) (int64, error) {
				return deleteExecBatchRetentionRows(ctx, tx, rows, now, &cutoff)
			},
		)
		total += deleted
		if err != nil {
			return total, fmt.Errorf("exec batch age retention: %w", err)
		}
	}
	if policy.ExecBatchMaxCount > 0 {
		deleted, err := retentionDeleteBatches(ctx, s, "exec_batch", "count",
			func(tx *gorm.DB) ([]ExecBatchRow, error) {
				batch := query.Use(tx).ExecBatchRow
				rows := make([]ExecBatchRow, 0, retentionBatchSize)
				err := batch.WithContext(ctx).
					Where(batch.FinishedAt.IsNotNull()).
					Order(batch.StartedAt.Desc(), batch.ID.Desc(), batch.OwnerID.Desc()).
					Limit(retentionBatchSize).Offset(int(policy.ExecBatchMaxCount)).Scan(&rows)
				return rows, err
			},
			func(tx *gorm.DB, rows []ExecBatchRow) (int64, error) {
				return deleteExecBatchRetentionRows(ctx, tx, rows, now, nil)
			},
		)
		total += deleted
		if err != nil {
			return total, fmt.Errorf("exec batch count retention: %w", err)
		}
	}
	return total, nil
}

func deleteExecBatchRetentionRows(ctx context.Context, tx *gorm.DB, rows []ExecBatchRow, now time.Time, startedBefore *int64) (int64, error) {
	tombstones := make([]*SyncTombstone, 0, len(rows))
	for _, row := range rows {
		if row.SyncOptIn {
			tombstones = append(tombstones, &SyncTombstone{
				ID: row.ID, Kind: execBatchTombstoneKind, DeletedAt: now.UnixMilli(), OwnerID: row.OwnerID,
			})
		}
	}
	if len(tombstones) > 0 {
		tombstone := query.Use(tx).SyncTombstone
		if err := tombstone.WithContext(ctx).Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "owner_id"}, {Name: "id"}}, DoNothing: true,
		}).CreateInBatches(tombstones, retentionBatchSize); err != nil {
			return 0, err
		}
	}
	batch := query.Use(tx).ExecBatchRow
	q := batch.WithContext(ctx).Where(execBatchRetentionIdentities(rows), batch.FinishedAt.IsNotNull())
	if startedBefore != nil {
		q = q.Where(batch.StartedAt.Lt(*startedBefore))
	}
	info, err := q.Delete()
	return info.RowsAffected, err
}

func execBatchRetentionIdentities(rows []ExecBatchRow) gen.Condition {
	var condition strings.Builder
	arguments := make([]any, 0, 2*len(rows))
	condition.WriteByte('(')
	for index, row := range rows {
		if index > 0 {
			condition.WriteString(" OR ")
		}
		condition.WriteString("(owner_id = ? AND id = ?)")
		arguments = append(arguments, row.OwnerID, row.ID)
	}
	condition.WriteByte(')')
	return field.NewUnsafeFieldRaw(condition.String(), arguments...)
}
