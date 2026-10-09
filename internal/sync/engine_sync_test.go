package sync

import (
	"context"
	"fmt"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/vault"
)

func TestEngineSyncRoundtripConverges(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	ctx := context.Background()

	groupID := ids.New()
	putDeviceGroup(t, deviceA, groupID, nil, "生产环境", 100)
	assetID := ids.New()
	host := "10.0.0.1"
	putDeviceAsset(t, deviceA, store.AssetRow{
		ID: assetID, GroupID: &groupID, Kind: "ssh", Name: "web-01", Host: &host,
		Username: strPtr("root"), CreatedAt: 100, UpdatedAt: 100,
	})
	credentialID := ids.New()
	putDeviceCredential(t, deviceA, credentialID, "root 密码", "s3cret-中文", 100)
	snippetID := ids.New()
	putDeviceSnippet(t, deviceA, snippetID, "部署", "systemctl restart app", 100)
	endedAt := int64(200)
	putDeviceTranscript(t, deviceA, ids.New(), 150, &endedAt, []byte("session output line"), true)

	report := syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	if report.Pushed == 0 {
		t.Fatalf("expected pushes, report=%+v", report)
	}
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")

	groups, err := deviceB.db.GroupList(ctx)
	if err != nil || len(groups) != 1 || groups[0].Name != "生产环境" {
		t.Fatalf("deviceB groups=%+v err=%v", groups, err)
	}
	assets, err := deviceB.db.AssetList(ctx, true)
	if err != nil || len(assets) != 2 {
		t.Fatalf("deviceB assets=%+v err=%v", assets, err)
	}
	asset, err := deviceB.db.AssetGet(ctx, assetID)
	if err != nil || asset.Host == nil || *asset.Host != "10.0.0.1" || asset.Username == nil || *asset.Username != "root" {
		t.Fatalf("deviceB asset=%+v err=%v", asset, err)
	}
	row, err := deviceB.db.CredentialGetRow(ctx, credentialID)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := deviceB.vault.DecryptCredentialString(ctx, row)
	if err != nil || secret != "s3cret-中文" {
		t.Fatalf("deviceB secret=%q err=%v", secret, err)
	}
	snippets, err := deviceB.db.SnippetList(ctx)
	if err != nil || len(snippets) != 1 || snippets[0].Body != "systemctl restart app" {
		t.Fatalf("deviceB snippets=%+v err=%v", snippets, err)
	}
	transcripts, err := deviceB.db.TranscriptListByAsset(ctx, "asset-1", 0)
	if err != nil || len(transcripts) != 1 {
		t.Fatalf("deviceB transcripts=%+v err=%v", transcripts, err)
	}
	chunks, err := deviceB.db.TranscriptChunks(ctx, transcripts[0].ID, 0, 1<<20)
	if err != nil || len(chunks) != 1 || string(chunks[0].Data) != "session output line" {
		t.Fatalf("deviceB chunks=%+v err=%v", chunks, err)
	}

	// B 的新增对象回流到 A。
	assetB := ids.New()
	putDeviceAsset(t, deviceB, store.AssetRow{ID: assetB, Kind: "ssh", Name: "db-01", CreatedAt: 300, UpdatedAt: 300})
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	if names := deviceAssetNames(t, deviceA); names[assetB] != "db-01" {
		t.Fatalf("deviceA missing asset from B: %v", names)
	}

	// 幂等: 双方再同步一轮不应产生任何变更。
	reportA := syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	reportB := syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if reportA.Pulled+reportA.Applied+reportA.Pushed != 0 || reportB.Pulled+reportB.Applied+reportB.Pushed != 0 {
		rows, _ := server.db.DB().QueryContext(ctx, "SELECT id, seq FROM user_sync_object ORDER BY seq")
		var desc []string
		for rows.Next() {
			var id string
			var seq int64
			rows.Scan(&id, &seq)
			desc = append(desc, fmt.Sprintf("%s@%d", id, seq))
		}
		rows.Close()
		t.Fatalf("idle sync not quiescent: A=%+v B=%+v objects=%v", reportA, reportB, desc)
	}
}

