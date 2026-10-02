package aicontext

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
	"github.com/ProbiusOfficial/NexTerm/internal/session"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func WithSession(deps Dependencies, manager *session.Manager, storage *store.Store, database tools.Database) Dependencies {
	terminal := tools.SessionTerminal(manager)
	deps.Session = func(ctx context.Context, sessionID string) (SessionBrief, error) {
		current, err := manager.Session(sessionID)
		if err != nil {
			return SessionBrief{}, err
		}
		info := current.Info()
		asset := current.Asset()
		brief := SessionBrief{Name: info.Name, Kind: info.Kind}
		if brief.Name == "" {
			brief.Name = asset.Name
		}
		if storage != nil {
			row, err := storage.AssetGet(ctx, asset.ID)
			if err == nil {
				if row.Host != nil {
					brief.Host = *row.Host
					if row.Port != nil {
						brief.Host = fmt.Sprintf("%s:%d", brief.Host, *row.Port)
					}
				}
				if row.Username != nil {
					brief.Username = *row.Username
				}
				var options struct {
					CWD string `json:"cwd"`
				}
				if json.Unmarshal([]byte(row.OptionsJSON), &options) == nil {
					brief.CWD = options.CWD
				}
			}
		}
		return brief, nil
	}
	deps.Screen = terminal.Snapshot
	deps.Tail = func(ctx context.Context, tabID string, count int) ([]string, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return manager.TailLines(tabID, count)
	}
	deps.Transport = manager.Transport
	if database != nil {
		deps.Tables = func(ctx context.Context, connID string) ([]TableBrief, error) {
			tables, err := database.Tables(ctx, connID, "")
			if err != nil {
				return nil, err
			}
			result := make([]TableBrief, len(tables))
			for i, table := range tables {
				result[i] = TableBrief{Name: table}
			}
			return result, nil
		}
	}
	return deps
}
