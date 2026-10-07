package sync

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/profiles"
	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func applyKnownHost(t *testing.T, id, host string, port int32, keyType, fingerprint string, addedAt int64) ApplyObject {
	t.Helper()
	return ApplyObject{ID: id, Kind: KindKnownHost, Payload: applyPayload(t, knownHostObject{
		ID: id, Host: host, Port: port, KeyType: keyType, Fingerprint: fingerprint, AddedAt: addedAt,
	})}
}

func applyKnownHostTombstone(t *testing.T, id string, deletedAt int64) ApplyObject {
	t.Helper()
	return ApplyObject{ID: id, Kind: KindTombstone, Payload: applyPayload(t, tombstoneObject{
		TargetKind: KindKnownHost, DeletedAt: deletedAt,
	})}
}

func applyAIProfile(t *testing.T, payload aiProfileObject) ApplyObject {
	t.Helper()
	return ApplyObject{ID: payload.ID, Kind: KindAIProfile, Payload: applyPayload(t, payload)}
}

func applyAIProfileTombstone(t *testing.T, id string, deletedAt int64) ApplyObject {
	t.Helper()
	return ApplyObject{ID: id, Kind: KindTombstone, Payload: applyPayload(t, tombstoneObject{
		TargetKind: KindAIProfile, DeletedAt: deletedAt,
	})}
}

func fullTestAIProfile(id string) aiProfileObject {
	return aiProfileObject{
		ID: id, Name: "同步档案", BaseURL: "https://ai.example.com/v1", APIKey: "profile-secret-值",
		Model: "model-x", FallbackModel: "model-y", Temperature: 0.7, ContextWindow: 128000,
		MaxTokens: testPtr(4096), Proxy: testPtr("http://proxy.example.com:8080"), Stream: true,
		RequestTimeoutSeconds: testPtr(120), IdleTimeoutSeconds: testPtr(30),
		CircuitFailureThreshold: testPtr(5), CircuitCooldownSeconds: testPtr(60),
		UpdatedAt: 100,
	}
}

func requireKnownHost(t *testing.T, instance *testInstance, id, fingerprint string, addedAt int64) store.KnownHostRow {
	t.Helper()
	row, found, err := instance.service.engine.knownHostByID(context.Background(), id)
	if err != nil || !found {
		t.Fatalf("known host %s not found: found=%v err=%v", id, found, err)
	}
	if fingerprint != "" && row.Fingerprint != fingerprint {
		t.Fatalf("known host %s fingerprint=%q, want %q", id, row.Fingerprint, fingerprint)
	}
	if addedAt != 0 && row.AddedAt != addedAt {
		t.Fatalf("known host %s revision=%d, want %d", id, row.AddedAt, addedAt)
	}
	return row
}

func requireNoKnownHost(t *testing.T, instance *testInstance, id string) {
	t.Helper()
	if _, found, err := instance.service.engine.knownHostByID(context.Background(), id); err != nil || found {
		t.Fatalf("known host %s must be absent: found=%v err=%v", id, found, err)
	}
}

func requireSyncTombstone(t *testing.T, instance *testInstance, id, kind string, deletedAt int64) {
	t.Helper()
	row, found, err := instance.service.engine.syncTombstoneGet(context.Background(), id)
	if err != nil || !found {
		t.Fatalf("tombstone %s not found: found=%v err=%v", id, found, err)
	}
	if row.Kind != kind || row.DeletedAt != deletedAt {
		t.Fatalf("tombstone %s=%+v, want kind=%s deletedAt=%d", id, row, kind, deletedAt)
	}
}

func requireNoSyncTombstone(t *testing.T, instance *testInstance, id string) {
	t.Helper()
	if _, found, err := instance.service.engine.syncTombstoneGet(context.Background(), id); err != nil || found {
		t.Fatalf("tombstone %s must be cleared: found=%v err=%v", id, found, err)
	}
}

