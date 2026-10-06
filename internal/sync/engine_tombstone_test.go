package sync

import (
	"context"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
)

// opt-out 后重新 opt-in 被拒绝: 服务端墓碑保持, 本地记录保留, 不再重推删除。
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

	// A opt-out: 墓碑推送, B 端副本被清除。
	if err := deviceA.db.TranscriptSetSyncOptIn(ctx, transcriptID, false); err != nil {
		t.Fatal(err)
	}
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if _, err := deviceB.db.TranscriptGet(ctx, transcriptID); !isNotFound(err) {
		t.Fatalf("opt-out transcript still on B: %v", err)
	}

	// A 重新 opt-in 被拒绝; 墓碑与本地记录保持, 后续同步不重推删除。
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
	// B 端副本保持删除。
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

// 旧墓碑 + 较新对象: 较新 group/snippet/凭据胜过旧墓碑后必须清除本地墓碑,
// 后续收集不得再由墓碑覆盖对象或回推删除, 双设备收敛后空闲同步零推送。
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

	// A 删除三者并同步, 墓碑到达服务端。
	if _, err := deviceA.db.DB().ExecContext(ctx, "DELETE FROM asset_group WHERE id=?", groupID); err != nil {
		t.Fatal(err)
	}
	if _, err := deviceA.db.DB().ExecContext(ctx, "DELETE FROM snippet WHERE id=?", snippetID); err != nil {
		t.Fatal(err)
	}
	if err := deviceA.db.CredentialDelete(ctx, credentialID); err != nil {
		t.Fatal(err)
	}
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")

	// B 离线编辑出较新版本并推送, 覆盖服务端墓碑。修订号取未来时间, 保证胜过墓碑的真实删除时间。
	future := ids.NowMS() + 3600_000
	putDeviceGroup(t, deviceB, groupID, nil, "B 的新分组", future)
	putDeviceSnippet(t, deviceB, snippetID, "B 的新片段", "new-body", future)
	putDeviceCredential(t, deviceB, credentialID, "B 的新凭据", "new-value", future)
	if _, err := deviceB.db.DB().ExecContext(ctx, "UPDATE credential SET updated_at=? WHERE id=?", future, credentialID); err != nil {
		t.Fatal(err)
	}
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")

	// A 拉取: 较新对象胜过旧墓碑并被应用, 本地墓碑必须被清除。
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

	// 服务端必须保留对象而非墓碑: A 第二轮零推送, B 保持静默。
	reportA := syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	if reportA.Pushed != 0 {
		t.Fatalf("idle sync must not re-push tombstones: %+v", reportA)
	}
	reportB := syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if reportB.Pulled+reportB.Applied+reportB.Pushed != 0 {
		t.Fatalf("deviceB must be quiescent: %+v", reportB)
	}
}