func TestEngineLWWConflictConverges(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	ctx := context.Background()

	assetID := ids.New()
	putDeviceAsset(t, deviceA, store.AssetRow{ID: assetID, Kind: "ssh", Name: "原始", CreatedAt: 100, UpdatedAt: 100})
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")

	// 双方各自离线编辑同一资产, B 的修订号更高。
	putDeviceAsset(t, deviceA, store.AssetRow{ID: assetID, Kind: "ssh", Name: "A 的编辑", CreatedAt: 100, UpdatedAt: 200})
	putDeviceAsset(t, deviceB, store.AssetRow{ID: assetID, Kind: "ssh", Name: "B 的编辑", CreatedAt: 100, UpdatedAt: 300})
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")

	for name, device := range map[string]*testDevice{"A": deviceA, "B": deviceB} {
		asset, err := device.db.AssetGet(ctx, assetID)
		if err != nil {
			t.Fatal(err)
		}
		if asset.Name != "B 的编辑" {
			t.Fatalf("device%s asset name=%q, want B 的编辑", name, asset.Name)
		}
	}
}

func TestEngineTombstonePreventsResurrection(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	ctx := context.Background()

	credentialID := ids.New()
	putDeviceCredential(t, deviceA, credentialID, "临时口令", "value", 100)
	snippetID := ids.New()
	putDeviceSnippet(t, deviceA, snippetID, "一次性片段", "body", 100)
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")

	// A 删除凭据与片段, 墓碑随同步传播。
	if err := deviceA.db.CredentialDelete(ctx, credentialID); err != nil {
		t.Fatal(err)
	}
	if _, err := deviceA.db.DB().ExecContext(ctx, "DELETE FROM snippet WHERE id=?", snippetID); err != nil {
		t.Fatal(err)
	}
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")

	if _, err := deviceB.db.CredentialGetRow(ctx, credentialID); !isNotFound(err) {
		t.Fatalf("credential resurrected on B: %v", err)
	}
	snippets, err := deviceB.db.SnippetList(ctx)
	if err != nil || len(snippets) != 0 {
		t.Fatalf("snippet resurrected on B: %+v err=%v", snippets, err)
	}

	// B 的本地墓碑记忆使旧对象无法借任何后续同步复活。
	report := syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if report.Applied != 0 {
		t.Fatalf("tombstone re-applied: %+v", report)
	}
	if _, err := deviceB.db.CredentialGetRow(ctx, credentialID); !isNotFound(err) {
		t.Fatalf("credential resurrected after idle sync: %v", err)
	}
}

