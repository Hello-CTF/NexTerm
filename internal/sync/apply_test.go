package sync

import (
	"context"
	"encoding/json"
	"strings"
	stdsync "sync"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

func applyPayload(t *testing.T, payload any) json.RawMessage {
	t.Helper()
	encoded, err := marshalObject(payload)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func applyGroupObject(t *testing.T, id, name string, updatedAt int64) ApplyObject {
	t.Helper()
	return ApplyObject{ID: id, Kind: KindGroup, Payload: applyPayload(t, groupObject{
		ID: id, Name: name, CreatedAt: 1, UpdatedAt: updatedAt,
	})}
}

func mustApplyObjects(t *testing.T, instance *testInstance, objects ...ApplyObject) ApplyObjectsResult {
	t.Helper()
	result, err := instance.service.ApplyObjects(context.Background(), ApplyObjectsRequest{Objects: objects})
	if err != nil {
		t.Fatalf("apply objects: %v", err)
	}
	return result
}

func requireApplyResult(t *testing.T, entry ApplyObjectResult, result string) {
	t.Helper()
	if entry.Result != result {
		t.Fatalf("object %s result=%q warning=%q, want %q", entry.ID, entry.Result, entry.Warning, result)
	}
}

func TestApplyObjectsDuplicateIdempotent(t *testing.T) {
	instance := newTestInstance(t, false)
	groupID := ids.New()
	object := applyGroupObject(t, groupID, "同步分组", 100)

	first := mustApplyObjects(t, instance, object)
	requireApplyResult(t, first.Objects[0], ApplyResultApplied)
	second := mustApplyObjects(t, instance, object)
	requireApplyResult(t, second.Objects[0], ApplyResultIdentical)

	if first.Applied != 1 || first.Identical != 0 || first.Skipped != 0 {
		t.Fatalf("first result=%+v", first)
	}
	if second.Applied != 0 || second.Identical != 1 || second.Skipped != 0 {
		t.Fatalf("second result=%+v", second)
	}
	groups, err := instance.db.GroupList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].ID != groupID || groups[0].Name != "同步分组" {
		t.Fatalf("groups=%+v", groups)
	}
	if groups[0].UpdatedAt != 100 {
		t.Fatalf("revision must be preserved: %+v", groups[0])
	}
}

func TestApplyObjectsNoRevisionRollback(t *testing.T) {
	instance := newTestInstance(t, false)
	ctx := context.Background()
	groupID := ids.New()
	if _, err := instance.db.GroupUpsert(ctx, groupID, nil, "本地新版本", 0, 1, 200); err != nil {
		t.Fatal(err)
	}

	result := mustApplyObjects(t, instance, applyGroupObject(t, groupID, "远端旧版本", 100))
	requireApplyResult(t, result.Objects[0], ApplyResultSkipped)

	group, err := instance.db.GroupGet(ctx, groupID)
	if err != nil {
		t.Fatal(err)
	}
	if group.Name != "本地新版本" {
		t.Fatalf("older revision rolled back local object: %+v", group)
	}
}

func TestApplyObjectsTombstoneAntiResurrectionAndStaleClearing(t *testing.T) {
	instance := newTestInstance(t, false)
	ctx := context.Background()
	groupID := ids.New()

	mustApplyObjects(t, instance, applyGroupObject(t, groupID, "原始分组", 100))
	tombstone := ApplyObject{ID: groupID, Kind: KindTombstone, Payload: applyPayload(t, tombstoneObject{
		TargetKind: KindGroup, DeletedAt: 200,
	})}
	result := mustApplyObjects(t, instance, tombstone)
	requireApplyResult(t, result.Objects[0], ApplyResultApplied)
	if _, err := instance.db.GroupGet(ctx, groupID); !isNotFound(err) {
		t.Fatalf("group must be deleted by tombstone: %v", err)
	}

	stale := mustApplyObjects(t, instance, applyGroupObject(t, groupID, "旧版本复活", 150))
	requireApplyResult(t, stale.Objects[0], ApplyResultSkipped)
	if _, err := instance.db.GroupGet(ctx, groupID); !isNotFound(err) {
		t.Fatal("older object resurrected under tombstone")
	}

	newer := mustApplyObjects(t, instance, applyGroupObject(t, groupID, "新版本", 300))
	requireApplyResult(t, newer.Objects[0], ApplyResultApplied)
	group, err := instance.db.GroupGet(ctx, groupID)
	if err != nil || group.Name != "新版本" {
		t.Fatalf("newer object must win: %+v err=%v", group, err)
	}
	if _, found, err := instance.service.engine.syncTombstoneGet(ctx, groupID); err != nil || found {
		t.Fatalf("stale tombstone must be cleared: found=%v err=%v", found, err)
	}
}

func TestApplyObjectsMalformedRejectedPerObject(t *testing.T) {
	instance := newTestInstance(t, false)
	validID := ids.New()
	mismatchID := ids.New()

	objects := []ApplyObject{
		{ID: validID, Kind: "bogus", Payload: applyPayload(t, groupObject{ID: validID, Name: "x", UpdatedAt: 1})},
		{ID: "bad-id", Kind: KindGroup, Payload: applyPayload(t, groupObject{ID: "bad-id", Name: "x", UpdatedAt: 1})},
		{ID: ids.New(), Kind: KindGroup, Payload: json.RawMessage(`{"id":`)},
		{ID: mismatchID, Kind: KindGroup, Payload: applyPayload(t, groupObject{ID: ids.New(), Name: "x", UpdatedAt: 1})},
		{ID: ids.New(), Kind: KindGroup},
		{ID: ids.New(), Kind: KindTombstone, Payload: applyPayload(t, tombstoneObject{TargetKind: "bogus", DeletedAt: 1})},
		applyGroupObject(t, validID, "正常分组", 100),
	}
	result := mustApplyObjects(t, instance, objects...)
	if result.Applied != 1 || result.Skipped != 6 {
		t.Fatalf("result=%+v", result)
	}
	for index, entry := range result.Objects[:6] {
		requireApplyResult(t, entry, ApplyResultSkipped)
		if entry.Warning == "" {
			t.Fatalf("malformed object %d must carry a warning", index)
		}
	}
	requireApplyResult(t, result.Objects[6], ApplyResultApplied)
	if _, err := instance.db.GroupGet(context.Background(), validID); err != nil {
		t.Fatalf("valid object in mixed batch must apply: %v", err)
	}
}

func TestApplyObjectsBounds(t *testing.T) {
	instance := newTestInstance(t, false)
	objects := make([]ApplyObject, 0, maxApplyObjectsPerCall+1)
	for index := 0; index <= maxApplyObjectsPerCall; index++ {
		objects = append(objects, applyGroupObject(t, ids.New(), "批量分组", int64(index+1)))
	}
	_, err := instance.service.ApplyObjects(context.Background(), ApplyObjectsRequest{Objects: objects})
	requireCode(t, err, ipc.CodeBadParam)

	oversized := ApplyObject{ID: ids.New(), Kind: KindSnippet, Payload: json.RawMessage(`{"id":"` + ids.New() + `","body":"` + strings.Repeat("a", maxApplyObjectBytes) + `"}`)}
	result := mustApplyObjects(t, instance, oversized)
	requireApplyResult(t, result.Objects[0], ApplyResultSkipped)
	if !strings.Contains(result.Objects[0].Warning, "大小上限") {
		t.Fatalf("oversized warning=%q", result.Objects[0].Warning)
	}
}

func TestApplyObjectsMultiKind(t *testing.T) {
	instance := newTestInstance(t, true)
	ctx := context.Background()
	groupID := ids.New()
	credentialID := ids.New()
	snippetID := ids.New()
	assetID := ids.New()
	transcriptID := ids.New()
	victimID := ids.New()
	endedAt := ids.NowMS() - 1000

	mustApplyObjects(t, instance, applyGroupObject(t, victimID, "待删除分组", 100))
	objects := []ApplyObject{
		applyGroupObject(t, groupID, "同步分组", 100),
		{ID: credentialID, Kind: KindCredential, Payload: applyPayload(t, credentialObject{
			ID: credentialID, Name: "同步凭据", Kind: "password", Secret: "s3cret", UpdatedAt: 100,
		})},
		{ID: snippetID, Kind: KindSnippet, Payload: applyPayload(t, snippetObject{
			ID: snippetID, GroupID: testPtr(groupID), Name: "同步片段", Body: "body", CreatedAt: 1, UpdatedAt: 100,
		})},
		{ID: assetID, Kind: KindAsset, Payload: applyPayload(t, assetObject{
			ID: assetID, GroupID: testPtr(groupID), Kind: "ssh", Name: "同步资产", OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 100,
		})},
		{ID: victimID, Kind: KindTombstone, Payload: applyPayload(t, tombstoneObject{TargetKind: KindGroup, DeletedAt: 200})},
		{ID: transcriptID, Kind: KindTranscript, Payload: applyPayload(t, transcriptObject{
			ID: transcriptID, SessionID: ids.New(), AssetID: "asset-1", AssetName: "web-01", AssetKind: "ssh",
			StartedAt: endedAt - 500, EndedAt: endedAt, Bytes: 6, Chunks: 1,
			Content: []transcriptChunkObject{{Seq: 0, TabID: "tab-1", TS: endedAt - 400, Data: []byte("output")}},
		})},
	}
	result := mustApplyObjects(t, instance, objects...)
	if result.Applied != 6 || result.Skipped != 0 {
		t.Fatalf("result=%+v objects=%+v", result, result.Objects)
	}
	for _, entry := range result.Objects {
		requireApplyResult(t, entry, ApplyResultApplied)
	}

	if _, err := instance.db.GroupGet(ctx, groupID); err != nil {
		t.Fatalf("group: %v", err)
	}
	credential, err := instance.db.CredentialGetRow(ctx, credentialID)
	if err != nil {
		t.Fatalf("credential: %v", err)
	}
	secret, err := instance.vault.DecryptCredentialString(ctx, credential)
	if err != nil || secret != "s3cret" {
		t.Fatalf("credential secret=%q err=%v", secret, err)
	}
	if _, err := instance.db.SnippetGet(ctx, snippetID); err != nil {
		t.Fatalf("snippet: %v", err)
	}
	asset, err := instance.db.AssetGet(ctx, assetID)
	if err != nil || asset.GroupID == nil || *asset.GroupID != groupID {
		t.Fatalf("asset=%+v err=%v", asset, err)
	}
	if _, err := instance.db.GroupGet(ctx, victimID); !isNotFound(err) {
		t.Fatalf("tombstone target must be deleted: %v", err)
	}
	transcript, err := instance.db.TranscriptGet(ctx, transcriptID)
	if err != nil || !transcript.SyncOptIn {
		t.Fatalf("transcript=%+v err=%v", transcript, err)
	}
	chunks, err := instance.db.TranscriptChunks(ctx, transcriptID, 0, 1<<20)
	if err != nil || len(chunks) != 1 || string(chunks[0].Data) != "output" {
		t.Fatalf("chunks=%+v err=%v", chunks, err)
	}
}

func TestApplyObjectsCredentialVaultLocked(t *testing.T) {
	instance := newTestInstance(t, false)
	groupID := ids.New()
	credentialID := ids.New()

	result := mustApplyObjects(t, instance,
		applyGroupObject(t, groupID, "分组", 100),
		ApplyObject{ID: credentialID, Kind: KindCredential, Payload: applyPayload(t, credentialObject{
			ID: credentialID, Name: "凭据", Kind: "password", Secret: "s3cret", UpdatedAt: 100,
		})},
	)
	requireApplyResult(t, result.Objects[0], ApplyResultApplied)
	requireApplyResult(t, result.Objects[1], ApplyResultSkipped)
	if !strings.Contains(result.Objects[1].Warning, "凭据库") {
		t.Fatalf("locked vault warning=%q", result.Objects[1].Warning)
	}
	if _, err := instance.db.CredentialGetRow(context.Background(), credentialID); !isNotFound(err) {
		t.Fatalf("locked vault must not apply credential: %v", err)
	}
}

func TestApplyObjectsConcurrent(t *testing.T) {
	instance := newTestInstance(t, true)
	objectIDs := make([]string, 0, 8)
	objects := make([]ApplyObject, 0, 8)
	for index := 0; index < 8; index++ {
		id := ids.New()
		objectIDs = append(objectIDs, id)
		objects = append(objects, applyGroupObject(t, id, "并发分组", 100))
	}

	var wg stdsync.WaitGroup
	errs := make([]error, 8)
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			_, errs[worker] = instance.service.ApplyObjects(context.Background(), ApplyObjectsRequest{Objects: objects})
		}(worker)
	}
	wg.Wait()
	for worker, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", worker, err)
		}
	}
	groups, err := instance.db.GroupList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != len(objectIDs) {
		t.Fatalf("groups=%d want %d", len(groups), len(objectIDs))
	}
}
