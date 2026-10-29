package production

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// sessionAssetBackupBeforeOverwrite reads the stored options of the session's asset, not the snapshot the connected
// session carries, so toggling the option applies without reconnecting. Any lookup failure means off.
func sessionAssetBackupBeforeOverwrite(ctx context.Context, sessions *session.Manager, database *store.Store, sessionID string) bool {
	if sessions == nil || database == nil || sessionID == "" {
		return false
	}
	connected, err := sessions.SessionFor(productionSessionRequestContext(ctx), sessionID)
	if err != nil {
		return false
	}
	row, err := database.AssetGetLive(ctx, connected.Asset().ID)
	if err != nil {
		return false
	}
	return store.AssetBackupBeforeOverwrite(row.OptionsJSON)
}