func requireAIProfileState(t *testing.T, instance *testInstance) (aiProfilesState, int64) {
	t.Helper()
	state, revision, _, err := instance.service.engine.aiProfilesLoad(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return state, revision
}

func putTestAIProfile(t *testing.T, instance *testInstance, record aiProfileRecord, updatedAt int64) {
	t.Helper()
	engine := instance.service.engine
	state, _, _, err := engine.aiProfilesLoad(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	state.upsert(record)
	state.ensureActive()
	if err := engine.aiProfilesSave(context.Background(), state, updatedAt, ""); err != nil {
		t.Fatal(err)
	}
}

func sealedTestAIProfile(t *testing.T, instance *testInstance, id, secret string) aiProfileRecord {
	t.Helper()
	envelope, err := instance.vault.EncryptSecret(context.Background(), secret)
	if err != nil {
		t.Fatal(err)
	}
	return aiProfileRecord{ID: id, Name: "本地档案", BaseURL: "https://local.example.com", APIKey: envelope, Model: "local-model", Stream: true}
}

func TestApplyKnownHostRoundtripIdempotent(t *testing.T) {
	instance := newTestInstance(t, false)
	hostID := ids.New()
	object := applyKnownHost(t, hostID, "git.example.com", 22, "ssh-ed25519", "SHA256:aaa", 100)

	requireApplyResult(t, mustApplyObjects(t, instance, object).Objects[0], ApplyResultApplied)
	row := requireKnownHost(t, instance, hostID, "SHA256:aaa", 100)
	if row.Host != "git.example.com" || row.Port != 22 || row.KeyType != "ssh-ed25519" {
		t.Fatalf("known host fields mangled: %+v", row)
	}
	requireApplyResult(t, mustApplyObjects(t, instance, object).Objects[0], ApplyResultIdentical)
}

func TestApplyKnownHostNoRevisionRollback(t *testing.T) {
	instance := newTestInstance(t, false)
	ctx := context.Background()
	hostID := ids.New()
	if _, err := instance.db.DB().ExecContext(ctx,
		`INSERT INTO known_host(id, host, port, key_type, fingerprint, added_at) VALUES(?,?,?,?,?,?)`,
		hostID, "git.example.com", 22, "ssh-ed25519", "SHA256:new", 200); err != nil {
		t.Fatal(err)
	}

	result := mustApplyObjects(t, instance, applyKnownHost(t, hostID, "git.example.com", 22, "ssh-ed25519", "SHA256:old", 100))
	requireApplyResult(t, result.Objects[0], ApplyResultSkipped)
	requireKnownHost(t, instance, hostID, "SHA256:new", 200)
}

func TestApplyKnownHostTombstoneLifecycle(t *testing.T) {
	instance := newTestInstance(t, false)
	hostID := ids.New()
	mustApplyObjects(t, instance, applyKnownHost(t, hostID, "git.example.com", 22, "ssh-ed25519", "SHA256:aaa", 100))

	requireApplyResult(t, mustApplyObjects(t, instance, applyKnownHostTombstone(t, hostID, 200)).Objects[0], ApplyResultApplied)
	requireNoKnownHost(t, instance, hostID)
	requireSyncTombstone(t, instance, hostID, KindKnownHost, 200)

	stale := mustApplyObjects(t, instance, applyKnownHost(t, hostID, "git.example.com", 22, "ssh-ed25519", "SHA256:old", 150))
	requireApplyResult(t, stale.Objects[0], ApplyResultSkipped)
	requireNoKnownHost(t, instance, hostID)

	newer := mustApplyObjects(t, instance, applyKnownHost(t, hostID, "git.example.com", 22, "ssh-ed25519", "SHA256:new", 300))
	requireApplyResult(t, newer.Objects[0], ApplyResultApplied)
	requireKnownHost(t, instance, hostID, "SHA256:new", 300)
	requireNoSyncTombstone(t, instance, hostID)
}

func TestApplyKnownHostTripleConflictDisplacesLoser(t *testing.T) {
	instance := newTestInstance(t, false)
	localID := ids.New()
	remoteID := ids.New()
	mustApplyObjects(t, instance, applyKnownHost(t, localID, "git.example.com", 22, "ssh-ed25519", "SHA256:local", 100))

	result := mustApplyObjects(t, instance, applyKnownHost(t, remoteID, "git.example.com", 22, "ssh-ed25519", "SHA256:remote", 200))
	requireApplyResult(t, result.Objects[0], ApplyResultApplied)
	requireNoKnownHost(t, instance, localID)
	requireKnownHost(t, instance, remoteID, "SHA256:remote", 200)
	requireSyncTombstone(t, instance, localID, KindKnownHost, 200)

	stale := mustApplyObjects(t, instance, applyKnownHost(t, localID, "git.example.com", 22, "ssh-ed25519", "SHA256:local", 100))
	requireApplyResult(t, stale.Objects[0], ApplyResultSkipped)
	requireNoKnownHost(t, instance, localID)
}

func TestApplyKnownHostTripleConflictLocalNewerKeepsRow(t *testing.T) {
	instance := newTestInstance(t, false)
	localID := ids.New()
	remoteID := ids.New()
	mustApplyObjects(t, instance, applyKnownHost(t, localID, "git.example.com", 22, "ssh-ed25519", "SHA256:local", 300))

	result := mustApplyObjects(t, instance, applyKnownHost(t, remoteID, "git.example.com", 22, "ssh-ed25519", "SHA256:remote", 200))
	requireApplyResult(t, result.Objects[0], ApplyResultSkipped)
	requireKnownHost(t, instance, localID, "SHA256:local", 300)
	requireNoKnownHost(t, instance, remoteID)
	requireNoSyncTombstone(t, instance, localID)
}

// 同修订不同 ID 的同行记录必须以规范载荷哈希决胜: 两种应用顺序收敛到同一终态。
func TestApplyKnownHostEqualRevisionConverges(t *testing.T) {
	hostA := ids.New()
	hostB := ids.New()
	objectA := applyKnownHost(t, hostA, "git.example.com", 22, "ssh-ed25519", "SHA256:aaa", 100)
	objectB := applyKnownHost(t, hostB, "git.example.com", 22, "ssh-ed25519", "SHA256:bbb", 100)

	finalRow := func(objects ...ApplyObject) store.KnownHostRow {
		t.Helper()
		instance := newTestInstance(t, false)
		mustApplyObjects(t, instance, objects...)
		rows, err := instance.db.KnownHostList(context.Background())
		if err != nil || len(rows) != 1 {
			t.Fatalf("rows=%+v err=%v", rows, err)
		}
		return rows[0]
	}

	rowAB := finalRow(objectA, objectB)
	rowBA := finalRow(objectB, objectA)
	if rowAB.ID != rowBA.ID || rowAB.Fingerprint != rowBA.Fingerprint {
		t.Fatalf("equal-revision LWW depends on order: AB=%+v BA=%+v", rowAB, rowBA)
	}
}

func TestApplyAIProfileRoundtripPreservesFieldsAndSealsKey(t *testing.T) {
	instance := newTestInstance(t, true)
	profileID := ids.New()
	payload := fullTestAIProfile(profileID)

	requireApplyResult(t, mustApplyObjects(t, instance, applyAIProfile(t, payload)).Objects[0], ApplyResultApplied)

	state, revision := requireAIProfileState(t, instance)
	if revision != payload.UpdatedAt {
		t.Fatalf("setting revision=%d, want %d", revision, payload.UpdatedAt)
	}
	record, exists := state.find(profileID)
	if !exists {
		t.Fatal("profile missing after apply")
	}
	if record.Name != payload.Name || record.BaseURL != payload.BaseURL || record.Model != payload.Model ||
		record.FallbackModel != payload.FallbackModel || record.Temperature != payload.Temperature ||
		record.ContextWindow != payload.ContextWindow || record.MaxTokens == nil || *record.MaxTokens != *payload.MaxTokens ||
		record.Proxy == nil || *record.Proxy != *payload.Proxy || record.Stream != payload.Stream ||
		record.RequestTimeoutSeconds == nil || *record.RequestTimeoutSeconds != *payload.RequestTimeoutSeconds ||
		record.IdleTimeoutSeconds == nil || *record.IdleTimeoutSeconds != *payload.IdleTimeoutSeconds ||
		record.CircuitFailureThreshold == nil || *record.CircuitFailureThreshold != *payload.CircuitFailureThreshold ||
		record.CircuitCooldownSeconds == nil || *record.CircuitCooldownSeconds != *payload.CircuitCooldownSeconds {
		t.Fatalf("profile fields not preserved: %+v", record)
	}
	if !strings.HasPrefix(record.APIKey, store.SecretEnvelopePrefix) {
		t.Fatalf("apiKey must be stored as envelope, got %q", record.APIKey)
	}
	stored, found, err := instance.db.SettingGet(context.Background(), store.AIProfilesSettingKey)
	if err != nil || !found {
		t.Fatalf("setting read: found=%v err=%v", found, err)
	}
	if strings.Contains(stored, payload.APIKey) {
		t.Fatal("plaintext apiKey persisted in setting")
	}
	if state.ActiveID == nil || *state.ActiveID != profileID {
		t.Fatalf("first profile must become active: %+v", state.ActiveID)
	}

	plaintext, err := instance.vault.DecryptSecret(context.Background(), record.APIKey)
	if err != nil || plaintext != payload.APIKey {
		t.Fatalf("stored envelope must open to the payload key: %q err=%v", plaintext, err)
	}

	// 落盘形态重建的规范载荷必须与应用载荷逐字节一致(含 reveal 后的 apiKey)。
	objects, err := instance.service.engine.collectLocalObjects(context.Background(), &SyncReport{})
	if err != nil {
		t.Fatal(err)
	}
	local, exists := objects[profileID]
	if !exists || local.kind != KindAIProfile {
		t.Fatalf("collected objects missing profile: %+v", local)
	}
	if string(local.plaintext) != string(applyPayload(t, payload)) {
		t.Fatalf("collect roundtrip mismatch:\n%s\n%s", local.plaintext, applyPayload(t, payload))
	}

	requireApplyResult(t, mustApplyObjects(t, instance, applyAIProfile(t, payload)).Objects[0], ApplyResultIdentical)
}

func TestApplyAIProfileNoRevisionRollback(t *testing.T) {
	instance := newTestInstance(t, true)
	profileID := ids.New()
	putTestAIProfile(t, instance, aiProfileRecord{ID: profileID, Name: "本地新名", Model: "m", Stream: true}, 200)

	stale := fullTestAIProfile(profileID)
	stale.Name = "远端旧名"
	stale.APIKey = ""
	stale.UpdatedAt = 100
	requireApplyResult(t, mustApplyObjects(t, instance, applyAIProfile(t, stale)).Objects[0], ApplyResultSkipped)

	state, revision := requireAIProfileState(t, instance)
	if revision != 200 {
		t.Fatalf("revision rolled back to %d", revision)
	}
	record, _ := state.find(profileID)
	if record.Name != "本地新名" {
		t.Fatalf("older payload rolled back local profile: %+v", record)
	}
}

func TestApplyAIProfileTombstoneLifecycle(t *testing.T) {
	instance := newTestInstance(t, true)
	profileID := ids.New()
	payload := fullTestAIProfile(profileID)
	mustApplyObjects(t, instance, applyAIProfile(t, payload))

	requireApplyResult(t, mustApplyObjects(t, instance, applyAIProfileTombstone(t, profileID, 200)).Objects[0], ApplyResultApplied)
	state, _ := requireAIProfileState(t, instance)
	if _, exists := state.find(profileID); exists {
		t.Fatal("profile must be removed by tombstone")
	}
	requireSyncTombstone(t, instance, profileID, KindAIProfile, 200)

	stale := fullTestAIProfile(profileID)
	stale.UpdatedAt = 150
	requireApplyResult(t, mustApplyObjects(t, instance, applyAIProfile(t, stale)).Objects[0], ApplyResultSkipped)
	if state, _ := requireAIProfileState(t, instance); len(state.Profiles) != 0 {
		t.Fatalf("stale profile resurrected under tombstone: %+v", state.Profiles)
	}

	newer := fullTestAIProfile(profileID)
	newer.Name = "新版本档案"
	newer.UpdatedAt = 300
	requireApplyResult(t, mustApplyObjects(t, instance, applyAIProfile(t, newer)).Objects[0], ApplyResultApplied)
	state, _ = requireAIProfileState(t, instance)
	record, exists := state.find(profileID)
	if !exists || record.Name != "新版本档案" {
		t.Fatalf("newer profile must win: %+v", record)
	}
	requireNoSyncTombstone(t, instance, profileID)
}

func TestApplyAIProfileKeylessAppliesWhileVaultLocked(t *testing.T) {
	instance := newTestInstance(t, true)
	instance.vault.Lock()
	keyless := fullTestAIProfile(ids.New())
	keyless.APIKey = ""

	requireApplyResult(t, mustApplyObjects(t, instance, applyAIProfile(t, keyless)).Objects[0], ApplyResultApplied)

	keyed := fullTestAIProfile(ids.New())
	result := mustApplyObjects(t, instance, applyAIProfile(t, keyed))
	requireApplyResult(t, result.Objects[0], ApplyResultSkipped)
	if !strings.Contains(result.Objects[0].Warning, "凭据库") {
		t.Fatalf("locked vault warning=%q", result.Objects[0].Warning)
	}
	state, _ := requireAIProfileState(t, instance)
	if _, exists := state.find(keyed.ID); exists {
		t.Fatal("keyed profile must not apply while vault locked")
	}
	if _, exists := state.find(keyless.ID); !exists {
		t.Fatal("keyless profile must apply while vault locked")
	}
}

func TestApplyAIProfileKeyedSkippedWhenVaultUnavailable(t *testing.T) {
	instance := newTestInstance(t, false)
	profileID := ids.New()
	result := mustApplyObjects(t, instance, applyAIProfile(t, fullTestAIProfile(profileID)))
	requireApplyResult(t, result.Objects[0], ApplyResultSkipped)
	if !strings.Contains(result.Objects[0].Warning, "凭据库") {
		t.Fatalf("unavailable vault warning=%q", result.Objects[0].Warning)
	}
}

// 档案共享 LWW 时钟: 较旧对象不得拉低设置修订号, 本地较新编辑不被回滚。
func TestApplyAIProfileSharedClockNeverLowersRevision(t *testing.T) {
	instance := newTestInstance(t, true)
	keeperID := ids.New()
	putTestAIProfile(t, instance, aiProfileRecord{ID: keeperID, Name: "keeper", Model: "m", Stream: true}, 500)

	older := fullTestAIProfile(ids.New())
	older.UpdatedAt = 100
	requireApplyResult(t, mustApplyObjects(t, instance, applyAIProfile(t, older)).Objects[0], ApplyResultApplied)

	_, revision := requireAIProfileState(t, instance)
	if revision != 500 {
		t.Fatalf("blob revision lowered to %d", revision)
	}
}

// 同步落盘的 ai.models 必须能被 profiles.Manager 直接消费: 字段不漂移, 密钥经本地凭据库可解析。
func TestApplyAIProfileReadableByProfilesManager(t *testing.T) {
	instance := newTestInstance(t, true)
	profileID := ids.New()
	payload := fullTestAIProfile(profileID)
	requireApplyResult(t, mustApplyObjects(t, instance, applyAIProfile(t, payload)).Objects[0], ApplyResultApplied)

	manager, err := profiles.NewManager(context.Background(), instance.db)
	if err != nil {
		t.Fatal(err)
	}
	overview := manager.Overview()
	if len(overview.Profiles) != 1 || overview.Profiles[0].ID != profileID {
		t.Fatalf("overview=%+v", overview)
	}
	if overview.Profiles[0].APIKey != profiles.MaskedAPIKey {
		t.Fatalf("overview must mask the key: %q", overview.Profiles[0].APIKey)
	}
	profile, exists := manager.Profile(profileID)
	if !exists {
		t.Fatal("profile missing from manager")
	}
	if profile.Name != payload.Name || profile.BaseURL != payload.BaseURL || profile.Model != payload.Model ||
		profile.FallbackModel != payload.FallbackModel || profile.Temperature != payload.Temperature ||
		profile.ContextWindow != payload.ContextWindow || profile.MaxTokens == nil || *profile.MaxTokens != *payload.MaxTokens ||
		profile.Proxy == nil || *profile.Proxy != *payload.Proxy || profile.Stream != payload.Stream ||
		profile.RequestTimeoutSeconds == nil || *profile.RequestTimeoutSeconds != *payload.RequestTimeoutSeconds ||
		profile.IdleTimeoutSeconds == nil || *profile.IdleTimeoutSeconds != *payload.IdleTimeoutSeconds ||
		profile.CircuitFailureThreshold == nil || *profile.CircuitFailureThreshold != *payload.CircuitFailureThreshold ||
		profile.CircuitCooldownSeconds == nil || *profile.CircuitCooldownSeconds != *payload.CircuitCooldownSeconds {
		t.Fatalf("manager sees drifted fields: %+v", profile)
	}
	config, ok := manager.ActiveConfig()
	if !ok || config.APIKey != payload.APIKey {
		t.Fatalf("manager must resolve the applied key: %+v ok=%v", config, ok)
	}
}

func TestApplySettingsObjectsMalformedRejected(t *testing.T) {
	instance := newTestInstance(t, false)
	unknownField := ids.New()
	mismatchID := ids.New()

	objects := []ApplyObject{
		{ID: unknownField, Kind: KindKnownHost, Payload: json.RawMessage(`{"id":"` + unknownField + `","host":"h","port":22,"keyType":"ssh-ed25519","fingerprint":"f","addedAt":1,"bogus":true}`)},
		{ID: mismatchID, Kind: KindKnownHost, Payload: applyPayload(t, knownHostObject{ID: ids.New(), Host: "h", Port: 22, KeyType: "k", Fingerprint: "f", AddedAt: 1})},
		{ID: ids.New(), Kind: KindAIProfile, Payload: json.RawMessage(`{"id":"x","bogus":1}`)},
		{ID: ids.New(), Kind: KindTombstone, Payload: applyPayload(t, tombstoneObject{TargetKind: KindKnownHost, DeletedAt: 1})[:0]},
	}
	result := mustApplyObjects(t, instance, objects...)
	if result.Skipped != 4 {
		t.Fatalf("result=%+v", result)
	}
	for index, entry := range result.Objects {
		requireApplyResult(t, entry, ApplyResultSkipped)
		if entry.Warning == "" {
			t.Fatalf("malformed object %d must carry a warning", index)
		}
	}
	if rows, err := instance.db.KnownHostList(context.Background()); err != nil || len(rows) != 0 {
		t.Fatalf("malformed known host applied: %+v err=%v", rows, err)
	}
}
