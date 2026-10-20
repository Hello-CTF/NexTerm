package aicontext

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// sessionRequestContext elevates an identity-less caller to open access only
// in trusted-open shapes: a desktop deployment, an explicitly open
// deployment, or an internal shared-state caller already marked by the app
// (owner.WithOpenAccess). Server account mode never elevates here, so a
// gateway request without an identity keeps requester-less reads denied
// (ErrForbidden) instead of reaching every tab.
func sessionRequestContext(ctx context.Context) context.Context {
	if _, ok := ipc.UserIDFromContext(ctx); ok {
		return ctx
	}
	if owner.OpenAccess(ctx) {
		return session.WithOpenAccess(ctx)
	}
	return ctx
}

func WithSession(deps Dependencies, manager *session.Manager, storage *store.Store, database tools.Database) Dependencies {
	terminal := tools.SessionTerminal(manager)
	deps.Session = func(ctx context.Context, sessionID string) (SessionBrief, error) {
		ctx = sessionRequestContext(ctx)
		current, err := manager.SessionFor(ctx, sessionID)
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
		return manager.TailLinesFor(sessionRequestContext(ctx), tabID, count)
	}
	deps.TabSession = func(tabID string) string {
		tab, err := manager.Tab(tabID)
		if err != nil {
			return ""
		}
		return tab.SessionID
	}
	deps.Transport = func(ctx context.Context, sessionID string) (base.Transport, error) {
		return manager.TransportFor(sessionRequestContext(ctx), sessionID)
	}
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
