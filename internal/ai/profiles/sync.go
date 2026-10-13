package profiles

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// syncOptInReader is implemented by the store: reads the AI profile sync opt-in (default on) for the account user.
type syncOptInReader interface {
	AIProfileSyncOptIn(ctx context.Context, userID string) (bool, error)
}

// syncTombstoneRecorder is implemented by the store: records sync tombstones produced by user-initiated deletes.
type syncTombstoneRecorder interface {
	SyncTombstoneRecord(ctx context.Context, id, kind string, deletedAt int64) error
}

// aiProfileSyncDeleter is implemented by the store: writes back profile state and records the delete tombstone by opt-in within one transaction (atomic delete).
type aiProfileSyncDeleter interface {
	AIProfilesDeleteTx(ctx context.Context, stateJSON string, tombstoneID string, deletedAt int64) error
}

// recordDeleteTombstone records the user delete tombstone by the deleter's opt-in after the profile
// delete is persisted (without the conflict triple). Opt-in is isolated per account user and defaults on:
// no tombstone when the deleter's identity is unavailable (desktop direct / anonymous mode) or opt-in is off;
// deleting a never-synced or sync-disabled profile must not delete the remote copy.
func (m *Manager) recordDeleteTombstone(ctx context.Context, id string) error {
	userID, ok := ipc.UserIDFromContext(ctx)
	if !ok {
		return nil
	}
	reader, ok := m.settings.(syncOptInReader)
	if !ok {
		return nil
	}
	enabled, err := reader.AIProfileSyncOptIn(ctx, userID)
	if err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	recorder, ok := m.settings.(syncTombstoneRecorder)
	if !ok {
		return nil
	}
	return recorder.SyncTombstoneRecord(ctx, id, store.SyncTombstoneKindAIProfile, ids.NowMS())
}
