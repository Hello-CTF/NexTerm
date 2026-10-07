package sync

import (
	"context"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/ProbiusOfficial/NexTerm/internal/vault"
)

// 本文件的测试全部跑在真实 PostgreSQL 上, 由 NEXTERM_TEST_PG_DSN 门控;
// 未配置 DSN 时逐测试 SKIP, 不得视为已验证。

func TestPostgresApplyKnownHostAndAIProfile(t *testing.T) {
	instance := newPostgresTestInstance(t, true)
	ctx := context.Background()

	hostID := ids.New()
	object := applyKnownHost(t, hostID, "pg.example.com", 22, "ssh-ed25519", "SHA256:pg", 100)
	requireApplyResult(t, mustApplyObjects(t, instance, object).Objects[0], ApplyResultApplied)
	requireApplyResult(t, mustApplyObjects(t, instance, object).Objects[0], ApplyResultIdentical)
	row, found, err := instance.service.engine.knownHostByID(ctx, hostID)
	if err != nil || !found || row.Fingerprint != "SHA256:pg" || row.AddedAt != 100 {
		t.Fatalf("known host=%+v found=%v err=%v", row, found, err)
	}

	profileID := ids.New()
	payload := fullTestAIProfile(profileID)
	requireApplyResult(t, mustApplyObjects(t, instance, applyAIProfile(t, payload)).Objects[0], ApplyResultApplied)
	requireApplyResult(t, mustApplyObjects(t, instance, applyAIProfile(t, payload)).Objects[0], ApplyResultIdentical)
	state, revision := requireAIProfileState(t, instance)
	record, exists := state.find(profileID)
	if !exists || revision != payload.UpdatedAt {
		t.Fatalf("profile=%+v exists=%v revision=%d", record, exists, revision)
	}
	if !strings.HasPrefix(record.APIKey, store.SecretEnvelopePrefix) {
		t.Fatalf("apiKey must be stored as envelope: %q", record.APIKey)
	}
	stored, found, err := instance.db.SettingGet(ctx, store.AIProfilesSettingKey)
	if err != nil || !found || strings.Contains(stored, payload.APIKey) {
		t.Fatalf("setting must not persist plaintext key: found=%v err=%v", found, err)
	}

	requireApplyResult(t, mustApplyObjects(t, instance, applyKnownHostTombstone(t, hostID, 200)).Objects[0], ApplyResultApplied)
	if _, found, err := instance.service.engine.knownHostByID(ctx, hostID); err != nil || found {
		t.Fatalf("known host must be deleted: found=%v err=%v", found, err)
	}
	requireApplyResult(t, mustApplyObjects(t, instance, applyAIProfileTombstone(t, profileID, 200)).Objects[0], ApplyResultApplied)
	if state, _ := requireAIProfileState(t, instance); len(state.Profiles) != 0 {
		t.Fatalf("profile must be deleted: %+v", state.Profiles)
	}
}

func TestPostgresEngineSettingsSyncE2E(t *testing.T) {
	db := newPostgresSyncStore(t)
	server := newTestSyncServerOn(t, db)
	ctx := context.Background()

	_, envelopes, _, err := vault.GenerateUserDEKEnvelopes("alice-pw-123")
	if err != nil {
		t.Fatal(err)
	}
	user, err := server.accounts.CreateUserWithEnvelopes(ctx, "alice", "alice", "alice-pw-123", envelopes)
	if err != nil {
		t.Fatal(err)
	}
	server.authBypassUserID = user.ID

	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)

	hostID := ids.New()
	putDeviceKnownHost(t, deviceA, hostID, "pg.example.com", 22, "ssh-ed25519", "SHA256:pg-sync", 100)
	profileID := ids.New()
	putDeviceSealedAIProfile(t, deviceA, profileID, "PG 档案", "pg-key-值", 100)

	reportA := syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	if reportA.Pushed != 2 {
		t.Fatalf("deviceA pushed=%d want 2: %+v", reportA.Pushed, reportA)
	}
	var serverObjects int
	if err := db.DB().QueryRowContext(ctx, "SELECT count(*) FROM user_sync_object WHERE user_id = ?", user.ID).Scan(&serverObjects); err != nil || serverObjects != 2 {
		t.Fatalf("server objects=%d err=%v", serverObjects, err)
	}

	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	row, found := deviceKnownHost(t, deviceB, hostID)
	if !found || row.Fingerprint != "SHA256:pg-sync" || row.AddedAt != 100 {
		t.Fatalf("deviceB known host=%+v found=%v", row, found)
	}
	record, exists := deviceAIProfileState(t, deviceB).find(profileID)
	if !exists {
		t.Fatal("deviceB missing profile")
	}
	key, err := deviceB.vault.DecryptSecret(ctx, record.APIKey)
	if err != nil || key != "pg-key-值" {
		t.Fatalf("deviceB key=%q err=%v", key, err)
	}

	// A 删除已知主机与档案: 墓碑经服务端(PG)推送到 B, B 端副本被清除且不复活。
	deleteDeviceKnownHost(t, deviceA, hostID, ids.NowMS())
	deleteDeviceAIProfile(t, deviceA, profileID, ids.NowMS())
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if _, found := deviceKnownHost(t, deviceB, hostID); found {
		t.Fatal("known host must be tombstoned on B")
	}
	if state := deviceAIProfileState(t, deviceB); len(state.Profiles) != 0 {
		t.Fatalf("profile must be tombstoned on B: %+v", state.Profiles)
	}

	requireDeviceQuiescent(t, deviceA, server, "alice", "alice-pw-123")
	requireDeviceQuiescent(t, deviceB, server, "alice", "alice-pw-123")
}
