package sync

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func mustPayload(t *testing.T, payload any) []byte {
	t.Helper()
	plaintext, err := marshalObject(payload)
	if err != nil {
		t.Fatal(err)
	}
	return plaintext
}

func mustEntity(t *testing.T, id, kind string, payload any) compareEntity {
	t.Helper()
	return newCompareEntity(id, kind, mustPayload(t, payload))
}

func TestCompareNameExtraction(t *testing.T) {
	group := mustEntity(t, "g1", KindGroup, groupObject{ID: "g1", Name: "生产环境", UpdatedAt: 1})
	if group.name != "生产环境" {
		t.Fatalf("group name=%q", group.name)
	}
	knownHost := mustEntity(t, "k1", KindKnownHost, knownHostObject{ID: "k1", Host: "example.com", Port: 22, KeyType: "ssh-ed25519", AddedAt: 1})
	if knownHost.name != "example.com:22" {
		t.Fatalf("known_host name=%q", knownHost.name)
	}
	preference := mustEntity(t, "preference:theme", KindPreference, map[string]any{"id": "preference:theme", "key": "theme", "value": "dark", "updatedAt": 1})
	if preference.name != "theme" {
		t.Fatalf("preference name=%q", preference.name)
	}
	tombstone := mustEntity(t, "t1", KindTombstone, tombstoneObject{TargetKind: KindGroup, DeletedAt: 5})
	if tombstone.name != "t1" {
		t.Fatalf("tombstone name=%q", tombstone.name)
	}
	transcript := mustEntity(t, "tr1", KindTranscript, map[string]any{"id": "tr1", "assetName": "web-01", "endedAt": 3})
	if transcript.name != "web-01 的会话记录" {
		t.Fatalf("transcript name=%q", transcript.name)
	}
	permission := mustEntity(t, "global", KindAIPermission, aiPermissionObject{ID: "global", Mode: "ask", UpdatedAt: 1})
	if permission.name != "全局 AI 权限" {
		t.Fatalf("ai_permission name=%q", permission.name)
	}
	// Falls back to the id when the name is missing or the payload is corrupt.
	broken := newCompareEntity("b1", KindAsset, []byte("{not json"))
	if broken.name != "b1" {
		t.Fatalf("broken name=%q", broken.name)
	}
}

func TestCompareNameFallbackForEmptyFields(t *testing.T) {
	unnamed := mustEntity(t, "a1", KindAsset, assetObject{ID: "a1", Name: "", UpdatedAt: 1})
	if unnamed.name != "a1" {
		t.Fatalf("empty asset name should fall back to id, got %q", unnamed.name)
	}
	hostless := mustEntity(t, "k2", KindKnownHost, knownHostObject{ID: "k2", Host: "", Port: 22, AddedAt: 1})
	if hostless.name != "k2" {
		t.Fatalf("empty host should fall back to id, got %q", hostless.name)
	}
}

func TestCompareStateDecisions(t *testing.T) {
	live := func(updatedAt int64) compareEntity {
		return mustEntity(t, "x", KindAsset, assetObject{ID: "x", Name: "web", UpdatedAt: updatedAt})
	}
	softDeleted := func(updatedAt, deletedAt int64) compareEntity {
		return mustEntity(t, "x", KindAsset, assetObject{ID: "x", Name: "web", UpdatedAt: updatedAt, DeletedAt: &deletedAt})
	}
	tombstone := func(deletedAt int64) compareEntity {
		return mustEntity(t, "x", KindTombstone, tombstoneObject{TargetKind: KindAsset, DeletedAt: deletedAt})
	}

	if state := compareState(live(100), live(100)); state != CompareStateSame {
		t.Fatalf("identical entities state=%s", state)
	}
	if state := compareState(tombstone(500), tombstone(500)); state != CompareStateSame {
		t.Fatalf("identical tombstones state=%s", state)
	}
	if state := compareState(live(200), live(100)); state != CompareStateLocalNewer {
		t.Fatalf("local revision higher state=%s", state)
	}
	if state := compareState(live(100), live(200)); state != CompareStateRemoteNewer {
		t.Fatalf("remote revision higher state=%s", state)
	}
	// Equal revisions break by payload hash lexicographic order: same rule as merge.go remoteWins.
	localTie, remoteTie := live(100), live(100)
	remoteTie = mustEntity(t, "x", KindAsset, assetObject{ID: "x", Name: "web2", UpdatedAt: 100})
	want := CompareStateRemoteNewer
	if localTie.payloadHash > remoteTie.payloadHash {
		want = CompareStateLocalNewer
	}
	if state := compareState(localTie, remoteTie); state != want {
		t.Fatalf("tie-break state=%s want %s (hashes %s vs %s)", state, want, localTie.payloadHash, remoteTie.payloadHash)
	}
	// Remote deleted form: local wins = local newer, otherwise = remote deleted.
	if state := compareState(live(600), tombstone(500)); state != CompareStateLocalNewer {
		t.Fatalf("local live newer than remote tombstone state=%s", state)
	}
	if state := compareState(live(100), tombstone(500)); state != CompareStateRemoteDeleted {
		t.Fatalf("remote tombstone newer state=%s", state)
	}
	// Local deleted form: local wins = local deleted, otherwise = remote newer.
	if state := compareState(tombstone(600), live(100)); state != CompareStateLocalDeleted {
		t.Fatalf("local tombstone newer state=%s", state)
	}
	if state := compareState(tombstone(100), live(600)); state != CompareStateRemoteNewer {
		t.Fatalf("remote live newer than local tombstone state=%s", state)
	}
	// Soft-deleted assets participate in the revision via deletedAt.
	if state := compareState(softDeleted(100, 700), live(600)); state != CompareStateLocalDeleted {
		t.Fatalf("soft-deleted local newer state=%s", state)
	}
	if state := compareState(live(100), softDeleted(100, 700)); state != CompareStateRemoteDeleted {
		t.Fatalf("soft-deleted remote newer state=%s", state)
	}
}

