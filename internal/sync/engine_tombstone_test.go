package sync

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ids"
)

// Re-opt-in after opt-out is rejected: the server-side tombstone stays, the local record is kept, and the deletion is not re-pushed.
func TestEngineTranscriptReOptInRejectedAfterOptOut(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	ctx := context.Background()

	endedAt := ids.NowMS() - 1000
	transcriptID := ids.New()
	putDeviceTranscript(t, deviceA, transcriptID, endedAt-500, &endedAt, []byte("output"), true)
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")

	// A opts out: the tombstone is pushed and B's copy is cleared.
	if err := deviceA.db.TranscriptSetSyncOptIn(ctx, transcriptID, false); err != nil {
		t.Fatal(err)
	}
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if _, err := deviceB.db.TranscriptGet(ctx, transcriptID); !isNotFound(err) {
		t.Fatalf("opt-out transcript still on B: %v", err)
	}

	// A re-opts-in and is rejected; the tombstone and the local record stay, and later syncs do not re-push the deletion.
	if err := deviceA.db.TranscriptSetSyncOptIn(ctx, transcriptID, true); err == nil {
		t.Fatal("re-opt-in after opt-out must be rejected")
	}
	if _, err := deviceA.db.TranscriptGet(ctx, transcriptID); err != nil {
		t.Fatalf("local record must stay on A: %v", err)
	}
	assertDeviceTombstone(t, deviceA, transcriptID, KindTranscript)
	reportA := syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	if reportA.Pushed != 0 {
		t.Fatalf("rejected re-opt-in must not re-push: %+v", reportA)
	}
	// B's copy stays deleted.
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if _, err := deviceB.db.TranscriptGet(ctx, transcriptID); !isNotFound(err) {
		t.Fatalf("B copy resurrected: %v", err)
	}
}

func assertDeviceTombstone(t *testing.T, device *testDevice, id, kind string) {
	t.Helper()
	var gotKind string
	var deletedAt int64
	err := device.db.DB().QueryRowContext(context.Background(),
		"SELECT kind, deleted_at FROM sync_tombstone WHERE id=?", id).Scan(&gotKind, &deletedAt)
	if err != nil {
		t.Fatalf("sync_tombstone %s: %v", id, err)
	}
	if gotKind != kind || deletedAt <= 0 {
		t.Fatalf("sync_tombstone %s = kind %s deleted_at %d", id, gotKind, deletedAt)
	}
}

// Stale tombstone plus newer object: after a newer group/snippet/credential beats the old tombstone, the local tombstone must be cleared,
// later collection must not overwrite the object with the tombstone or re-push the deletion, and idle sync pushes nothing after both devices converge.
func TestEngineNewerObjectClearsStaleTombstone(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	ctx := context.Background()

	groupID := ids.New()
	putDeviceGroup(t, deviceA, groupID, nil, "原始分组", 100)
	snippetID := ids.New()
	putDeviceSnippet(t, deviceA, snippetID, "原始片段", "body", 100)
	credentialID := ids.New()
	putDeviceCredential(t, deviceA, credentialID, "原始凭据", "value", 100)
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")

	// A deletes all three and syncs; the tombstones reach the server.
	if err := deviceA.db.GroupDelete(ctx, groupID); err != nil {
		t.Fatal(err)
	}
	if err := deviceA.db.SnippetDelete(ctx, snippetID); err != nil {
		t.Fatal(err)
	}
	if err := deviceA.db.CredentialDelete(ctx, credentialID); err != nil {
		t.Fatal(err)
	}
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")

	// B edits newer versions offline and pushes, overwriting the server-side tombstone. Revisions are set in the future to guarantee beating the tombstone's real deletion time.
	future := ids.NowMS() + 3600_000
	putDeviceGroup(t, deviceB, groupID, nil, "B 的新分组", future)
	putDeviceSnippet(t, deviceB, snippetID, "B 的新片段", "new-body", future)
	putDeviceCredential(t, deviceB, credentialID, "B 的新凭据", "new-value", future)
	if _, err := deviceB.db.DB().ExecContext(ctx, "UPDATE credential SET updated_at=? WHERE id=?", future, credentialID); err != nil {
		t.Fatal(err)
	}
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")

	// A pulls: the newer object beats the stale tombstone and is applied; the local tombstone must be cleared.
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	group, err := deviceA.db.GroupGet(ctx, groupID)
	if err != nil || group.Name != "B 的新分组" {
		t.Fatalf("deviceA group=%+v err=%v", group, err)
	}
	if _, found, err := deviceA.engine.syncTombstoneGet(ctx, groupID); err != nil || found {
		t.Fatalf("group tombstone must be cleared: found=%v err=%v", found, err)
	}
	if _, found, err := deviceA.engine.syncTombstoneGet(ctx, snippetID); err != nil || found {
		t.Fatalf("snippet tombstone must be cleared: found=%v err=%v", found, err)
	}
	if _, err := deviceA.db.CredentialGetRow(ctx, credentialID); err != nil {
		t.Fatalf("credential must exist on A: %v", err)
	}

	// The server must keep the object, not the tombstone: A's second round pushes nothing and B stays quiescent.
	reportA := syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	if reportA.Pushed != 0 {
		t.Fatalf("idle sync must not re-push tombstones: %+v", reportA)
	}
	reportB := syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if reportB.Pulled+reportB.Applied+reportB.Pushed != 0 {
		t.Fatalf("deviceB must be quiescent: %+v", reportB)
	}
}
