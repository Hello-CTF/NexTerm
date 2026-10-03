package tools

import (
	"context"
	"fmt"

	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func WithStore(deps Dependencies, storage *store.Store, database Database) Dependencies {
	if database != nil {
		deps.Database = database
	}
	if storage == nil {
		return deps
	}
	deps.ListAssets = func(ctx context.Context) ([]Asset, error) {
		rows, err := storage.AssetList(ctx, false)
		if err != nil {
			return nil, err
		}
		assets := make([]Asset, len(rows))
		for i, row := range rows {
			asset := Asset{ID: row.ID, Name: row.Name, Kind: row.Kind}
			if row.Host != nil {
				asset.Host = *row.Host
			}
			if row.Port != nil {
				asset.Host = fmt.Sprintf("%s:%d", asset.Host, *row.Port)
			}
			if row.Username != nil {
				asset.Username = *row.Username
			}
			assets[i] = asset
		}
		return assets, nil
	}
	deps.Audit = func(ctx context.Context, entry AuditEntry) error {
		input := store.AuditInput{Source: "ai", Kind: entry.Kind, Payload: entry.Payload}
		if entry.SessionID != "" {
			input.SessionID = &entry.SessionID
		}
		if entry.AssetID != "" {
			input.AssetID = &entry.AssetID
		}
		code := int32(entry.ExitCode)
		input.ExitCode = &code
		duration := entry.DurationMS
		input.DurationMS = &duration
		return storage.AuditInsert(ctx, input)
	}
	return deps
}