func TestCompareTranscriptContentFullness(t *testing.T) {
	omitted := mustEntity(t, "t", KindTranscript, map[string]any{"id": "t", "assetName": "a", "endedAt": 10, "contentOmitted": true})
	full := mustEntity(t, "t", KindTranscript, map[string]any{"id": "t", "assetName": "a", "endedAt": 10, "content": []any{map[string]any{"seq": 1}}})
	// Same endedAt: full content beats the omitted version, symmetric in both directions.
	if state := compareState(full, omitted); state != CompareStateLocalNewer {
		t.Fatalf("full local vs omitted remote state=%s", state)
	}
	if state := compareState(omitted, full); state != CompareStateRemoteNewer {
		t.Fatalf("omitted local vs full remote state=%s", state)
	}
	// Same fullness: decided by endedAt.
	newer := mustEntity(t, "t", KindTranscript, map[string]any{"id": "t", "assetName": "a", "endedAt": 20, "contentOmitted": true})
	if state := compareState(newer, omitted); state != CompareStateLocalNewer {
		t.Fatalf("newer endedAt state=%s", state)
	}
}

func TestBuildCompareRowsAssembly(t *testing.T) {
	localOnly := mustEntity(t, "l1", KindGroup, groupObject{ID: "l1", Name: "本机分组", UpdatedAt: 10})
	localTomb := mustEntity(t, "l2", KindTombstone, tombstoneObject{TargetKind: KindGroup, DeletedAt: 20})
	shared := mustEntity(t, "s1", KindSnippet, snippetObject{ID: "s1", Name: "共享片段", Body: "a", UpdatedAt: 30})
	sharedRemote := mustEntity(t, "s1", KindSnippet, snippetObject{ID: "s1", Name: "共享片段", Body: "b", UpdatedAt: 40})
	remoteOnly := mustEntity(t, "r1", KindAsset, assetObject{ID: "r1", Name: "服务端资产", UpdatedAt: 50})
	remoteTomb := mustEntity(t, "r2", KindTombstone, tombstoneObject{TargetKind: KindAsset, DeletedAt: 60})
	localForUndecryptable := mustEntity(t, "u1", KindGroup, groupObject{ID: "u1", Name: "密钥外分组", UpdatedAt: 70})

	local := map[string]compareEntity{"l1": localOnly, "l2": localTomb, "s1": shared, "u1": localForUndecryptable}
	remote := map[string]compareEntity{"s1": sharedRemote, "r1": remoteOnly, "r2": remoteTomb}
	undecryptable := map[string]bool{"u1": true, "u2": true}

	rows := buildCompareRows(local, remote, undecryptable)
	byID := map[string]CompareRow{}
	for _, row := range rows {
		byID[row.ID] = row
	}
	if len(rows) != 7 {
		t.Fatalf("rows=%d want 7", len(rows))
	}
	if row := byID["l1"]; row.State != CompareStateLocalOnly || row.Name != "本机分组" || row.UpdatedLocal != 10 {
		t.Fatalf("l1=%+v", row)
	}
	if row := byID["l2"]; row.State != CompareStateLocalDeleted {
		t.Fatalf("l2=%+v", row)
	}
	if row := byID["s1"]; row.State != CompareStateRemoteNewer || row.UpdatedLocal != 30 || row.UpdatedRemote != 40 {
		t.Fatalf("s1=%+v", row)
	}
	if row := byID["r1"]; row.State != CompareStateRemoteOnly || row.UpdatedRemote != 50 {
		t.Fatalf("r1=%+v", row)
	}
	if row := byID["r2"]; row.State != CompareStateRemoteDeleted {
		t.Fatalf("r2=%+v", row)
	}
	// Undecryptable rows with a same-name local row keep the local name and kind; pure-cloud undecryptable rows degrade to the id with an empty kind.
	if row := byID["u1"]; row.State != CompareStateRemoteUndecryptable || row.Name != "密钥外分组" || row.Kind != KindGroup || row.UpdatedLocal != 70 {
		t.Fatalf("u1=%+v", row)
	}
	if row := byID["u2"]; row.State != CompareStateRemoteUndecryptable || row.Name != "u2" || row.Kind != "" {
		t.Fatalf("u2=%+v", row)
	}
	// Differences first, same last: this case is all differences, checking name ordering and stability.
	for i := 1; i < len(rows); i++ {
		if rows[i-1].State == CompareStateSame && rows[i].State != CompareStateSame {
			t.Fatalf("same row ordered before diff: %v", rows)
		}
	}
}

