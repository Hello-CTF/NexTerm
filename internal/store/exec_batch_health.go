package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gen"
)

type ExecBatchAssetHistory struct {
	AssetID    string `json:"assetId"`
	BatchID    string `json:"batchId"`
	Status     string `json:"status"`
	StartedAt  int64  `json:"startedAt"`
	FinishedAt *int64 `json:"finishedAt"`
}

func (s *Store) ExecBatchHistoryLatestByAssets(ctx context.Context, assetIDs []string) ([]ExecBatchAssetHistory, error) {
	if len(assetIDs) == 0 || len(assetIDs) > 64 {
		return nil, badParam(fmt.Errorf("asset history requires 1-64 asset ids"))
	}
	seen := make(map[string]struct{}, len(assetIDs))
	for _, assetID := range assetIDs {
		if assetID == "" || len(assetID) > 191 || strings.TrimSpace(assetID) != assetID {
			return nil, badParam(fmt.Errorf("invalid asset history id"))
		}
		if _, exists := seen[assetID]; exists {
			return nil, badParam(fmt.Errorf("duplicate asset history id %s", assetID))
		}
		seen[assetID] = struct{}{}
	}

	queries := query.Use(s.db)
	result := queries.ExecBatchResultRow.As("result")
	batch := queries.ExecBatchRow.As("batch")
	conditions := []gen.Condition{result.AssetID.In(assetIDs...)}
	conditions = append(conditions, execBatchHistoryOwnerConditions(ctx, batch.Alias())...)
	rows := []ExecBatchAssetHistory{}
	if err := result.WithContext(ctx).
		Select(result.AssetID, result.BatchID, result.Status, batch.StartedAt, batch.FinishedAt).
		Join(batch, result.OwnerID.EqCol(batch.OwnerID), result.BatchID.EqCol(batch.ID)).
		Where(conditions...).
		Order(batch.StartedAt.Desc(), batch.ID.Desc(), result.Position.Asc(), result.SessionID.Asc()).
		Scan(&rows); err != nil {
		return nil, dbError(err)
	}

	latest := make([]ExecBatchAssetHistory, 0, len(assetIDs))
	for _, row := range rows {
		if _, exists := seen[row.AssetID]; !exists {
			continue
		}
		delete(seen, row.AssetID)
		latest = append(latest, row)
	}
	return latest, nil
}
