package sync

import (
	"context"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func putDeviceKnownHost(t *testing.T, device *testDevice, id, host string, port int32, keyType, fingerprint string, addedAt int64) {
	t.Helper()
	if _, err := device.db.DB().ExecContext(context.Background(),
		`INSERT INTO known_host(id, host, port, key_type, fingerprint, added_at) VALUES(?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET fingerprint=excluded.fingerprint, added_at=excluded.added_at`,
		id, host, port, keyType, fingerprint, addedAt); err != nil {
		t.Fatal(err)
	}
}

func putDeviceAIProfile(t *testing.T, device *testDevice, record aiProfileRecord, updatedAt int64) {
	t.Helper()
	state, _, _, err := device.engine.aiProfilesLoad(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	state.upsert(record)
	state.ensureActive()
	if err := device.engine.aiProfilesSave(context.Background(), state, updatedAt, ""); err != nil {
		t.Fatal(err)
	}
}

func putDeviceSealedAIProfile(t *testing.T, device *testDevice, id, name, secret string, updatedAt int64) {
	t.Helper()
	envelope, err := device.vault.EncryptSecret(context.Background(), secret)
	if err != nil {
		t.Fatal(err)
	}
	putDeviceAIProfile(t, device, aiProfileRecord{
		ID: id, Name: name, BaseURL: "https://ai.example.com", APIKey: envelope, Model: "model-x", Stream: true,
	}, updatedAt)
}

func deviceAIProfileState(t *testing.T, device *testDevice) aiProfilesState {
	t.Helper()
	state, _, _, err := device.engine.aiProfilesLoad(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func deviceKnownHost(t *testing.T, device *testDevice, id string) (store.KnownHostRow, bool) {
	t.Helper()
	row, found, err := device.engine.knownHostByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return row, found
}

// serverUserID 取测试账号的用户 ID, 用于按用户写设备端 opt-in 键。
func serverUserID(t *testing.T, server *testSyncServer, username, password string) string {
	t.Helper()
	user, err := server.accounts.Authenticate(context.Background(), username, password)
	if err != nil {
		t.Fatal(err)
	}
	return user.ID
}

// enableDeviceKindOptIn 为设备上的指定用户开启 known_host/AI 档案同步(opt-in 是设备端按用户键存的)。
// M165 引擎门控后, 涉及这两类对象的同步测试都显式开启, 默认关由 TestEngineKindOptInDefaultOff 覆盖。
func enableDeviceKindOptIn(t *testing.T, device *testDevice, userID string) {
	t.Helper()
	if err := device.db.SetKnownHostSyncOptIn(context.Background(), userID, true); err != nil {
		t.Fatal(err)
	}
	if err := device.db.SetAIProfileSyncOptIn(context.Background(), userID, true); err != nil {
		t.Fatal(err)
	}
}

// deleteDeviceKnownHost/deleteDeviceAIProfile 模拟本地删除入口(后续 store/app 切片接入): 先立碑再删行。
// 用户主动删除是普通墓碑: 同时清除冲突三元组元数据, 再传播不得携带败者三元组。
func deleteDeviceKnownHost(t *testing.T, device *testDevice, id string, deletedAt int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := device.db.DB().ExecContext(ctx, "DELETE FROM setting WHERE key = ?", knownHostTombstoneMetaKey(id)); err != nil {
		t.Fatal(err)
	}
	if err := device.engine.syncTombstonePut(ctx, id, KindKnownHost, deletedAt); err != nil {
		t.Fatal(err)
	}
	if err := device.db.KnownHostRemove(ctx, id); err != nil {
		t.Fatal(err)
	}
}

func deleteDeviceAIProfile(t *testing.T, device *testDevice, id string, deletedAt int64) {
	t.Helper()
	ctx := context.Background()
	if err := device.engine.syncTombstonePut(ctx, id, KindAIProfile, deletedAt); err != nil {
		t.Fatal(err)
	}
	state := deviceAIProfileState(t, device)
	if state.remove(id) {
		state.ensureActive()
		if err := device.engine.aiProfilesSave(ctx, state, deletedAt, ""); err != nil {
			t.Fatal(err)
		}
	}
}

func requireDeviceQuiescent(t *testing.T, device *testDevice, server *testSyncServer, username, password string) {
	t.Helper()
	if report := syncDevice(t, device, server, username, password); report.Pulled+report.Applied+report.Pushed != 0 {
		t.Fatalf("idle sync not quiescent: %+v", report)
	}
}

// assertServerObjectKind 直接解开服务端密文, 验证对象槽位当前存放的种类(对象/墓碑)。
func assertServerObjectKind(t *testing.T, server *testSyncServer, username, password, objectID, wantKind string) {
	t.Helper()
	var blob []byte
	if err := server.db.DB().QueryRowContext(context.Background(),
		"SELECT blob FROM user_sync_object WHERE id = ?", objectID).Scan(&blob); err != nil {
		t.Fatalf("server object %s: %v", objectID, err)
	}
	kind, _, err := tryOpenObject(mustDEK(t, server, username, password), blob, objectID)
	if err != nil || kind != wantKind {
		t.Fatalf("server object %s kind=%q err=%v, want %q", objectID, kind, err, wantKind)
	}
}

// 评审回归(顺序一: 败者先推, 胜者后同步): 胜者同步时必须为败者立碑并推走,
// 服务端败者对象被墓碑替换; 再删除胜者后, 新设备不得复活败者的旧指纹。
func TestEngineKnownHostLoserFirstWinnerTombstones(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	loser := newTestDevice(t)
	winner := newTestDevice(t)
	userID := serverUserID(t, server, "alice", "alice-pw-123")
	enableDeviceKindOptIn(t, loser, userID)
	enableDeviceKindOptIn(t, winner, userID)

	loserID := ids.New()
	winnerID := ids.New()
	putDeviceKnownHost(t, loser, loserID, "git.example.com", 22, "ssh-ed25519", "SHA256:stale", 100)
	putDeviceKnownHost(t, winner, winnerID, "git.example.com", 22, "ssh-ed25519", "SHA256:fresh", 200)

	syncDevice(t, loser, server, "alice", "alice-pw-123")
	syncDevice(t, winner, server, "alice", "alice-pw-123")
	assertServerObjectKind(t, server, "alice", "alice-pw-123", loserID, KindTombstone)
	if row, found := deviceKnownHost(t, winner, winnerID); !found || row.Fingerprint != "SHA256:fresh" {
		t.Fatalf("winner must keep its row: %+v found=%v", row, found)
	}

	// 败者设备再同步: 墓碑清除其本地旧行。
	syncDevice(t, loser, server, "alice", "alice-pw-123")
	if _, found := deviceKnownHost(t, loser, loserID); found {
		t.Fatal("loser row must be tombstoned on the loser device")
	}
	assertDeviceTombstone(t, loser, loserID, KindKnownHost)

	// 删除胜者并传播后, 新设备不得得到败者(或胜者)的任何行。
	deleteDeviceKnownHost(t, winner, winnerID, ids.NowMS())
	syncDevice(t, winner, server, "alice", "alice-pw-123")
	syncDevice(t, loser, server, "alice", "alice-pw-123")

	newcomer := newTestDevice(t)
	enableDeviceKindOptIn(t, newcomer, userID)
	syncDevice(t, newcomer, server, "alice", "alice-pw-123")
	rows, err := newcomer.db.KnownHostList(context.Background())
	if err != nil || len(rows) != 0 {
		t.Fatalf("newcomer must not resurrect any triple row: %+v err=%v", rows, err)
	}
	requireDeviceQuiescent(t, loser, server, "alice", "alice-pw-123")
	requireDeviceQuiescent(t, winner, server, "alice", "alice-pw-123")
	requireDeviceQuiescent(t, newcomer, server, "alice", "alice-pw-123")
}

// 评审回归(R2): 同 ID 不同三元组的本地化身不得与同 ID 墓碑共存。
// 化身让出原 ID(re-ID)后: 信任内容经新 ID 在全设备保留, 服务端败者槽位被墓碑替换,
// 后续同原 ID 墓碑不误删 re-ID 记录, 二次收敛后全设备静默。
func TestEngineKnownHostIncarnationReIDConverges(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	rogue := newTestDevice(t)
	ctx := context.Background()
	userID := serverUserID(t, server, "alice", "alice-pw-123")
	for _, device := range []*testDevice{deviceA, deviceB, rogue} {
		enableDeviceKindOptIn(t, device, userID)
	}

	sharedID := ids.New()
	winnerID := ids.New()
	putDeviceKnownHost(t, deviceA, sharedID, "old.example.com", 22, "ssh-rsa", "SHA256:trusted", 50)
	putDeviceKnownHost(t, deviceA, winnerID, "git.example.com", 22, "ssh-ed25519", "SHA256:winner", 200)
	putDeviceKnownHost(t, deviceB, winnerID, "git.example.com", 22, "ssh-ed25519", "SHA256:winner", 200)
	putDeviceKnownHost(t, rogue, sharedID, "git.example.com", 22, "ssh-ed25519", "SHA256:rogue", 100)

	syncDevice(t, rogue, server, "alice", "alice-pw-123")   // 败者对象(sharedID, 新三元组, 100)上服务端
	syncDevice(t, deviceA, server, "alice", "alice-pw-123") // A: 化身 re-ID, 原 ID 立碑并推走

	assertServerObjectKind(t, server, "alice", "alice-pw-123", sharedID, KindTombstone)
	if _, found := deviceKnownHost(t, deviceA, sharedID); found {
		t.Fatal("original id must be vacated on A")
	}
	assertDeviceTombstone(t, deviceA, sharedID, KindKnownHost)

	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	syncDevice(t, rogue, server, "alice", "alice-pw-123")

	// 全设备(含后加入者)都持有化身信任内容(新 ID)与胜者行, 且都不存在原 ID 行。
	newcomer := newTestDevice(t)
	enableDeviceKindOptIn(t, newcomer, userID)
	syncDevice(t, newcomer, server, "alice", "alice-pw-123")
	devices := map[string]*testDevice{"A": deviceA, "B": deviceB, "rogue": rogue, "newcomer": newcomer}
	incarnationIDs := map[string]string{}
	for name, device := range devices {
		rows, err := device.db.KnownHostList(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var trusted, winner bool
		for _, row := range rows {
			if row.ID == sharedID {
				t.Fatalf("device%s still holds the original id: %+v", name, row)
			}
			if row.Host == "old.example.com" {
				trusted = row.Fingerprint == "SHA256:trusted" && row.AddedAt == 50 && row.KeyType == "ssh-rsa"
				incarnationIDs[name] = row.ID
			}
			if row.ID == winnerID {
				winner = row.Fingerprint == "SHA256:winner"
			}
		}
		if !trusted || !winner || len(rows) != 2 {
			t.Fatalf("device%s rows=%+v, want trusted incarnation + winner only", name, rows)
		}
	}
	if len(incarnationIDs) != 4 {
		t.Fatalf("every device must hold the incarnation: %v", incarnationIDs)
	}

	// 后续同原 ID 的墓碑(更晚修订)不误删 re-ID 后的化身。
	tombstonePayload, err := marshalObject(tombstoneObject{TargetKind: KindKnownHost, DeletedAt: ids.NowMS()})
	if err != nil {
		t.Fatal(err)
	}
	report := &SyncReport{}
	applied, _ := deviceA.engine.applyDecryptedObject(ctx, sharedID, KindTombstone, tombstonePayload, report)
	if !applied {
		t.Fatal("same-id tombstone must be absorbed")
	}
	if row, found := deviceKnownHost(t, deviceA, incarnationIDs["A"]); !found || row.Fingerprint != "SHA256:trusted" {
		t.Fatalf("re-IDed incarnation must survive the same-id tombstone: %+v found=%v", row, found)
	}

	// 较新墓碑随 A 的推送传播, 其余设备各拉取一次: 化身在全设备存活。
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	for name, device := range devices {
		if name == "A" {
			continue
		}
		syncDevice(t, device, server, "alice", "alice-pw-123")
		if row, found := deviceKnownHost(t, device, incarnationIDs[name]); !found || row.Fingerprint != "SHA256:trusted" {
			t.Fatalf("device%s re-IDed incarnation must survive the propagated tombstone: %+v found=%v", name, row, found)
		}
	}

	// 二次收敛: 全设备空闲同步静默。
	for name, device := range devices {
		if report := syncDevice(t, device, server, "alice", "alice-pw-123"); report.Pulled+report.Applied+report.Pushed != 0 {
			t.Fatalf("device%s not quiescent: %+v", name, report)
		}
	}
}

// 评审回归(R3): 其他设备仍持原 ID 同一信任副本时, re-ID 对象在同 addedAt 哈希决胜中
// 必须必胜——旧副本只能走 displaced 路径保留新 ID, 原 ID 墓碑只清旧槽位, 信任记录不丢。
func TestEngineKnownHostReIDBeatsOldCopyHashDuel(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceB := newTestDevice(t)
	deviceA := newTestDevice(t)
	ctx := context.Background()
	userID := serverUserID(t, server, "alice", "alice-pw-123")
	enableDeviceKindOptIn(t, deviceB, userID)
	enableDeviceKindOptIn(t, deviceA, userID)

	sharedID := ids.New()
	winnerID := ids.New()
	// B 与 A 都持有原 ID 的信任副本与三元组胜者行(更早的同步所致)。
	putDeviceKnownHost(t, deviceB, sharedID, "old.example.com", 22, "ssh-rsa", "SHA256:trusted", 50)
	putDeviceKnownHost(t, deviceB, winnerID, "git.example.com", 22, "ssh-ed25519", "SHA256:winner", 200)
	putDeviceKnownHost(t, deviceA, sharedID, "old.example.com", 22, "ssh-rsa", "SHA256:trusted", 50)
	putDeviceKnownHost(t, deviceA, winnerID, "git.example.com", 22, "ssh-ed25519", "SHA256:winner", 200)
	rogue := newTestDevice(t)
	enableDeviceKindOptIn(t, rogue, userID)
	putDeviceKnownHost(t, rogue, sharedID, "git.example.com", 22, "ssh-ed25519", "SHA256:rogue", 100)

	syncDevice(t, rogue, server, "alice", "alice-pw-123")   // 败者对象(sharedID 迁移三元组, 100)上服务端
	syncDevice(t, deviceB, server, "alice", "alice-pw-123") // B 先同步: re-ID + 原 ID 立碑并推走

	// 构造性不变式: B 的 re-ID 载荷在同修订号下必胜原 ID 副本(两种哈希顺序中败者被拒绝)。
	rows, err := deviceB.db.KnownHostList(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatalf("deviceB rows=%+v err=%v", rows, err)
	}
	var reID string
	for _, row := range rows {
		if row.Host == "old.example.com" {
			reID = row.ID
		}
	}
	if reID == "" || reID == sharedID {
		t.Fatalf("deviceB must re-ID the incarnation: %+v", rows)
	}
	oldPayload, err := marshalObject(knownHostObject{ID: sharedID, Host: "old.example.com", Port: 22, KeyType: "ssh-rsa", Fingerprint: "SHA256:trusted", AddedAt: 50})
	if err != nil {
		t.Fatal(err)
	}
	newPayload, err := marshalObject(knownHostObject{ID: reID, Host: "old.example.com", Port: 22, KeyType: "ssh-rsa", Fingerprint: "SHA256:trusted", AddedAt: 50})
	if err != nil {
		t.Fatal(err)
	}
	if !remoteWins(50, 50, newPayload, oldPayload) {
		t.Fatal("re-ID payload must provably win the equal-revision hash duel against the old copy")
	}

	// A 持原副本后同步: 对 re-ID 对象只能 displaced(采纳新 ID), 原 ID 墓碑被吸收, 信任内容与修订号保留。
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	if _, found := deviceKnownHost(t, deviceA, sharedID); found {
		t.Fatal("deviceA must displace the old copy")
	}
	if row, found := deviceKnownHost(t, deviceA, reID); !found || row.Fingerprint != "SHA256:trusted" || row.AddedAt != 50 {
		t.Fatalf("deviceA must adopt the re-IDed record intact: %+v found=%v", row, found)
	}
	assertDeviceTombstone(t, deviceA, sharedID, KindKnownHost)

	// 服务端槽位: 原 ID 是墓碑, 新 ID 是存活对象(不得被墓碑替换)。
	assertServerObjectKind(t, server, "alice", "alice-pw-123", sharedID, KindTombstone)
	assertServerObjectKind(t, server, "alice", "alice-pw-123", reID, KindKnownHost)

	// 新设备同样获得信任内容(新 ID, 原修订号)。
	newcomer := newTestDevice(t)
	enableDeviceKindOptIn(t, newcomer, userID)
	syncDevice(t, newcomer, server, "alice", "alice-pw-123")
	if row, found := deviceKnownHost(t, newcomer, reID); !found || row.Fingerprint != "SHA256:trusted" || row.AddedAt != 50 {
		t.Fatalf("newcomer must receive the re-IDed record: %+v found=%v", row, found)
	}
	if _, found := deviceKnownHost(t, newcomer, sharedID); found {
		t.Fatal("newcomer must not resurrect the original id")
	}

	// 二次同步静默。
	requireDeviceQuiescent(t, deviceB, server, "alice", "alice-pw-123")
	requireDeviceQuiescent(t, deviceA, server, "alice", "alice-pw-123")
	requireDeviceQuiescent(t, newcomer, server, "alice", "alice-pw-123")
}

// incarnationIDOf 返回设备上指定主机的行 ID(re-ID 后由调用方断言内容而非具体 ID)。
func incarnationIDOf(t *testing.T, device *testDevice, host string) string {
	t.Helper()
	rows, err := device.db.KnownHostList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Host == host {
			return row.ID
		}
	}
	t.Fatalf("no row for host %s: %+v", host, rows)
	return ""
}

// 评审回归(R4): 冲突墓碑与用户删除墓碑的区分。B 持旧副本(50), A 持同 ID 较新化身(60),
// 三元组胜者(200): A 吸收冲突墓碑时必须安全 re-ID 保留较新信任内容(而非按用户删除清掉),
// 原 ID 与败者 re-ID 槽位都收敛为墓碑, 最新内容在 A/B/rogue/新设备保留, 二次同步静默。
func TestEngineKnownHostConflictTombstonePreservesNewerIncarnation(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceB := newTestDevice(t)
	deviceA := newTestDevice(t)
	rogue := newTestDevice(t)
	userID := serverUserID(t, server, "alice", "alice-pw-123")
	for _, device := range []*testDevice{deviceB, deviceA, rogue} {
		enableDeviceKindOptIn(t, device, userID)
	}

	sharedID := ids.New()
	winnerID := ids.New()
	putDeviceKnownHost(t, deviceB, sharedID, "old.example.com", 22, "ssh-rsa", "SHA256:older", 50)
	putDeviceKnownHost(t, deviceB, winnerID, "git.example.com", 22, "ssh-ed25519", "SHA256:winner", 200)
	putDeviceKnownHost(t, deviceA, sharedID, "old.example.com", 22, "ssh-rsa", "SHA256:newer", 60)
	putDeviceKnownHost(t, deviceA, winnerID, "git.example.com", 22, "ssh-ed25519", "SHA256:winner", 200)
	putDeviceKnownHost(t, rogue, sharedID, "git.example.com", 22, "ssh-ed25519", "SHA256:rogue", 100)

	syncDevice(t, rogue, server, "alice", "alice-pw-123")   // 败者对象(100)上服务端
	syncDevice(t, deviceB, server, "alice", "alice-pw-123") // B: 旧副本 re-ID + 冲突墓碑(200) + 推送
	if _, found := deviceKnownHost(t, deviceB, sharedID); found {
		t.Fatal("B must vacate the original id")
	}
	olderReID := incarnationIDOf(t, deviceB, "old.example.com")
	if olderReID == sharedID {
		t.Fatal("B must re-ID under a fresh id")
	}

	syncDevice(t, deviceA, server, "alice", "alice-pw-123") // A: 较新化身 re-ID 保留, 旧 re-ID 槽位立碑

	if _, found := deviceKnownHost(t, deviceA, sharedID); found {
		t.Fatal("A must vacate the original id")
	}
	if _, found := deviceKnownHost(t, deviceA, olderReID); found {
		t.Fatal("A must tombstone the older re-ID slot")
	}
	newerReID := incarnationIDOf(t, deviceA, "old.example.com")
	if newerReID == sharedID || newerReID == olderReID {
		t.Fatalf("A must preserve the newer incarnation under its own fresh id: %s", newerReID)
	}
	if row, found := deviceKnownHost(t, deviceA, newerReID); !found || row.Fingerprint != "SHA256:newer" || row.AddedAt != 60 {
		t.Fatalf("A must preserve the newer trust content at revision 60: %+v found=%v", row, found)
	}
	assertDeviceTombstone(t, deviceA, sharedID, KindKnownHost)
	assertDeviceTombstone(t, deviceA, olderReID, KindKnownHost)

	// B 采纳较新化身并弃旧 re-ID; rogue 的败者行被同三元组冲突墓碑清除。
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	syncDevice(t, rogue, server, "alice", "alice-pw-123")
	if _, found := deviceKnownHost(t, rogue, sharedID); found {
		t.Fatal("rogue row must be deleted by the same-triple conflict tombstone")
	}
	if row, found := deviceKnownHost(t, deviceB, newerReID); !found || row.Fingerprint != "SHA256:newer" || row.AddedAt != 60 {
		t.Fatalf("B must adopt the newer incarnation: %+v found=%v", row, found)
	}
	if _, found := deviceKnownHost(t, deviceB, olderReID); found {
		t.Fatal("B must drop the older re-ID")
	}

	// 新设备获得最新信任内容(60); 服务端原 ID 与败者 re-ID 槽位都是墓碑, 新 ID 是存活对象。
	newcomer := newTestDevice(t)
	enableDeviceKindOptIn(t, newcomer, userID)
	syncDevice(t, newcomer, server, "alice", "alice-pw-123")
	if row, found := deviceKnownHost(t, newcomer, newerReID); !found || row.Fingerprint != "SHA256:newer" || row.AddedAt != 60 {
		t.Fatalf("newcomer must receive the newer incarnation: %+v found=%v", row, found)
	}
	if _, found := deviceKnownHost(t, newcomer, sharedID); found {
		t.Fatal("newcomer must not resurrect the original id")
	}
	if _, found := deviceKnownHost(t, newcomer, olderReID); found {
		t.Fatal("newcomer must not resurrect the older re-ID")
	}
	assertServerObjectKind(t, server, "alice", "alice-pw-123", sharedID, KindTombstone)
	assertServerObjectKind(t, server, "alice", "alice-pw-123", olderReID, KindTombstone)
	assertServerObjectKind(t, server, "alice", "alice-pw-123", newerReID, KindKnownHost)

	// 二次同步静默: 无持续重推, 无墓碑抖动。
	requireDeviceQuiescent(t, deviceA, server, "alice", "alice-pw-123")
	requireDeviceQuiescent(t, deviceB, server, "alice", "alice-pw-123")
	requireDeviceQuiescent(t, rogue, server, "alice", "alice-pw-123")
	requireDeviceQuiescent(t, newcomer, server, "alice", "alice-pw-123")
}

// 评审回归(顺序二: 胜者先推, 败者后同步): 败者经 displaced 路径立碑自身并 adopts 胜者;
// 删除胜者后二次收敛, 新设备同样不得复活败者。
func TestEngineKnownHostWinnerFirstLoserDisplaced(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	winner := newTestDevice(t)
	loser := newTestDevice(t)
	userID := serverUserID(t, server, "alice", "alice-pw-123")
	enableDeviceKindOptIn(t, winner, userID)
	enableDeviceKindOptIn(t, loser, userID)

	winnerID := ids.New()
	loserID := ids.New()
	putDeviceKnownHost(t, winner, winnerID, "git.example.com", 22, "ssh-ed25519", "SHA256:fresh", 200)
	putDeviceKnownHost(t, loser, loserID, "git.example.com", 22, "ssh-ed25519", "SHA256:stale", 100)

	syncDevice(t, winner, server, "alice", "alice-pw-123")
	syncDevice(t, loser, server, "alice", "alice-pw-123")
	assertServerObjectKind(t, server, "alice", "alice-pw-123", loserID, KindTombstone)
	if _, found := deviceKnownHost(t, loser, loserID); found {
		t.Fatal("loser row must be displaced locally")
	}
	if row, found := deviceKnownHost(t, loser, winnerID); !found || row.Fingerprint != "SHA256:fresh" {
		t.Fatalf("loser must adopt the winner row: %+v found=%v", row, found)
	}

	deleteDeviceKnownHost(t, winner, winnerID, ids.NowMS())
	syncDevice(t, winner, server, "alice", "alice-pw-123")
	syncDevice(t, loser, server, "alice", "alice-pw-123")
	if _, found := deviceKnownHost(t, loser, winnerID); found {
		t.Fatal("winner row must be tombstoned on the loser device")
	}

	newcomer := newTestDevice(t)
	enableDeviceKindOptIn(t, newcomer, userID)
	syncDevice(t, newcomer, server, "alice", "alice-pw-123")
	rows, err := newcomer.db.KnownHostList(context.Background())
	if err != nil || len(rows) != 0 {
		t.Fatalf("newcomer must not resurrect any triple row: %+v err=%v", rows, err)
	}
	requireDeviceQuiescent(t, winner, server, "alice", "alice-pw-123")
	requireDeviceQuiescent(t, loser, server, "alice", "alice-pw-123")
	requireDeviceQuiescent(t, newcomer, server, "alice", "alice-pw-123")
}

func TestEngineSyncKnownHostsAndAIProfilesConverge(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	ctx := context.Background()
	userID := serverUserID(t, server, "alice", "alice-pw-123")
	enableDeviceKindOptIn(t, deviceA, userID)
	enableDeviceKindOptIn(t, deviceB, userID)

	hostID := ids.New()
	putDeviceKnownHost(t, deviceA, hostID, "git.example.com", 22, "ssh-ed25519", "SHA256:sync-fp", 100)
	keyedID := ids.New()
	putDeviceSealedAIProfile(t, deviceA, keyedID, "同步档案", "sync-key-值", 100)
	keylessID := ids.New()
	putDeviceAIProfile(t, deviceA, aiProfileRecord{ID: keylessID, Name: "无密钥档案", Model: "model-y", Stream: true}, 100)

	report := syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	if report.Pushed != 3 {
		t.Fatalf("deviceA pushed=%d want 3: %+v", report.Pushed, report)
	}
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")

	row, found := deviceKnownHost(t, deviceB, hostID)
	if !found || row.Host != "git.example.com" || row.Port != 22 || row.KeyType != "ssh-ed25519" ||
		row.Fingerprint != "SHA256:sync-fp" || row.AddedAt != 100 {
		t.Fatalf("deviceB known host=%+v found=%v", row, found)
	}
	state := deviceAIProfileState(t, deviceB)
	keyed, exists := state.find(keyedID)
	if !exists {
		t.Fatal("deviceB missing keyed profile")
	}
	if !strings.HasPrefix(keyed.APIKey, store.SecretEnvelopePrefix) {
		t.Fatalf("deviceB profile key must be re-sealed with local vault: %q", keyed.APIKey)
	}
	key, err := deviceB.vault.DecryptSecret(ctx, keyed.APIKey)
	if err != nil || key != "sync-key-值" {
		t.Fatalf("deviceB key=%q err=%v", key, err)
	}
	if keyed.Name != "同步档案" || keyed.BaseURL != "https://ai.example.com" || keyed.Model != "model-x" || !keyed.Stream {
		t.Fatalf("deviceB keyed profile fields=%+v", keyed)
	}
	keyless, exists := state.find(keylessID)
	if !exists || keyless.APIKey != "" {
		t.Fatalf("deviceB keyless profile=%+v exists=%v", keyless, exists)
	}
	if state.ActiveID == nil {
		t.Fatal("deviceB profiles must keep an active profile")
	}

	// B 端新增回流到 A。
	hostB := ids.New()
	putDeviceKnownHost(t, deviceB, hostB, "db.example.com", 2222, "ssh-rsa", "SHA256:from-b", 200)
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	if row, found := deviceKnownHost(t, deviceA, hostB); !found || row.Fingerprint != "SHA256:from-b" {
		t.Fatalf("deviceA missing known host from B: %+v found=%v", row, found)
	}

	requireDeviceQuiescent(t, deviceA, server, "alice", "alice-pw-123")
	requireDeviceQuiescent(t, deviceB, server, "alice", "alice-pw-123")
}

func TestEngineSettingsDeletionConverges(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	userID := serverUserID(t, server, "alice", "alice-pw-123")
	enableDeviceKindOptIn(t, deviceA, userID)
	enableDeviceKindOptIn(t, deviceB, userID)

	hostID := ids.New()
	putDeviceKnownHost(t, deviceA, hostID, "git.example.com", 22, "ssh-ed25519", "SHA256:gone", 100)
	profileID := ids.New()
	putDeviceSealedAIProfile(t, deviceA, profileID, "待删档案", "gone-key", 100)
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if _, found := deviceKnownHost(t, deviceB, hostID); !found {
		t.Fatal("deviceB must hold the known host before deletion")
	}

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
	assertDeviceTombstone(t, deviceB, hostID, KindKnownHost)
	assertDeviceTombstone(t, deviceB, profileID, KindAIProfile)

	// 空闲同步不得复活已删对象。
	requireDeviceQuiescent(t, deviceA, server, "alice", "alice-pw-123")
	requireDeviceQuiescent(t, deviceB, server, "alice", "alice-pw-123")
	if _, found := deviceKnownHost(t, deviceB, hostID); found {
		t.Fatal("known host resurrected on B")
	}
	if state := deviceAIProfileState(t, deviceB); len(state.Profiles) != 0 {
		t.Fatal("profile resurrected on B")
	}
}

func TestEngineKnownHostLWWConflictConverges(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	userID := serverUserID(t, server, "alice", "alice-pw-123")
	enableDeviceKindOptIn(t, deviceA, userID)
	enableDeviceKindOptIn(t, deviceB, userID)

	hostID := ids.New()
	putDeviceKnownHost(t, deviceA, hostID, "git.example.com", 22, "ssh-ed25519", "SHA256:base", 100)
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")

	// 双方离线编辑同一行, B 的修订号更高。
	putDeviceKnownHost(t, deviceA, hostID, "git.example.com", 22, "ssh-ed25519", "SHA256:a-edit", 200)
	putDeviceKnownHost(t, deviceB, hostID, "git.example.com", 22, "ssh-ed25519", "SHA256:b-edit", 300)
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")

	for name, device := range map[string]*testDevice{"A": deviceA, "B": deviceB} {
		row, found := deviceKnownHost(t, device, hostID)
		if !found || row.Fingerprint != "SHA256:b-edit" || row.AddedAt != 300 {
			t.Fatalf("device%s known host=%+v found=%v, want B 的编辑", name, row, found)
		}
	}
	requireDeviceQuiescent(t, deviceA, server, "alice", "alice-pw-123")
	requireDeviceQuiescent(t, deviceB, server, "alice", "alice-pw-123")
}

// 同修订不同内容以规范载荷哈希决胜: 双设备必须收敛到同一档案, 与推送顺序无关。
func TestEngineAIProfileEqualRevisionConverges(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	userID := serverUserID(t, server, "alice", "alice-pw-123")
	enableDeviceKindOptIn(t, deviceA, userID)
	enableDeviceKindOptIn(t, deviceB, userID)

	profileID := ids.New()
	putDeviceAIProfile(t, deviceA, aiProfileRecord{ID: profileID, Name: "名字X", Model: "m", Stream: true}, 100)
	putDeviceAIProfile(t, deviceB, aiProfileRecord{ID: profileID, Name: "名字Y", Model: "m", Stream: true}, 100)
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")

	nameA := deviceAIProfileState(t, deviceA).Profiles[0].Name
	nameB := deviceAIProfileState(t, deviceB).Profiles[0].Name
	if nameA != nameB {
		t.Fatalf("equal-revision LWW diverged: A=%q B=%q", nameA, nameB)
	}
	requireDeviceQuiescent(t, deviceA, server, "alice", "alice-pw-123")
	requireDeviceQuiescent(t, deviceB, server, "alice", "alice-pw-123")
}

func TestEngineAIProfileKeyedDeferredWhileVaultLocked(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	userID := serverUserID(t, server, "alice", "alice-pw-123")
	enableDeviceKindOptIn(t, deviceA, userID)
	enableDeviceKindOptIn(t, deviceB, userID)

	hostID := ids.New()
	putDeviceKnownHost(t, deviceA, hostID, "git.example.com", 22, "ssh-ed25519", "SHA256:locked", 100)
	keyedID := ids.New()
	putDeviceSealedAIProfile(t, deviceA, keyedID, "锁定档案", "locked-key", 100)
	keylessID := ids.New()
	putDeviceAIProfile(t, deviceA, aiProfileRecord{ID: keylessID, Name: "无密钥档案", Model: "m", Stream: true}, 100)

	deviceA.vault.Lock()
	report := syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	if report.Pushed != 2 {
		t.Fatalf("locked vault must defer only the keyed profile: %+v", report)
	}
	if len(report.Warnings) == 0 {
		t.Fatal("deferred profile must surface a warning")
	}
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if _, found := deviceKnownHost(t, deviceB, hostID); !found {
		t.Fatal("known host must sync while vault locked")
	}
	state := deviceAIProfileState(t, deviceB)
	if _, exists := state.find(keyedID); exists {
		t.Fatal("keyed profile must not sync while vault locked")
	}
	if _, exists := state.find(keylessID); !exists {
		t.Fatal("keyless profile must sync while vault locked")
	}

	if err := deviceA.vault.UnlockMaster(context.Background(), "device-master-pw"); err != nil {
		t.Fatal(err)
	}
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	keyed, exists := deviceAIProfileState(t, deviceB).find(keyedID)
	if !exists {
		t.Fatal("keyed profile must sync after unlock")
	}
	key, err := deviceB.vault.DecryptSecret(context.Background(), keyed.APIKey)
	if err != nil || key != "locked-key" {
		t.Fatalf("deviceB key=%q err=%v", key, err)
	}
	requireDeviceQuiescent(t, deviceA, server, "alice", "alice-pw-123")
	requireDeviceQuiescent(t, deviceB, server, "alice", "alice-pw-123")
}

// M165 R2: opt-in 默认关: 引擎不收集/不推送/不应用 known_host 与 AI 档案(含远端已有对象)。
func TestEngineKindOptInDefaultOff(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)

	hostID := ids.New()
	putDeviceKnownHost(t, deviceA, hostID, "git.example.com", 22, "ssh-ed25519", "SHA256:off", 100)
	profileID := ids.New()
	putDeviceAIProfile(t, deviceA, aiProfileRecord{ID: profileID, Name: "默认关档案", Model: "m", Stream: true}, 100)

	if report := syncDevice(t, deviceA, server, "alice", "alice-pw-123"); report.Pushed != 0 {
		t.Fatalf("opt-in 默认关不得推送两类对象: %+v", report)
	}
	if report := syncDevice(t, deviceB, server, "alice", "alice-pw-123"); report.Applied != 0 {
		t.Fatalf("opt-in 默认关不得应用远端对象: %+v", report)
	}
	if _, found := deviceKnownHost(t, deviceB, hostID); found {
		t.Fatal("opt-in 默认关不得应用 known_host")
	}
	if state := deviceAIProfileState(t, deviceB); len(state.Profiles) != 0 {
		t.Fatalf("opt-in 默认关不得应用 ai_profile: %+v", state.Profiles)
	}
}

// M165 R2: 开启→同步; 关闭→停止收集/应用/墓碑传播(远端副本保留); 再开启→补拉合并(关闭期间未记对账)。
func TestEngineKindOptInOnOffOn(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	userID := serverUserID(t, server, "alice", "alice-pw-123")
	ctx := context.Background()

	hostID := ids.New()
	putDeviceKnownHost(t, deviceA, hostID, "git.example.com", 22, "ssh-ed25519", "SHA256:onoff", 100)

	enableDeviceKindOptIn(t, deviceA, userID)
	enableDeviceKindOptIn(t, deviceB, userID)
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if _, found := deviceKnownHost(t, deviceB, hostID); !found {
		t.Fatal("opt-in 开启后 known_host 必须同步")
	}

	// A 关闭: 本地新增不推送; B 的远端变更不应用到 A。
	if err := deviceA.db.SetKnownHostSyncOptIn(ctx, userID, false); err != nil {
		t.Fatal(err)
	}
	localOnly := ids.New()
	putDeviceKnownHost(t, deviceA, localOnly, "off.example.com", 22, "ssh-rsa", "SHA256:local-only", 200)
	fromB := ids.New()
	putDeviceKnownHost(t, deviceB, fromB, "from-b.example.com", 2222, "ssh-rsa", "SHA256:from-b", 200)
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if _, found := deviceKnownHost(t, deviceB, localOnly); found {
		t.Fatal("opt-in 关闭后本地新增不得推送")
	}
	if _, found := deviceKnownHost(t, deviceA, fromB); found {
		t.Fatal("opt-in 关闭后远端对象不得应用")
	}

	// A 删除本地行(关闭期间, opt-in 关): 不立碑不传播, 远端副本保留。
	if err := deviceA.db.KnownHostRemove(ipc.WithUserID(ctx, userID), hostID); err != nil {
		t.Fatal(err)
	}
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	if _, found := deviceKnownHost(t, deviceB, hostID); !found {
		t.Fatal("opt-in 关闭期间删除不得传播, 远端副本必须保留")
	}

	// 再开启: 关闭期间未记对账的远端对象(fromB)补拉合并; 本地删除未立碑的 hostID 不复活(远端副本保留在 B 与服务端)。
	enableDeviceKindOptIn(t, deviceA, userID)
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	if _, found := deviceKnownHost(t, deviceA, fromB); !found {
		t.Fatal("重新开启后必须补拉关闭期间的远端对象")
	}
	if _, found := deviceKnownHost(t, deviceA, hostID); found {
		t.Fatal("关闭期间的本地删除不复活(未立碑, 删除仅限本地)")
	}
	if _, found := deviceKnownHost(t, deviceB, hostID); !found {
		t.Fatal("远端副本必须保留在 B")
	}
}

// M165 R2: opt-in 关闭的设备不应用远端墓碑(本地行保留); 重新开启后按墓碑清除。
func TestEngineKindOptInRemoteTombstoneGated(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	userID := serverUserID(t, server, "alice", "alice-pw-123")
	ctx := context.Background()

	hostID := ids.New()
	putDeviceKnownHost(t, deviceA, hostID, "git.example.com", 22, "ssh-ed25519", "SHA256:gone", 100)
	putDeviceKnownHost(t, deviceB, hostID, "git.example.com", 22, "ssh-ed25519", "SHA256:gone", 100)
	enableDeviceKindOptIn(t, deviceA, userID)
	enableDeviceKindOptIn(t, deviceB, userID)
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")

	// B 关闭; A 删除并推送墓碑。
	if err := deviceB.db.SetKnownHostSyncOptIn(ctx, userID, false); err != nil {
		t.Fatal(err)
	}
	deleteDeviceKnownHost(t, deviceA, hostID, ids.NowMS())
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	assertServerObjectKind(t, server, "alice", "alice-pw-123", hostID, KindTombstone)

	// B 同步: 墓碑不应用, 本地行保留。
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if _, found := deviceKnownHost(t, deviceB, hostID); !found {
		t.Fatal("opt-in 关闭时远端墓碑不得删除本地行")
	}

	// B 重新开启: 墓碑(仍未对账)在下一轮应用, 本地行清除。
	enableDeviceKindOptIn(t, deviceB, userID)
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if _, found := deviceKnownHost(t, deviceB, hostID); found {
		t.Fatal("重新开启后必须按墓碑清除本地行")
	}
}