func TestBuildCompareRowsSameSortsLast(t *testing.T) {
	same := mustEntity(t, "a", KindGroup, groupObject{ID: "a", Name: "zzz 一致", UpdatedAt: 1})
	diff := mustEntity(t, "b", KindGroup, groupObject{ID: "b", Name: "aaa 差异", UpdatedAt: 1})
	diffRemote := mustEntity(t, "b", KindGroup, groupObject{ID: "b", Name: "aaa 差异", UpdatedAt: 2})
	rows := buildCompareRows(
		map[string]compareEntity{"a": same, "b": diff},
		map[string]compareEntity{"a": same, "b": diffRemote},
		nil,
	)
	if len(rows) != 2 || rows[0].ID != "b" || rows[1].ID != "a" {
		t.Fatalf("diff should sort before same regardless of name: %+v", rows)
	}
}

// TestServiceCompareEndToEnd walks the full path through a real test server: everything matches after sync, then divergences are manufactured kind by kind.
func TestServiceCompareEndToEnd(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	instance := newTestInstance(t, true)
	ctx := context.Background()
	link := func() {
		t.Helper()
		if _, err := instance.service.LinkSet(ctx, LinkPatch{
			URL: testPtr(server.URL), Username: testPtr("alice"), Password: testPtr("alice-pw-123"),
		}); err != nil {
			t.Fatal(err)
		}
	}
	compare := func() CompareResult {
		t.Helper()
		result, err := instance.service.Compare(ctx)
		if err != nil {
			t.Fatalf("compare: %v", err)
		}
		return result
	}
	states := func(result CompareResult) map[string]string {
		out := map[string]string{}
		for _, row := range result.Rows {
			out[row.ID] = row.State
		}
		return out
	}

	link()
	// Nothing synced yet: both local and cloud are empty.
	if result := compare(); len(result.Rows) != 0 || result.Remote != 0 {
		t.Fatalf("empty compare=%+v", result)
	}

	// Write local objects and sync: local and cloud then match byte for byte.
	groupID, assetID, snippetID, credentialID := ids.New(), ids.New(), ids.New(), ids.New()
	host := "10.0.0.1"
	putTestGroup(t, instance, groupID, nil, "生产环境")
	if _, err := instance.db.AssetUpsert(ctx, store.AssetRow{ID: assetID, Kind: "ssh", Name: "web-01", Host: &host, CreatedAt: 100, UpdatedAt: 100}); err != nil {
		t.Fatal(err)
	}
	putTestCredential(t, instance, credentialID, "root 密码", "password", "s3cret")
	if _, err := instance.db.DB().ExecContext(ctx,
		`INSERT INTO snippet(id, group_id, name, body, sort, created_at, updated_at) VALUES(?,NULL,?,?,0,?,?)`,
		snippetID, "部署", "systemctl restart app", 1, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.service.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	result := compare()
	if result.Remote != 4 || len(result.Rows) != 4 {
		t.Fatalf("after sync rows=%d remote=%d", len(result.Rows), result.Remote)
	}
	for id, state := range states(result) {
		if state != CompareStateSame {
			t.Fatalf("after sync %s state=%s (rows=%+v)", id, state, result.Rows)
		}
	}
	// Row names come from the local entities.
	names := map[string]string{}
	for _, row := range result.Rows {
		names[row.ID] = row.Name
	}
	if names[groupID] != "生产环境" || names[assetID] != "web-01" || names[snippetID] != "部署" || names[credentialID] != "root 密码" {
		t.Fatalf("names=%v", names)
	}

	// Local only: a new group not yet synced.
	localOnlyID := ids.New()
	putTestGroup(t, instance, localOnlyID, nil, "未推送")
	if state := states(compare())[localOnlyID]; state != CompareStateLocalOnly {
		t.Fatalf("local-only state=%s", state)
	}

	// Local newer: the asset revision is higher than the cloud's.
	if _, err := instance.db.AssetUpsert(ctx, store.AssetRow{ID: assetID, Kind: "ssh", Name: "web-01-改名", Host: &host, CreatedAt: 100, UpdatedAt: 200}); err != nil {
		t.Fatal(err)
	}
	if state := states(compare())[assetID]; state != CompareStateLocalNewer {
		t.Fatalf("local-newer state=%s", state)
	}

	// Remote only: another device pushed an asset unknown locally (Compare is read-only and never applies, so the local side stays absent).
	deviceB := newTestDevice(t)
	remoteOnlyID := ids.New()
	putDeviceAsset(t, deviceB, store.AssetRow{ID: remoteOnlyID, Kind: "ssh", Name: "db-01", CreatedAt: 300, UpdatedAt: 300})
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	if state := states(compare())[remoteOnlyID]; state != CompareStateRemoteOnly {
		t.Fatalf("remote-only state=%s", state)
	}

	// Remote newer: B edits the shared asset to a higher revision and pushes (local @200 unpushed).
	// Remote deleted: B deletes the shared snippet; the remote tombstone revision is higher than the local live revision.
	putDeviceAsset(t, deviceB, store.AssetRow{ID: assetID, Kind: "ssh", Name: "web-01-B 改", Host: &host, CreatedAt: 100, UpdatedAt: 300})
	if err := deviceB.db.SnippetDelete(ctx, snippetID); err != nil {
		t.Fatal(err)
	}
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	got := states(compare())
	if state := got[assetID]; state != CompareStateRemoteNewer {
		t.Fatalf("remote-newer state=%s", state)
	}
	if state := got[snippetID]; state != CompareStateRemoteDeleted {
		t.Fatalf("remote-deleted state=%s", state)
	}

	// Local deleted: delete the local group (the tombstone revision is the current millisecond, far above the cloud live revision).
	if err := instance.db.GroupDelete(ctx, groupID); err != nil {
		t.Fatal(err)
	}
	if state := states(compare())[groupID]; state != CompareStateLocalDeleted {
		t.Fatalf("local-deleted state=%s", state)
	}
}

// TestServiceCompareUndecryptableRows: after a DEK reset the old ciphertext cannot be decrypted; rows are labeled remote-undecryptable truthfully.
func TestServiceCompareUndecryptableRows(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	ctx := context.Background()

	deviceA := newTestDevice(t)
	assetID := ids.New()
	putDeviceAsset(t, deviceA, store.AssetRow{ID: assetID, Kind: "ssh", Name: "旧密钥资产", CreatedAt: 100, UpdatedAt: 100})
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	resetDEK(t, server, "alice", "alice-pw-123")

	instance := newTestInstance(t, true)
	if _, err := instance.service.LinkSet(ctx, LinkPatch{
		URL: testPtr(server.URL), Username: testPtr("alice"), Password: testPtr("alice-pw-123"),
	}); err != nil {
		t.Fatal(err)
	}
	result, err := instance.service.Compare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Remote != 1 || len(result.Rows) != 1 {
		t.Fatalf("result=%+v", result)
	}
	row := result.Rows[0]
	if row.ID != assetID || row.State != CompareStateRemoteUndecryptable || row.Name != assetID || row.Kind != "" {
		t.Fatalf("row=%+v", row)
	}
}

// TestServiceCompareRequiresDesktopAndLink boundaries: non-desktop is rejected, unconfigured link is rejected.
func TestServiceCompareRequiresDesktopAndLink(t *testing.T) {
	ctx := context.Background()
	web := newTestInstance(t, true)
	web.service.desktop = false
	if _, err := web.service.Compare(ctx); err == nil {
		t.Fatal("non-desktop compare must fail")
	} else {
		requireCode(t, err, ipc.CodeUnsupported)
	}

	instance := newTestInstance(t, true)
	if _, err := instance.service.Compare(ctx); err == nil {
		t.Fatal("compare without link must fail")
	} else {
		requireCode(t, err, ipc.CodeBadParam)
	}
}
