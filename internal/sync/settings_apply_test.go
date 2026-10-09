package sync

import (
	"context"
	"encoding/json"
	"strconv"
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

func applyKnownHostConflictTombstone(t *testing.T, id, host string, port int32, keyType string, deletedAt int64) ApplyObject {
	t.Helper()
	return ApplyObject{ID: id, Kind: KindTombstone, Payload: applyPayload(t, tombstoneObject{
		TargetKind: KindKnownHost, DeletedAt: deletedAt, Host: host, Port: port, KeyType: keyType,
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
		Model: "model-x", FallbackModel: "model-y", Temperature: testPtr(0.7), ReasoningEffort: "high", ContextWindow: 128000,
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
	// 败者(远端)必须按胜者修订号立碑, 随本轮推送覆盖服务端败者对象。
	requireSyncTombstone(t, instance, remoteID, KindKnownHost, 300)
}

// re-ID 的新 ID 必须在同修订号哈希决胜中必胜旧载荷: 两种哈希顺序里, 败者顺序被构造性拒绝,
// 旧副本只能走 displaced 路径, 不会反向把 re-ID 对象墓碑化。
func TestKnownHostReincarnationIDWinsHashTiebreak(t *testing.T) {
	instance := newTestInstance(t, false)
	engine := instance.service.engine
	row := &store.KnownHostRow{ID: ids.New(), Host: "old.example.com", Port: 22, KeyType: "ssh-rsa", Fingerprint: "SHA256:trusted", AddedAt: 50}
	oldPayload, err := marshalObject(knownHostObject{
		ID: row.ID, Host: row.Host, Port: row.Port, KeyType: row.KeyType, Fingerprint: row.Fingerprint, AddedAt: row.AddedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	for iteration := 0; iteration < 64; iteration++ {
		newID, err := engine.knownHostReincarnationID(row)
		if err != nil {
			t.Fatal(err)
		}
		newPayload, err := marshalObject(knownHostObject{
			ID: newID, Host: row.Host, Port: row.Port, KeyType: row.KeyType, Fingerprint: row.Fingerprint, AddedAt: row.AddedAt,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !remoteWins(row.AddedAt, row.AddedAt, newPayload, oldPayload) {
			t.Fatalf("iteration %d: reincarnation %s loses the equal-revision hash tiebreak", iteration, newID)
		}
	}
}

// 与载荷同 ID 但不同三元组的本地行是「化身」: 它与同 ID 败者墓碑不能共存(槽位二义,
// 服务端不收敛, 后续同 ID 墓碑会误删它)。化身必须在同一事务内让出原 ID——信任内容以
// 新 ID 原样保留, 原 ID 立碑给远端败者; 不同三元组的他行不受影响。
func TestApplyKnownHostTripleConflictIncarnationReIDed(t *testing.T) {
	instance := newTestInstance(t, false)
	sharedID := ids.New()
	winnerID := ids.New()
	mustApplyObjects(t, instance, applyKnownHost(t, sharedID, "old.example.com", 22, "ssh-rsa", "SHA256:unrelated", 50))
	mustApplyObjects(t, instance, applyKnownHost(t, winnerID, "git.example.com", 22, "ssh-ed25519", "SHA256:winner", 200))

	// 载荷携带 sharedID 与较新修订号(50 → 100)迁移三元组, 但目标三元组的胜者(200)更胜一筹:
	// 迁移被拒, sharedID 立碑, 化身以新 ID 保留(内容与修订号不变)。
	result := mustApplyObjects(t, instance, applyKnownHost(t, sharedID, "git.example.com", 22, "ssh-ed25519", "SHA256:loser", 100))
	requireApplyResult(t, result.Objects[0], ApplyResultSkipped)
	requireSyncTombstone(t, instance, sharedID, KindKnownHost, 200)
	requireNoKnownHost(t, instance, sharedID)
	requireKnownHost(t, instance, winnerID, "SHA256:winner", 200)

	rows, err := instance.db.KnownHostList(context.Background())
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	var incarnation *store.KnownHostRow
	for index := range rows {
		if rows[index].Host == "old.example.com" {
			incarnation = &rows[index]
		}
	}
	if incarnation == nil || incarnation.ID == sharedID || incarnation.Fingerprint != "SHA256:unrelated" ||
		incarnation.AddedAt != 50 || incarnation.KeyType != "ssh-rsa" || incarnation.Port != 22 {
		t.Fatalf("incarnation must survive under a fresh id with content intact: %+v", rows)
	}

	// 收集映射不得再有同 ID 槽位冲突: 化身对象与原 ID 墓碑各自成对象。
	objects, err := instance.service.engine.collectLocalObjects(context.Background(), &SyncReport{}, kindOptIn{knownHost: true, aiProfile: true})
	if err != nil {
		t.Fatal(err)
	}
	if entry, exists := objects[sharedID]; !exists || entry.kind != KindTombstone {
		t.Fatalf("original id slot must carry the tombstone: %+v", objects[sharedID])
	}
	if entry, exists := objects[incarnation.ID]; !exists || entry.kind != KindKnownHost {
		t.Fatalf("incarnation must be collected as a known_host object: %+v", entry)
	}

	// 后续同原 ID 的墓碑(更晚修订)不会误删 re-ID 后的化身。
	requireApplyResult(t, mustApplyObjects(t, instance, applyKnownHostTombstone(t, sharedID, 300)).Objects[0], ApplyResultApplied)
	rows, err = instance.db.KnownHostList(context.Background())
	if err != nil || len(rows) != 2 {
		t.Fatalf("tombstone replay must not delete the re-IDed record: %+v err=%v", rows, err)
	}
	stillThere := false
	for _, row := range rows {
		if row.ID == incarnation.ID && row.Fingerprint == "SHA256:unrelated" {
			stillThere = true
		}
	}
	if !stillThere {
		t.Fatalf("incarnation lost after same-id tombstone replay: %+v", rows)
	}
}

// 冲突墓碑(携带原败者三元组)遇到同 ID 不同三元组的较新胜出化身: 不得按用户删除清掉,
// 必须安全 re-ID 保留内容与修订号, 冲突墓碑只清原槽位并随收集再传播(保留三元组)。
func TestApplyKnownHostConflictTombstoneReIDsNewerIncarnation(t *testing.T) {
	instance := newTestInstance(t, false)
	sharedID := ids.New()
	mustApplyObjects(t, instance, applyKnownHost(t, sharedID, "old.example.com", 22, "ssh-rsa", "SHA256:newer", 60))

	result := mustApplyObjects(t, instance, applyKnownHostConflictTombstone(t, sharedID, "git.example.com", 22, "ssh-ed25519", 200))
	requireApplyResult(t, result.Objects[0], ApplyResultApplied)
	requireNoKnownHost(t, instance, sharedID)
	requireSyncTombstone(t, instance, sharedID, KindKnownHost, 200)

	rows, err := instance.db.KnownHostList(context.Background())
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	incarnation := rows[0]
	if incarnation.ID == sharedID || incarnation.Host != "old.example.com" || incarnation.Fingerprint != "SHA256:newer" || incarnation.AddedAt != 60 {
		t.Fatalf("newer incarnation must survive under a fresh id: %+v", incarnation)
	}

	// 收集: 原 ID 槽位是携带三元组的冲突墓碑, 新 ID 是存活对象。
	objects, err := instance.service.engine.collectLocalObjects(context.Background(), &SyncReport{}, kindOptIn{knownHost: true, aiProfile: true})
	if err != nil {
		t.Fatal(err)
	}
	entry := objects[sharedID]
	if entry.kind != KindTombstone || !strings.Contains(string(entry.plaintext), `"host":"git.example.com"`) {
		t.Fatalf("conflict tombstone must carry the loser triple: %+v", entry)
	}
	if entry := objects[incarnation.ID]; entry.kind != KindKnownHost {
		t.Fatalf("incarnation must be collected as a known_host object: %+v", entry)
	}

	// 同三元组冲突墓碑重放(其他设备的败者清理): 本地已无原 ID 行, 只合并墓碑, 化身不受影响。
	replay := mustApplyObjects(t, instance, applyKnownHostConflictTombstone(t, sharedID, "git.example.com", 22, "ssh-ed25519", 200))
	requireApplyResult(t, replay.Objects[0], ApplyResultApplied)
	if row, found, _ := instance.service.engine.knownHostByID(context.Background(), incarnation.ID); !found || row.Fingerprint != "SHA256:newer" {
		t.Fatalf("replay must not touch the re-IDed incarnation: %+v found=%v", row, found)
	}
}

// 用户主动删除墓碑(无三元组)按 ID 生效, 不论本地行三元组; 再传播不得携带三元组。
func TestApplyKnownHostUserTombstoneDeletesRegardlessOfTriple(t *testing.T) {
	instance := newTestInstance(t, false)
	rowID := ids.New()
	mustApplyObjects(t, instance, applyKnownHost(t, rowID, "old.example.com", 22, "ssh-rsa", "SHA256:fp", 60))

	requireApplyResult(t, mustApplyObjects(t, instance, applyKnownHostTombstone(t, rowID, 100)).Objects[0], ApplyResultApplied)
	requireNoKnownHost(t, instance, rowID)
	requireSyncTombstone(t, instance, rowID, KindKnownHost, 100)

	objects, err := instance.service.engine.collectLocalObjects(context.Background(), &SyncReport{}, kindOptIn{knownHost: true, aiProfile: true})
	if err != nil {
		t.Fatal(err)
	}
	entry := objects[rowID]
	if entry.kind != KindTombstone || strings.Contains(string(entry.plaintext), "old.example.com") {
		t.Fatalf("user tombstone must stay plain: %+v", entry)
	}
}

// 本地行修订号较新: 冲突墓碑不得清除也不得 re-ID, 由本地行按标准反复活语义重推槽位。
func TestApplyKnownHostConflictTombstoneNewerRowSkips(t *testing.T) {
	instance := newTestInstance(t, false)
	rowID := ids.New()
	mustApplyObjects(t, instance, applyKnownHost(t, rowID, "old.example.com", 22, "ssh-rsa", "SHA256:newest", 300))

	result := mustApplyObjects(t, instance, applyKnownHostConflictTombstone(t, rowID, "git.example.com", 22, "ssh-ed25519", 200))
	requireApplyResult(t, result.Objects[0], ApplyResultSkipped)
	requireKnownHost(t, instance, rowID, "SHA256:newest", 300)
	requireNoSyncTombstone(t, instance, rowID)
}

// 同一 ID 的多个冲突墓碑: 三元组元数据确定性地跟随修订号最大者, 不拼出混合墓碑;
// 更高修订的用户删除墓碑随后清除元数据, 再传播退化为纯用户删除。
func TestApplyKnownHostConflictTombstoneMetaFollowsMaxRevision(t *testing.T) {
	instance := newTestInstance(t, false)
	ctx := context.Background()
	hostID := ids.New()

	requireApplyResult(t, mustApplyObjects(t, instance, applyKnownHostConflictTombstone(t, hostID, "a.example.com", 22, "ssh-rsa", 50)).Objects[0], ApplyResultApplied)
	requireApplyResult(t, mustApplyObjects(t, instance, applyKnownHostConflictTombstone(t, hostID, "b.example.com", 2222, "ssh-ed25519", 200)).Objects[0], ApplyResultApplied)
	assertTombstoneTriple := func(wantHost string, wantDeletedAt int64) {
		t.Helper()
		objects, err := instance.service.engine.collectLocalObjects(ctx, &SyncReport{}, kindOptIn{knownHost: true, aiProfile: true})
		if err != nil {
			t.Fatal(err)
		}
		entry := objects[hostID]
		if entry.kind != KindTombstone {
			t.Fatalf("slot must carry a tombstone: %+v", entry)
		}
		if wantHost == "" {
			if strings.Contains(string(entry.plaintext), "example.com") {
				t.Fatalf("user tombstone must not carry a triple: %s", entry.plaintext)
			}
			return
		}
		if !strings.Contains(string(entry.plaintext), `"host":"`+wantHost+`"`) || !strings.Contains(string(entry.plaintext), `"deletedAt":`+strconv.FormatInt(wantDeletedAt, 10)) {
			t.Fatalf("tombstone must carry triple %s at revision %d: %s", wantHost, wantDeletedAt, entry.plaintext)
		}
	}
	assertTombstoneTriple("b.example.com", 200)

	// 反向落地顺序: 低修订后落不得覆盖高修订元数据。
	otherID := ids.New()
	requireApplyResult(t, mustApplyObjects(t, instance, applyKnownHostConflictTombstone(t, otherID, "b.example.com", 2222, "ssh-ed25519", 200)).Objects[0], ApplyResultApplied)
	requireApplyResult(t, mustApplyObjects(t, instance, applyKnownHostConflictTombstone(t, otherID, "a.example.com", 22, "ssh-rsa", 50)).Objects[0], ApplyResultApplied)
	objects, err := instance.service.engine.collectLocalObjects(ctx, &SyncReport{}, kindOptIn{knownHost: true, aiProfile: true})
	if err != nil {
		t.Fatal(err)
	}
	if entry := objects[otherID]; !strings.Contains(string(entry.plaintext), `"host":"b.example.com"`) {
		t.Fatalf("lower revision must not overwrite the meta: %s", entry.plaintext)
	}

	// 更高修订的用户删除: 元数据被清除, 再传播为纯用户删除。
	requireApplyResult(t, mustApplyObjects(t, instance, applyKnownHostTombstone(t, hostID, 300)).Objects[0], ApplyResultApplied)
	assertTombstoneTriple("", 0)
}

func TestApplyKnownHostTripleConflictRemoteLoserTombstoned(t *testing.T) {
	instance := newTestInstance(t, false)
	winnerID := ids.New()
	loserID := ids.New()
	mustApplyObjects(t, instance, applyKnownHost(t, winnerID, "git.example.com", 22, "ssh-ed25519", "SHA256:winner", 200))

	// 败者先推后到: 本地胜者保住行, 败者 id 按胜者修订号立碑, 本轮 collect/push 即可覆盖服务端败者对象。
	result := mustApplyObjects(t, instance, applyKnownHost(t, loserID, "git.example.com", 22, "ssh-ed25519", "SHA256:loser", 100))
	requireApplyResult(t, result.Objects[0], ApplyResultSkipped)
	requireKnownHost(t, instance, winnerID, "SHA256:winner", 200)
	requireNoKnownHost(t, instance, loserID)
	requireSyncTombstone(t, instance, loserID, KindKnownHost, 200)

	// 墓碑阻断败者重放; 较新败者仍可胜出并反过来立碑胜者。
	stale := mustApplyObjects(t, instance, applyKnownHost(t, loserID, "git.example.com", 22, "ssh-ed25519", "SHA256:loser", 100))
	requireApplyResult(t, stale.Objects[0], ApplyResultSkipped)
	requireNoKnownHost(t, instance, loserID)

	newer := mustApplyObjects(t, instance, applyKnownHost(t, loserID, "git.example.com", 22, "ssh-ed25519", "SHA256:loser-new", 300))
	requireApplyResult(t, newer.Objects[0], ApplyResultApplied)
	requireNoKnownHost(t, instance, winnerID)
	requireKnownHost(t, instance, loserID, "SHA256:loser-new", 300)
	requireSyncTombstone(t, instance, winnerID, KindKnownHost, 300)
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
		record.FallbackModel != payload.FallbackModel || !float64PointersEqual(record.Temperature, payload.Temperature) ||
		record.ReasoningEffort != payload.ReasoningEffort ||
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
	objects, err := instance.service.engine.collectLocalObjects(context.Background(), &SyncReport{}, kindOptIn{knownHost: true, aiProfile: true})
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
		profile.FallbackModel != payload.FallbackModel || !float64PointersEqual(profile.Temperature, payload.Temperature) ||
		string(profile.ReasoningEffort) != payload.ReasoningEffort ||
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
