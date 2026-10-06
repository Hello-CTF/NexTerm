package sync

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	stdsync "sync"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func collectAssetIDs(assets []CollectAsset) []string {
	result := make([]string, 0, len(assets))
	for _, asset := range assets {
		result = append(result, asset.ID)
	}
	sort.Strings(result)
	return result
}

func TestCollectAssetsIncludeDeleted(t *testing.T) {
	instance := newTestInstance(t, false)
	ctx := context.Background()
	liveID := ids.New()
	deletedID := ids.New()
	deletedAt := int64(500)
	putTestAsset(t, instance, store.AssetRow{ID: liveID, Kind: "ssh", Name: "存活资产", OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 100})
	putTestAsset(t, instance, store.AssetRow{ID: deletedID, Kind: "ssh", Name: "已删资产", OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 100, DeletedAt: &deletedAt})

	withoutDeleted, err := instance.service.CollectAssets(ctx, CollectAssetsRequest{IncludeDeleted: false})
	if err != nil {
		t.Fatal(err)
	}
	if len(withoutDeleted.Assets) != 1 || withoutDeleted.Assets[0].ID != liveID {
		t.Fatalf("includeDeleted=false must hide soft-deleted: %+v", withoutDeleted.Assets)
	}
	if withoutDeleted.Assets[0].DeletedAt != nil {
		t.Fatalf("live asset must not carry deletedAt: %+v", withoutDeleted.Assets[0])
	}

	withDeleted, err := instance.service.CollectAssets(ctx, CollectAssetsRequest{IncludeDeleted: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(withDeleted.Assets) != 2 {
		t.Fatalf("includeDeleted=true must return soft-deleted: %+v", withDeleted.Assets)
	}
	byID := map[string]CollectAsset{}
	for _, asset := range withDeleted.Assets {
		byID[asset.ID] = asset
	}
	entry := byID[deletedID]
	if entry.DeletedAt == nil || *entry.DeletedAt != deletedAt {
		t.Fatalf("soft-deleted asset must carry its revision: %+v", entry)
	}
	if entry.OptionsJSON != "{}" || entry.UpdatedAt != 100 || entry.CreatedAt != 1 {
		t.Fatalf("M117 field alignment broken: %+v", entry)
	}
	for _, asset := range withDeleted.Assets {
		if asset.ID == store.BuiltinLocalAssetID {
			t.Fatal("builtin local asset must never be collected")
		}
	}
}

func TestCollectAssetsPagination(t *testing.T) {
	instance := newTestInstance(t, false)
	ctx := context.Background()
	want := make([]string, 0, 5)
	for index := 0; index < 5; index++ {
		id := ids.New()
		want = append(want, id)
		putTestAsset(t, instance, store.AssetRow{ID: id, Kind: "ssh", Name: "分页资产", OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 100})
	}
	sort.Strings(want)

	var walked []string
	afterID := ""
	pages := 0
	for {
		result, err := instance.service.CollectAssets(ctx, CollectAssetsRequest{IncludeDeleted: false, AfterID: afterID, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		pages++
		if len(result.Assets) == 0 {
			t.Fatal("empty page before hasMore=false")
		}
		walked = append(walked, collectAssetIDs(result.Assets)...)
		if !result.HasMore {
			break
		}
		afterID = result.NextAfterID
		if afterID == "" {
			t.Fatal("hasMore page must return nextAfterId")
		}
	}
	if pages != 3 {
		t.Fatalf("5 assets at limit 2 must take 3 pages, got %d", pages)
	}
	sort.Strings(walked)
	if len(walked) != len(want) {
		t.Fatalf("pagination lost or duplicated entries: %v vs %v", walked, want)
	}
	for index := range want {
		if walked[index] != want[index] {
			t.Fatalf("page walk mismatch: %v vs %v", walked, want)
		}
	}

	clamped, err := instance.service.CollectAssets(ctx, CollectAssetsRequest{IncludeDeleted: false, Limit: maxCollectLimit * 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(clamped.Assets) != 5 || clamped.HasMore {
		t.Fatalf("over-max limit must clamp, not fail: %+v", clamped)
	}
}

func TestCollectTombstonesBothKinds(t *testing.T) {
	instance := newTestInstance(t, false)
	ctx := context.Background()
	groupID := ids.New()
	snippetID := ids.New()
	credentialID := ids.New()
	if err := instance.service.engine.syncTombstonePut(ctx, groupID, KindGroup, 200); err != nil {
		t.Fatal(err)
	}
	if err := instance.service.engine.syncTombstonePut(ctx, snippetID, KindSnippet, 300); err != nil {
		t.Fatal(err)
	}
	if err := instance.db.CredentialTombstonePut(ctx, credentialID, 400); err != nil {
		t.Fatal(err)
	}

	result, err := instance.service.CollectTombstones(ctx, CollectTombstonesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Tombstones) != 3 {
		t.Fatalf("both tombstone tables must be listed: %+v", result.Tombstones)
	}
	byID := map[string]CollectTombstone{}
	for _, tombstone := range result.Tombstones {
		byID[tombstone.ID] = tombstone
	}
	if entry := byID[groupID]; entry.TargetKind != KindGroup || entry.DeletedAt != 200 {
		t.Fatalf("group tombstone=%+v", entry)
	}
	if entry := byID[snippetID]; entry.TargetKind != KindSnippet || entry.DeletedAt != 300 {
		t.Fatalf("snippet tombstone=%+v", entry)
	}
	if entry := byID[credentialID]; entry.TargetKind != KindCredential || entry.DeletedAt != 400 {
		t.Fatalf("credential tombstone=%+v", entry)
	}
}

func TestCollectTombstonesCredentialWinsIdCollision(t *testing.T) {
	instance := newTestInstance(t, false)
	ctx := context.Background()
	sharedID := ids.New()
	if err := instance.service.engine.syncTombstonePut(ctx, sharedID, KindGroup, 200); err != nil {
		t.Fatal(err)
	}
	if err := instance.db.CredentialTombstonePut(ctx, sharedID, 400); err != nil {
		t.Fatal(err)
	}

	result, err := instance.service.CollectTombstones(ctx, CollectTombstonesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Tombstones) != 1 {
		t.Fatalf("ID collision must collapse to one entry: %+v", result.Tombstones)
	}
	entry := result.Tombstones[0]
	if entry.TargetKind != KindCredential || entry.DeletedAt != 400 {
		t.Fatalf("credential tombstone must win the ID: %+v", entry)
	}
}

func TestCollectTombstonesPagination(t *testing.T) {
	instance := newTestInstance(t, false)
	ctx := context.Background()
	want := make([]string, 0, 5)
	for index := 0; index < 5; index++ {
		id := ids.New()
		want = append(want, id)
		if err := instance.service.engine.syncTombstonePut(ctx, id, KindGroup, int64(100+index)); err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(want)

	var walked []string
	afterID := ""
	for {
		result, err := instance.service.CollectTombstones(ctx, CollectTombstonesRequest{AfterID: afterID, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		for _, tombstone := range result.Tombstones {
			walked = append(walked, tombstone.ID)
		}
		if !result.HasMore {
			break
		}
		afterID = result.NextAfterID
	}
	sort.Strings(walked)
	if len(walked) != len(want) {
		t.Fatalf("pagination lost or duplicated entries: %v vs %v", walked, want)
	}
	for index := range want {
		if walked[index] != want[index] {
			t.Fatalf("page walk mismatch: %v vs %v", walked, want)
		}
	}
}

func putLockedTestCredential(t *testing.T, instance *testInstance, id, secret string) {
	t.Helper()
	putTestCredential(t, instance, id, "收集凭据", "password", secret)
	instance.vault.Lock()
}

func TestCollectCredentialsLocked(t *testing.T) {
	instance := newTestInstance(t, true)
	ctx := context.Background()
	credentialID := ids.New()
	putLockedTestCredential(t, instance, credentialID, "locked-secret")

	result, err := instance.service.CollectCredentials(ctx, CollectCredentialsRequest{RevealSecrets: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Credentials) != 1 {
		t.Fatalf("locked vault must not drop credentials: %+v", result.Credentials)
	}
	entry := result.Credentials[0]
	if entry.ID != credentialID || entry.Name != "收集凭据" || entry.Kind != "password" {
		t.Fatalf("metadata must stay readable: %+v", entry)
	}
	if entry.SecretState != CollectSecretLocked || entry.Secret != nil {
		t.Fatalf("locked outcome must be explicit: %+v", entry)
	}
	requireNoSecretLeak(t, result, "locked-secret")
}

func requireNoSecretLeak(t *testing.T, result any, secret string) {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("secret material leaked into collect response: %s", encoded)
	}
}

func TestCollectCredentialsUnavailableWithoutVaultUnlock(t *testing.T) {
	instance := newTestInstance(t, false)
	ctx := context.Background()
	credentialID := ids.New()
	if _, err := instance.db.CredentialPut(ctx, store.CredentialInput{
		ID: credentialID, Name: "未初始化凭据", Kind: "password", Nonce: []byte("n"), Blob: []byte("b"), KEKHint: "master:0",
	}); err != nil {
		t.Fatal(err)
	}

	result, err := instance.service.CollectCredentials(ctx, CollectCredentialsRequest{RevealSecrets: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Credentials) != 1 {
		t.Fatalf("unavailable vault must not drop credentials: %+v", result.Credentials)
	}
	entry := result.Credentials[0]
	if entry.SecretState != CollectSecretUnavailable || entry.Secret != nil {
		t.Fatalf("unavailable outcome must be explicit: %+v", entry)
	}
}

func TestCollectCredentialsUnlockedReveal(t *testing.T) {
	instance := newTestInstance(t, true)
	ctx := context.Background()
	credentialID := ids.New()
	putTestCredential(t, instance, credentialID, "收集凭据", "password", "revealed-secret")

	result, err := instance.service.CollectCredentials(ctx, CollectCredentialsRequest{RevealSecrets: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Credentials) != 1 {
		t.Fatalf("credentials=%+v", result.Credentials)
	}
	entry := result.Credentials[0]
	if entry.SecretState != CollectSecretRevealed || entry.Secret == nil || *entry.Secret != "revealed-secret" {
		t.Fatalf("unlocked reveal must carry the secret: %+v", entry)
	}
}

func TestCollectCredentialsWithheldWithoutRevealRequest(t *testing.T) {
	instance := newTestInstance(t, true)
	ctx := context.Background()
	credentialID := ids.New()
	putTestCredential(t, instance, credentialID, "收集凭据", "password", "withheld-secret")

	result, err := instance.service.CollectCredentials(ctx, CollectCredentialsRequest{RevealSecrets: false})
	if err != nil {
		t.Fatal(err)
	}
	entry := result.Credentials[0]
	if entry.SecretState != CollectSecretWithheld || entry.Secret != nil {
		t.Fatalf("metadata-only collect must withhold the secret: %+v", entry)
	}
	requireNoSecretLeak(t, result, "withheld-secret")
}

func TestCollectCredentialsDecryptFailureIsExplicit(t *testing.T) {
	instance := newTestInstance(t, true)
	ctx := context.Background()
	credentialID := ids.New()
	putTestCredential(t, instance, credentialID, "收集凭据", "password", "tampered-secret")
	if _, err := instance.db.DB().ExecContext(ctx, "UPDATE credential SET blob=? WHERE id=?", []byte("tampered"), credentialID); err != nil {
		t.Fatal(err)
	}

	result, err := instance.service.CollectCredentials(ctx, CollectCredentialsRequest{RevealSecrets: true})
	if err != nil {
		t.Fatal(err)
	}
	entry := result.Credentials[0]
	if entry.SecretState != CollectSecretError || entry.Secret != nil {
		t.Fatalf("decrypt failure must be explicit, not dropped: %+v", entry)
	}
	requireNoSecretLeak(t, result, "tampered-secret")
}

func TestCollectConcurrent(t *testing.T) {
	instance := newTestInstance(t, true)
	ctx := context.Background()
	for index := 0; index < 4; index++ {
		putTestCredential(t, instance, ids.New(), "并发凭据", "password", "concurrent-secret")
		putTestAsset(t, instance, store.AssetRow{ID: ids.New(), Kind: "ssh", Name: "并发资产", OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 100})
		if err := instance.service.engine.syncTombstonePut(ctx, ids.New(), KindGroup, int64(100+index)); err != nil {
			t.Fatal(err)
		}
	}

	var wg stdsync.WaitGroup
	errs := make([]error, 24)
	for worker := 0; worker < 24; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			switch worker % 3 {
			case 0:
				_, errs[worker] = instance.service.CollectAssets(ctx, CollectAssetsRequest{IncludeDeleted: true})
			case 1:
				_, errs[worker] = instance.service.CollectTombstones(ctx, CollectTombstonesRequest{})
			default:
				_, errs[worker] = instance.service.CollectCredentials(ctx, CollectCredentialsRequest{RevealSecrets: true})
			}
		}(worker)
	}
	wg.Wait()
	for worker, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", worker, err)
		}
	}
}

func TestCollectCommandsOfflineAnonymous(t *testing.T) {
	instance := newTestInstance(t, true)
	ctx := context.Background()
	putTestAsset(t, instance, store.AssetRow{ID: ids.New(), Kind: "ssh", Name: "匿名资产", OptionsJSON: "{}", CreatedAt: 1, UpdatedAt: 100})

	dispatcher := ipc.NewDispatcher()
	if err := instance.service.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	if err := instance.service.RegisterCommands(dispatcher); err == nil {
		t.Fatal("duplicate registration must fail")
	}

	dispatch := func(command, args string) ipc.Response {
		return dispatcher.Dispatch(ctx, ipc.Request{Command: command, Args: json.RawMessage(args)}, ipc.Environment{})
	}
	response := dispatch(CommandCollectAssets, `{"args":{"includeDeleted":false}}`)
	if !response.OK {
		t.Fatalf("anonymous collect assets rejected: %+v", response.Error)
	}
	var assets CollectAssetsResult
	if err := json.Unmarshal(response.Data, &assets); err != nil {
		t.Fatal(err)
	}
	if len(assets.Assets) != 1 || assets.Assets[0].Name != "匿名资产" {
		t.Fatalf("assets=%+v", assets.Assets)
	}

	response = dispatch(CommandCollectTombstones, `{"args":{}}`)
	if !response.OK {
		t.Fatalf("anonymous collect tombstones rejected: %+v", response.Error)
	}
	response = dispatch(CommandCollectCredentials, `{"args":{"revealSecrets":false}}`)
	if !response.OK {
		t.Fatalf("anonymous collect credentials rejected: %+v", response.Error)
	}
	var credentials CollectCredentialsResult
	if err := json.Unmarshal(response.Data, &credentials); err != nil {
		t.Fatal(err)
	}
	if len(credentials.Credentials) != 0 {
		t.Fatalf("credentials=%+v", credentials.Credentials)
	}

	response = dispatch(CommandCollectAssets, `{"args":`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("malformed envelope = %+v, want bad_param", response)
	}
}
