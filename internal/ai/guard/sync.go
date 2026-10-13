package guard

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// Sync object ID prefixes shared with internal/sync: device grants, grant rules, and per-device permission modes
// occupy one object per entity under the ai_grant kind.
const (
	AIGrantSyncDevicePrefix = "device/"
	AIGrantSyncRulePrefix   = "rule/"
	AIGrantSyncModePrefix   = "mode/"
)

// aiGrantSyncOptInReader is implemented by the store: reads the AI grant sync opt-in (default on) for the account user.
type aiGrantSyncOptInReader interface {
	AIGrantSyncOptIn(ctx context.Context, userID string) (bool, error)
}

// aiGrantSyncTombstoneRecorder is implemented by the store: records sync tombstones produced by user-initiated revocations.
type aiGrantSyncTombstoneRecorder interface {
	SyncTombstoneRecord(ctx context.Context, id, kind string, deletedAt int64) error
}

// recordAIGrantTombstone records the user revocation tombstone after the revocation is persisted, keyed by the opt-in:
// no tombstone when the revoker's identity is unavailable (desktop direct / anonymous mode) or opt-in is off;
// revoking a never-synced or sync-disabled grant must not delete the remote copy (same semantics as profile deletes).
func recordAIGrantTombstone(ctx context.Context, settings Settings, id string) error {
	userID, ok := ipc.UserIDFromContext(ctx)
	if !ok {
		return nil
	}
	reader, ok := settings.(aiGrantSyncOptInReader)
	if !ok {
		return nil
	}
	enabled, err := reader.AIGrantSyncOptIn(ctx, userID)
	if err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	recorder, ok := settings.(aiGrantSyncTombstoneRecorder)
	if !ok {
		return nil
	}
	return recorder.SyncTombstoneRecord(ctx, id, store.SyncTombstoneKindAIGrant, ids.NowMS())
}
