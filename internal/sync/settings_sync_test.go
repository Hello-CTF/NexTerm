package sync

import (
	"context"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
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

// deleteDeviceKnownHost/deleteDeviceAIProfile 模拟本地删除入口(后续 store/app 切片接入): 先立碑再删行。
func deleteDeviceKnownHost(t *testing.T, device *testDevice, id string, deletedAt int64) {
	t.Helper()
	ctx := context.Background()
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
	syncDevice(t, newcomer, server, "alice", "alice-pw-123")
	rows, err := newcomer.db.KnownHostList(context.Background())
	if err != nil || len(rows) != 0 {
		t.Fatalf("newcomer must not resurrect any triple row: %+v err=%v", rows, err)
	}
	requireDeviceQuiescent(t, loser, server, "alice", "alice-pw-123")
	requireDeviceQuiescent(t, winner, server, "alice", "alice-pw-123")
	requireDeviceQuiescent(t, newcomer, server, "alice", "alice-pw-123")
}

// 评审回归(顺序二: 胜者先推, 败者后同步): 败者经 displaced 路径立碑自身并 adopts 胜者;
// 删除胜者后二次收敛, 新设备同样不得复活败者。
func TestEngineKnownHostWinnerFirstLoserDisplaced(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	winner := newTestDevice(t)
	loser := newTestDevice(t)

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