func TestEngineTranscriptSelectiveSync(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	ctx := context.Background()

	endedAt := int64(300)
	optedIn := ids.New()
	putDeviceTranscript(t, deviceA, optedIn, 100, &endedAt, []byte("visible output"), true)
	notOpted := ids.New()
	putDeviceTranscript(t, deviceA, notOpted, 110, &endedAt, []byte("local only"), false)
	inProgress := ids.New()
	putDeviceTranscript(t, deviceA, inProgress, 120, nil, []byte("ongoing"), false)
	if err := deviceA.db.TranscriptSetSyncOptIn(ctx, inProgress, true); err == nil {
		t.Fatal("in-progress transcript must not be opt-in")
	}

	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")

	rows, err := deviceB.db.TranscriptListByAsset(ctx, "asset-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != optedIn {
		t.Fatalf("deviceB transcripts=%+v, want only opted-in", rows)
	}
	if rows[0].SyncOptIn != true {
		t.Fatal("synced transcript not marked opted-in on B")
	}
	chunks, err := deviceB.db.TranscriptChunks(ctx, optedIn, 0, 1<<20)
	if err != nil || len(chunks) != 1 || string(chunks[0].Data) != "visible output" {
		t.Fatalf("deviceB chunks=%+v err=%v", chunks, err)
	}

	// 超限记录只同步元数据。
	huge := ids.New()
	putDeviceTranscript(t, deviceA, huge, 130, &endedAt, []byte("x"), true)
	if _, err := deviceA.db.DB().ExecContext(ctx, "UPDATE transcript SET bytes=? WHERE id=?", syncTranscriptMaxContentBytes+1, huge); err != nil {
		t.Fatal(err)
	}
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	row, err := deviceB.db.TranscriptGet(ctx, huge)
	if err != nil {
		t.Fatal(err)
	}
	if !row.ContentOmitted || row.Bytes <= syncTranscriptMaxContentBytes {
		t.Fatalf("oversized transcript should sync metadata only: %+v", row)
	}

	// 删除 opt-in 记录: 墓碑传播, B 端副本被清除且不复活。
	if err := deviceA.db.TranscriptDelete(ctx, optedIn); err != nil {
		t.Fatal(err)
	}
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if _, err := deviceB.db.TranscriptGet(ctx, optedIn); !isNotFound(err) {
		t.Fatalf("transcript resurrected on B: %v", err)
	}
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if _, err := deviceB.db.TranscriptGet(ctx, optedIn); !isNotFound(err) {
		t.Fatalf("transcript resurrected on B after idle sync: %v", err)
	}

	// opt-out: 本地保留, 服务端副本被墓碑清除。
	stayLocal := ids.New()
	putDeviceTranscript(t, deviceA, stayLocal, 140, &endedAt, []byte("keep me"), true)
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if err := deviceA.db.TranscriptSetSyncOptIn(ctx, stayLocal, false); err != nil {
		t.Fatal(err)
	}
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if _, err := deviceB.db.TranscriptGet(ctx, stayLocal); !isNotFound(err) {
		t.Fatalf("opt-out transcript still on B: %v", err)
	}
	if _, err := deviceA.db.TranscriptGet(ctx, stayLocal); err != nil {
		t.Fatalf("opt-out transcript should stay on A: %v", err)
	}
}

