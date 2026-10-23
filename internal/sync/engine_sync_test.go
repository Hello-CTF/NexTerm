package sync

import (
	"context"
	"fmt"
	"sync"
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

	// B's new object flows back to A.
	assetB := ids.New()
	putDeviceAsset(t, deviceB, store.AssetRow{ID: assetB, Kind: "ssh", Name: "db-01", CreatedAt: 300, UpdatedAt: 300})
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	if names := deviceAssetNames(t, deviceA); names[assetB] != "db-01" {
		t.Fatalf("deviceA missing asset from B: %v", names)
	}

	// Idempotent: one more sync round on both sides must produce no changes.
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

	// Both sides edit the same asset offline; B's revision is higher.
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
			t.Fatalf("device%s asset name=%q, want B's edit", name, asset.Name)
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

	// A deletes the credential and snippet; tombstones propagate with sync.
	if err := deviceA.db.CredentialDelete(ctx, credentialID); err != nil {
		t.Fatal(err)
	}
	if err := deviceA.db.SnippetDelete(ctx, snippetID); err != nil {
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

	// B's local tombstone memory keeps old objects from reviving via any later sync.
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
	if rows[0].StoredBytes <= 0 {
		t.Fatalf("synced transcript did not persist its compressed size: %+v", rows[0])
	}
	chunks, err := deviceB.db.TranscriptChunks(ctx, optedIn, 0, 1<<20)
	if err != nil || len(chunks) != 1 || string(chunks[0].Data) != "visible output" {
		t.Fatalf("deviceB chunks=%+v err=%v", chunks, err)
	}

	// Oversized records sync metadata only.
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

	// Delete the opted-in record: the tombstone propagates and B's copy is cleared without revival.
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

	// opt-out: kept locally, the server-side copy is cleared by the tombstone.
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

	// bob syncs: he must not see any of alice's objects.
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

	// Server-side rows and logs carry no plaintext sync content.
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

	// Even with alice's blob in hand, bob cannot decrypt it (wrong-key isolation).
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

func TestEngineWrongDEKQuarantineBlocksAutomaticOverwrite(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	ctx := context.Background()

	assetID := ids.New()
	putDeviceAsset(t, deviceA, store.AssetRow{ID: assetID, Kind: "ssh", Name: "旧密钥资产", CreatedAt: 100, UpdatedAt: 100})
	localID := ids.New()
	putDeviceAsset(t, deviceA, store.AssetRow{ID: localID, Kind: "ssh", Name: "旧密钥下的服务端资产", CreatedAt: 100, UpdatedAt: 100})
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	putDeviceAsset(t, deviceB, store.AssetRow{ID: localID, Kind: "ssh", Name: "本机已有副本", CreatedAt: 100, UpdatedAt: 200})
	serverBlobHash := func(id string) string {
		t.Helper()
		var blob []byte
		if err := server.db.DB().QueryRowContext(ctx, "SELECT blob FROM user_sync_object WHERE id = ?", id).Scan(&blob); err != nil {
			t.Fatal(err)
		}
		return hashBlobHex(blob)
	}
	oldAssetHash, oldLocalHash := serverBlobHash(assetID), serverBlobHash(localID)

	// The account DEK was reset (admin reset, user logged in with a new envelope): old objects cannot be decrypted and are quarantined with a warning.
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
	if report.Pushed != 0 || serverBlobHash(assetID) != oldAssetHash || serverBlobHash(localID) != oldLocalHash {
		t.Fatalf("initial quarantine overwrote cloud ciphertext: report=%+v", report)
	}

	// Creating a same-ID local object later must not turn the quarantine into an automatic repair.
	putDeviceAsset(t, deviceB, store.AssetRow{ID: assetID, Kind: "ssh", Name: "新密钥资产", CreatedAt: 100, UpdatedAt: 200})
	report = syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if report.Pushed != 0 || serverBlobHash(assetID) != oldAssetHash || serverBlobHash(localID) != oldLocalHash {
		t.Fatalf("later local copy overwrote cloud ciphertext: report=%+v", report)
	}
	deviceB.engine = NewEngine(deviceB.db, deviceB.vault, nil)
	report = syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if report.Pushed != 0 || serverBlobHash(assetID) != oldAssetHash {
		t.Fatalf("quarantine did not survive engine restart: report=%+v", report)
	}

	// A changed cloud blob is pulled again. If it remains unreadable, the new blob is quarantined too.
	session, err := deviceB.engine.ensureSession(ctx, deviceB.config(server, "alice", "alice-pw-123"))
	if err != nil {
		t.Fatal(err)
	}
	pushRemote := func(blob []byte) {
		t.Helper()
		idsResponse, err := session.client.ids(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := session.client.push(ctx, idsResponse.Head, []WireObject{{ID: assetID, Blob: blob}}); err != nil {
			t.Fatal(err)
		}
	}
	pushRemote([]byte("changed-unreadable-ciphertext"))
	report = syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if report.DecryptFailed == 0 || report.Pushed != 0 || serverBlobHash(assetID) != hashBlobHex([]byte("changed-unreadable-ciphertext")) {
		t.Fatalf("changed unreadable cloud blob was not re-quarantined: report=%+v", report)
	}

	// Once another client explicitly repairs the cloud blob under the new DEK, normal merge resumes.
	plaintext, err := marshalObject(assetObject{ID: assetID, Kind: "ssh", Name: "服务端已修复", CreatedAt: 100, UpdatedAt: 300})
	if err != nil {
		t.Fatal(err)
	}
	blob, err := sealObject(mustDEK(t, server, "alice", "alice-pw-123"), plaintext, assetID, KindAsset)
	if err != nil {
		t.Fatal(err)
	}
	pushRemote(blob)
	report = syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	asset, err := deviceB.db.AssetGet(ctx, assetID)
	if err != nil || asset.Name != "服务端已修复" || report.Applied == 0 {
		t.Fatalf("repaired cloud object was not applied: asset=%+v report=%+v err=%v", asset, report, err)
	}
}

func TestEngineConcurrentPushConflictRecovers(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)

	putDeviceGroup(t, deviceA, ids.New(), nil, "A 分组", 100)
	putDeviceGroup(t, deviceB, ids.New(), nil, "B 分组", 100)
	// Two devices push in turn based on the same head; the later pusher must converge via conflict recovery, not overwrite.
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

// Deterministic stress (GT-07): several devices push concurrently against the same account over real HTTP.
// The server head CAS serializes concurrent pushes and the engine retries conflicts inside Sync; every device
// must converge to exactly the union of all objects, concurrent same-object edits must resolve to the single
// highest revision everywhere regardless of push order, and idle rounds stay quiescent.
func TestEngineConcurrentSyncStressConverges(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	ctx := context.Background()

	// maxSyncAttempts=3 bounds worst-case conflict retries: with deviceCount devices the unluckiest
	// pusher loses at most deviceCount-1 head races, so 3 devices always fit the retry budget.
	const deviceCount = 3
	const perDevice = 8
	devices := make([]*testDevice, 0, deviceCount)
	want := map[string]string{}
	for d := 0; d < deviceCount; d++ {
		device := newTestDevice(t)
		for i := 0; i < perDevice; i++ {
			id := ids.New()
			body := fmt.Sprintf("stress-body-%d-%d", d, i)
			putDeviceSnippet(t, device, id, fmt.Sprintf("stress-%d-%d", d, i), body, 100)
			want[id] = body
		}
		devices = append(devices, device)
	}

	syncConcurrently := func() {
		t.Helper()
		var wg sync.WaitGroup
		errs := make(chan error, len(devices))
		for _, device := range devices {
			wg.Add(1)
			go func(device *testDevice) {
				defer wg.Done()
				if _, err := device.engine.Sync(ctx, device.config(server, "alice", "alice-pw-123")); err != nil {
					errs <- err
				}
			}(device)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("concurrent sync: %v", err)
		}
	}

	// Phase 1: all devices push their distinct objects concurrently.
	syncConcurrently()

	// Phase 2: concurrent edits of the same object at distinct revisions; the highest revision must win everywhere.
	sharedID := ids.New()
	for d, device := range devices {
		putDeviceSnippet(t, device, sharedID, "stress-shared", fmt.Sprintf("shared-rev-%d", 100*(d+1)), int64(100*(d+1)))
	}
	syncConcurrently()
	want[sharedID] = fmt.Sprintf("shared-rev-%d", 100*deviceCount)

	// Phase 3: bounded convergence rounds until every device is quiescent, then exact-union assertions.
	for _, device := range devices {
		quiescent := false
		var lastReport SyncReport
		for round := 0; round < deviceCount+2 && !quiescent; round++ {
			lastReport = syncDevice(t, device, server, "alice", "alice-pw-123")
			quiescent = lastReport.Pulled+lastReport.Applied+lastReport.Pushed == 0
		}
		if !quiescent {
			t.Fatalf("device did not reach a quiescent idle round, last report: %+v", lastReport)
		}
		rows, err := device.db.SnippetList(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != len(want) {
			t.Fatalf("device snippets=%d, want %d", len(rows), len(want))
		}
		for _, row := range rows {
			if want[row.ID] != row.Body {
				t.Fatalf("device snippet %s body=%q, want %q", row.ID, row.Body, want[row.ID])
			}
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