func TestEngineMultiUserIsolationAndPlaintextScan(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	server.createUser(t, "bob", "bob-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	ctx := context.Background()

	canaryHost := "canary-host-7f3a2b.internal"
	assetID := ids.New()
	host := canaryHost
	putDeviceAsset(t, deviceA, store.AssetRow{
		ID: assetID, Kind: "ssh", Name: "canary-name-9x8z", Host: &host,
		Username: strPtr("canary-user-5k4j"), CreatedAt: 100, UpdatedAt: 100,
	})
	putDeviceCredential(t, deviceA, ids.New(), "canary-cred-2m9n", "canary-secret-1q2w", 100)
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")

	// bob 同步: 看不到 alice 的任何对象。
	syncDevice(t, deviceB, server, "bob", "bob-pw-123")
	assets, err := deviceB.db.AssetList(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, asset := range assets {
		if asset.ID == assetID {
			t.Fatal("bob sees alice's asset")
		}
	}

	// 服务端行与日志不含明文同步内容。
	rows, err := server.db.DB().QueryContext(ctx, "SELECT blob FROM user_sync_object")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	blobs := 0
	for rows.Next() {
		var blob []byte
		if err := rows.Scan(&blob); err != nil {
			t.Fatal(err)
		}
		blobs++
		for _, canary := range []string{"canary-name-9x8z", canaryHost, "canary-user-5k4j", "canary-cred-2m9n", "canary-secret-1q2w"} {
			if containsFold(blob, canary) {
				t.Fatalf("server blob leaks %q", canary)
			}
		}
	}
	if blobs == 0 {
		t.Fatal("no objects stored server-side")
	}

	// bob 拿到 alice 的 blob 也无法解密(错误密钥隔离)。
	aliceBlob := []byte(nil)
	if err := server.db.DB().QueryRowContext(ctx,
		"SELECT blob FROM user_sync_object WHERE id = ?", assetID).Scan(&aliceBlob); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tryOpenObject(mustDEK(t, server, "alice", "alice-pw-123"), aliceBlob, assetID); err != nil {
		t.Fatal("alice DEK should open alice blob")
	}
	if _, _, err := tryOpenObject(mustDEK(t, server, "bob", "bob-pw-123"), aliceBlob, assetID); err == nil {
		t.Fatal("bob DEK opened alice blob")
	}
}

func mustDEK(t *testing.T, server *testSyncServer, username, password string) []byte {
	t.Helper()
	ctx := context.Background()
	user, err := server.accounts.GetUserByUsername(ctx, username)
	if err != nil {
		t.Fatal(err)
	}
	envelopes, err := server.accounts.GetUserDEKEnvelopes(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	dek, err := vault.UnwrapUserDEK(password, envelopes.DEKEnvelope, envelopes.KDFSalt, envelopes.KDFParams)
	if err != nil {
		t.Fatal(err)
	}
	return dek
}

func TestEngineWrongDEKQuarantineAndHeal(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	ctx := context.Background()

	assetID := ids.New()
	putDeviceAsset(t, deviceA, store.AssetRow{ID: assetID, Kind: "ssh", Name: "旧密钥资产", CreatedAt: 100, UpdatedAt: 100})
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")

	// 账号 DEK 被重置(管理员重置后用户用新信封登录): 旧对象不可解密, 被隔离并告警。
	resetDEK(t, server, "alice", "alice-pw-123")
	report, err := deviceB.engine.Sync(ctx, deviceB.config(server, "alice", "alice-pw-123"))
	if err != nil {
		t.Fatal(err)
	}
	if report.DecryptFailed == 0 {
		t.Fatalf("expected quarantined objects, report=%+v", report)
	}
	if len(report.Warnings) == 0 {
		t.Fatal("expected quarantine warnings")
	}

	// 新 DEK 下的本地对象推送后覆盖旧密文, 同步自愈。
	putDeviceAsset(t, deviceB, store.AssetRow{ID: assetID, Kind: "ssh", Name: "新密钥资产", CreatedAt: 100, UpdatedAt: 200})
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	deviceC := newTestDevice(t)
	syncDevice(t, deviceC, server, "alice", "alice-pw-123")
	asset, err := deviceC.db.AssetGet(ctx, assetID)
	if err != nil || asset.Name != "新密钥资产" {
		t.Fatalf("deviceC asset=%+v err=%v", asset, err)
	}
}

func TestEngineConcurrentPushConflictRecovers(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)

	putDeviceGroup(t, deviceA, ids.New(), nil, "A 分组", 100)
	putDeviceGroup(t, deviceB, ids.New(), nil, "B 分组", 100)
	// 两个设备基于同一 head 先后推送; 后推者必须经冲突恢复收敛, 而不是覆盖。
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")

	ctx := context.Background()
	for name, device := range map[string]*testDevice{"A": deviceA, "B": deviceB} {
		groups, err := device.db.GroupList(ctx)
		if err != nil {
			t.Fatal(err)
		}
		names := map[string]bool{}
		for _, group := range groups {
			names[group.Name] = true
		}
		if !names["A 分组"] || !names["B 分组"] {
			t.Fatalf("device%s groups=%v, want both", name, names)
		}
	}
}

func strPtr(value string) *string { return &value }

func resetDEK(t *testing.T, server *testSyncServer, username, password string) {
	t.Helper()
	ctx := context.Background()
	user, err := server.accounts.GetUserByUsername(ctx, username)
	if err != nil {
		t.Fatal(err)
	}
	_, envelopes, _, err := vault.GenerateUserDEKEnvelopes(password)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.accounts.SetUserDEKEnvelopes(ctx, user.ID, envelopes); err != nil {
		t.Fatal(err)
	}
}
