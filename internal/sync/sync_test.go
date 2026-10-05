package sync

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/ProbiusOfficial/NexTerm/internal/vault"
)

type testInstance struct {
	db      *store.Store
	vault   *vault.Vault
	service *Service
}

func newTestInstance(t *testing.T, unlocked bool, options ...Option) *testInstance {
	t.Helper()
	ctx := context.Background()
	db, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.AssetEnsureBuiltinLocal(ctx); err != nil {
		t.Fatal(err)
	}
	credentialVault := vault.Load(ctx, db)
	if unlocked {
		if err := credentialVault.InitMaster(ctx, "sync-test-master"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(credentialVault.Lock)
	}
	return &testInstance{db: db, vault: credentialVault, service: New(db, credentialVault, append([]Option{WithMetadata("test", true)}, options...)...)}
}

func testPtr[T any](value T) *T { return &value }

func requireCode(t *testing.T, err error, code ipc.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error code %s", code)
	}
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != code {
		t.Fatalf("expected error code %s, got %v", code, err)
	}
}

func putTestCredential(t *testing.T, instance *testInstance, id, name, kind, secret string) {
	t.Helper()
	nonce, blob, err := instance.vault.EncryptCredential(context.Background(), secret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.db.CredentialPut(context.Background(), store.CredentialInput{
		ID: id, Name: name, Kind: kind, Nonce: nonce, Blob: blob, KEKHint: instance.vault.KEKHint(),
	}); err != nil {
		t.Fatal(err)
	}
}

func putTestGroup(t *testing.T, instance *testInstance, id string, parentID *string, name string) {
	t.Helper()
	if _, err := instance.db.GroupUpsert(context.Background(), id, parentID, name, 0, 1, 1); err != nil {
		t.Fatal(err)
	}
}

func putTestAsset(t *testing.T, instance *testInstance, row store.AssetRow) store.AssetRow {
	t.Helper()
	if row.ID == "" {
		row.ID = ids.New()
	}
	if row.Name == "" {
		row.Name = "asset"
	}
	if row.Kind == "" {
		row.Kind = "ssh"
	}
	if row.OptionsJSON == "" {
		row.OptionsJSON = `{}`
	}
	if _, err := instance.db.AssetUpsert(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	stored, err := instance.db.AssetGet(context.Background(), row.ID)
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

func TestSyntheticGoFixtureRoundTripAndProtocolGate(t *testing.T) {
	ctx := context.Background()
	source := newTestInstance(t, true)
	groupID := ids.New()
	assetID := ids.New()
	credentialID := ids.New()
	fixture := Bundle{
		Protocol: ProtocolVersion, Origin: "go-fixture", ExportedAt: 50,
		Groups: []GroupPayload{{ID: groupID, Name: "fixture group", CreatedAt: 1, UpdatedAt: 2}},
		Assets: []AssetPayload{{
			ID: assetID, GroupID: &groupID, CredID: &credentialID, Kind: "ssh", Name: "fixture 中文",
			Host: testPtr("192.0.2.10"), Port: testPtr(int32(2222)), Username: testPtr("root"),
			AuthKind: testPtr("password"), OptionsJSON: `{"keepalive":30}`, Tags: "fixture",
			Note: "all fields", Sort: 3, CreatedAt: 4, UpdatedAt: 5,
		}},
		Credentials: []CredentialPayload{{ID: credentialID, Name: "fixture secret", Kind: "password", Secret: "s3cret-中文"}},
	}
	encoded, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "builtin") {
		t.Fatal("wire fixture must not contain a builtin field")
	}
	var decoded Bundle
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fixture, decoded) {
		t.Fatalf("fixture round trip changed data\nwant=%+v\ngot=%+v", fixture, decoded)
	}
	report, err := source.service.Import(ctx, ImportRequest{Bundle: decoded})
	if err != nil || report.AssetsCreated != 1 || report.GroupsCreated != 1 || report.CredsCreated != 1 {
		t.Fatalf("fixture import report=%+v err=%v", report, err)
	}
	exported, err := source.service.Export(ctx, ExportRequest{AssetIDs: []string{assetID}, WithCredentials: true})
	if err != nil {
		t.Fatal(err)
	}
	for i := range exported.Credentials {
		exported.Credentials[i].UpdatedAt = 0
	}
	if !reflect.DeepEqual(fixture.Assets[0], exported.Assets[0]) || !reflect.DeepEqual(fixture.Credentials, exported.Credentials) {
		t.Fatalf("Go fixture did not survive import/export\nwant assets=%+v creds=%+v\ngot assets=%+v creds=%+v",
			fixture.Assets, fixture.Credentials, exported.Assets, exported.Credentials)
	}

	lockedTarget := newTestInstance(t, false)
	forged := decoded
	forged.Protocol = ProtocolVersion + 1
	_, err = lockedTarget.service.Import(ctx, ImportRequest{Bundle: forged, Force: true})
	requireCode(t, err, ipc.CodeUnsupported)
	if groups, _ := lockedTarget.db.GroupList(ctx); len(groups) != 0 {
		t.Fatal("unsupported bundle wrote groups before rejection")
	}
	if credentials, _ := lockedTarget.db.CredentialList(ctx); len(credentials) != 0 {
		t.Fatal("unsupported bundle wrote credentials before rejection")
	}
	if _, err := lockedTarget.db.AssetGet(ctx, assetID); !isNotFound(err) {
		t.Fatalf("unsupported bundle wrote asset: %v", err)
	}
}

func TestDigestExportAncestorsTombstonesAndBuiltins(t *testing.T) {
	ctx := context.Background()
	instance := newTestInstance(t, false)
	parentID, childID := ids.New(), ids.New()
	putTestGroup(t, instance, parentID, nil, "parent")
	putTestGroup(t, instance, childID, &parentID, "child")
	live := putTestAsset(t, instance, store.AssetRow{GroupID: &childID, Name: "live", CreatedAt: 1, UpdatedAt: 2})
	deleted := putTestAsset(t, instance, store.AssetRow{Name: "deleted", CreatedAt: 1, UpdatedAt: 2})
	if err := instance.db.AssetDelete(ctx, deleted.ID); err != nil {
		t.Fatal(err)
	}

	digest, err := instance.service.Digest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if digest.Protocol != ProtocolVersion || digest.Origin == "" || len(digest.Assets) != 2 {
		t.Fatalf("unexpected digest %+v", digest)
	}
	seenDeleted := false
	for _, entry := range digest.Assets {
		if entry.ID == store.BuiltinLocalAssetID {
			t.Fatal("digest leaked built-in asset")
		}
		if entry.ID == deleted.ID && entry.DeletedAt != nil {
			seenDeleted = true
		}
	}
	if !seenDeleted {
		t.Fatal("digest omitted tombstone")
	}

	bundle, err := instance.service.Export(ctx, ExportRequest{AssetIDs: []string{
		live.ID, deleted.ID, store.BuiltinLocalAssetID, ids.New(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Assets) != 2 || len(bundle.Groups) != 2 {
		t.Fatalf("unexpected bundle assets=%d groups=%d warnings=%v", len(bundle.Assets), len(bundle.Groups), bundle.Warnings)
	}
	if bundle.Groups[0].ID != parentID || bundle.Groups[1].ID != childID {
		t.Fatalf("ancestors not parent-first: %+v", bundle.Groups)
	}
	if len(bundle.Warnings) != 1 {
		t.Fatalf("missing selected asset should warn: %+v", bundle.Warnings)
	}
	for _, asset := range bundle.Assets {
		if asset.ID == store.BuiltinLocalAssetID {
			t.Fatal("export leaked built-in asset")
		}
		if asset.ID == deleted.ID && asset.DeletedAt == nil {
			t.Fatal("export lost tombstone")
		}
	}
}

func TestImportLWWForceTombstoneAndResurrection(t *testing.T) {
	ctx := context.Background()
	source := newTestInstance(t, false)
	target := newTestInstance(t, false)
	assetID := ids.New()
	putTestAsset(t, source, store.AssetRow{ID: assetID, Name: "source", CreatedAt: 10, UpdatedAt: 100})
	putTestAsset(t, target, store.AssetRow{ID: assetID, Name: "local-newer", CreatedAt: 50, UpdatedAt: 200})

	export := func() Bundle {
		t.Helper()
		bundle, err := source.service.Export(ctx, ExportRequest{AssetIDs: []string{assetID}})
		if err != nil {
			t.Fatal(err)
		}
		return bundle
	}
	report, err := target.service.Import(ctx, ImportRequest{Bundle: export()})
	if err != nil || report.SkippedNewer != 1 || len(report.Warnings) != 1 {
		t.Fatalf("newer local report=%+v err=%v", report, err)
	}
	local, _ := target.db.AssetGet(ctx, assetID)
	if local.Name != "local-newer" {
		t.Fatal("LWW skipped import still wrote asset")
	}

	report, err = target.service.Import(ctx, ImportRequest{Bundle: export(), Force: true})
	if err != nil || report.AssetsUpdated != 1 {
		t.Fatalf("forced report=%+v err=%v", report, err)
	}
	local, _ = target.db.AssetGet(ctx, assetID)
	if local.Name != "source" || local.CreatedAt != 50 {
		t.Fatalf("force lost data or destination creation time: %+v", local)
	}

	sourceRow, _ := source.db.AssetGet(ctx, assetID)
	sourceRow.DeletedAt = testPtr(int64(300))
	sourceRow.UpdatedAt = 300
	putTestAsset(t, source, sourceRow)
	if _, err := target.service.Import(ctx, ImportRequest{Bundle: export()}); err != nil {
		t.Fatal(err)
	}
	local, _ = target.db.AssetGet(ctx, assetID)
	if local.DeletedAt == nil {
		t.Fatal("tombstone did not propagate")
	}
	visible, _ := target.db.AssetList(ctx, false)
	for _, asset := range visible {
		if asset.ID == assetID {
			t.Fatal("tombstoned asset remains visible")
		}
	}

	sourceRow.DeletedAt = nil
	sourceRow.Name = "resurrected"
	sourceRow.UpdatedAt = 400
	putTestAsset(t, source, sourceRow)
	if _, err := target.service.Import(ctx, ImportRequest{Bundle: export()}); err != nil {
		t.Fatal(err)
	}
	local, _ = target.db.AssetGet(ctx, assetID)
	if local.DeletedAt != nil || local.Name != "resurrected" {
		t.Fatalf("live row did not resurrect tombstone: %+v", local)
	}

	setTestOrigin(t, source, "origin-b")
	setTestOrigin(t, target, "origin-a")
	putTestAsset(t, target, store.AssetRow{ID: assetID, Name: "equal-local", CreatedAt: 50, UpdatedAt: 400})
	sourceRow.Name = "equal-incoming"
	putTestAsset(t, source, sourceRow)
	if _, err := target.service.Import(ctx, ImportRequest{Bundle: export()}); err != nil {
		t.Fatal(err)
	}
	local, _ = target.db.AssetGet(ctx, assetID)
	if local.Name != "equal-incoming" {
		t.Fatal("equal revision with greater remote origin should accept incoming row")
	}
}

func TestImportProtectsBuiltinsAndDegradesDanglingReferences(t *testing.T) {
	ctx := context.Background()
	target := newTestInstance(t, false)
	missingGroup, missingCredential := ids.New(), ids.New()
	missingPath := t.TempDir() + "/missing-key"
	assetID := ids.New()
	bundle := Bundle{Protocol: ProtocolVersion, Assets: []AssetPayload{
		{ID: store.BuiltinLocalAssetID, Name: "forged builtin", Kind: "ssh", UpdatedAt: 999},
		{ID: assetID, Name: "dangling", Kind: "ssh", GroupID: &missingGroup, CredID: &missingCredential, KeyPath: &missingPath, UpdatedAt: 1},
	}}
	report, err := target.service.Import(ctx, ImportRequest{Bundle: bundle, Force: true})
	if err != nil || report.Refused != 1 || report.AssetsCreated != 1 || len(report.Warnings) < 4 {
		t.Fatalf("unexpected report %+v err=%v", report, err)
	}
	builtin, _ := target.db.AssetGet(ctx, store.BuiltinLocalAssetID)
	if !builtin.Builtin || builtin.Name != store.BuiltinLocalAssetName {
		t.Fatalf("built-in was overwritten: %+v", builtin)
	}
	asset, _ := target.db.AssetGet(ctx, assetID)
	if asset.GroupID != nil || asset.CredID != nil {
		t.Fatalf("dangling references were not cleared: %+v", asset)
	}

	tombstoneID := ids.New()
	report, err = target.service.Import(ctx, ImportRequest{Bundle: Bundle{Protocol: ProtocolVersion, Assets: []AssetPayload{{
		ID: tombstoneID, Name: "tombstone", Kind: "ssh", KeyPath: &missingPath, DeletedAt: testPtr(int64(1)),
	}}}})
	if err != nil || len(report.Warnings) != 0 {
		t.Fatalf("tombstone received misleading key warning: %+v err=%v", report, err)
	}
}

func TestGroupTopologyOrderingAndCycles(t *testing.T) {
	parentID, childID := ids.New(), ids.New()
	ordered, warnings := orderGroups([]GroupPayload{
		{ID: childID, ParentID: &parentID, Name: "child"},
		{ID: parentID, Name: "parent"},
	})
	if len(warnings) != 0 || len(ordered) != 2 || ordered[0].ID != parentID {
		t.Fatalf("parent-first ordering failed: %+v warnings=%v", ordered, warnings)
	}

	firstID, secondID := ids.New(), ids.New()
	ordered, warnings = orderGroups([]GroupPayload{
		{ID: firstID, ParentID: &secondID, Name: "first"},
		{ID: secondID, ParentID: &firstID, Name: "second"},
	})
	if len(ordered) != 2 || len(warnings) != 1 || (ordered[0].ParentID != nil && ordered[1].ParentID != nil) {
		t.Fatalf("cycle was not safely broken: %+v warnings=%v", ordered, warnings)
	}

	instance := newTestInstance(t, false)
	report, err := instance.service.Import(context.Background(), ImportRequest{Bundle: Bundle{
		Protocol: ProtocolVersion, Groups: ordered,
	}})
	if err != nil || report.GroupsCreated != 2 {
		t.Fatalf("cycle-safe groups did not import: %+v err=%v", report, err)
	}
}

func TestOriginTokenAndLinkSettings(t *testing.T) {
	ctx := context.Background()
	first := newTestInstance(t, false)
	second := newTestInstance(t, false)
	if valid, err := first.service.VerifyToken(ctx, "anything"); err != nil || valid {
		t.Fatalf("verification without token valid=%v err=%v", valid, err)
	}
	if _, found, _ := first.db.SettingGet(ctx, settingToken); found {
		t.Fatal("verification generated a token")
	}
	origin, err := first.service.Origin(ctx)
	if err != nil || len(origin) != 8 {
		t.Fatalf("origin=%q err=%v", origin, err)
	}
	if again, _ := first.service.Origin(ctx); again != origin {
		t.Fatal("origin is not stable")
	}
	token, err := first.service.Token(ctx)
	if err != nil || len(token) != 43 {
		t.Fatalf("token=%q err=%v", token, err)
	}
	if again, _ := first.service.Token(ctx); again != token {
		t.Fatal("token is not stable")
	}
	otherToken, _ := second.service.Token(ctx)
	if token == otherToken {
		t.Fatal("tokens must be per-instance")
	}
	for candidate, want := range map[string]bool{token: true, otherToken: false, "": false, "wrong": false} {
		if valid, err := first.service.VerifyToken(ctx, candidate); err != nil || valid != want {
			t.Fatalf("verify %q valid=%v want=%v err=%v", candidate, valid, want, err)
		}
	}
	rotated, err := first.service.RotateToken(ctx)
	if err != nil || rotated == token {
		t.Fatalf("rotation token=%q old=%q err=%v", rotated, token, err)
	}
	if valid, _ := first.service.VerifyToken(ctx, token); valid {
		t.Fatal("old token survived rotation")
	}
	if valid, _ := first.service.VerifyToken(ctx, rotated); !valid {
		t.Fatal("new token rejected")
	}

	insecure := true
	link, err := first.service.LinkSet(ctx, LinkPatch{
		URL: " https://example.com/ ", TokenKind: TokenKindBox, Token: &token, Insecure: &insecure,
	})
	if err != nil || !link.IsConfigured() || link.URL != "https://example.com/" {
		t.Fatalf("link=%+v err=%v", link, err)
	}
	link, err = first.service.LinkSet(ctx, LinkPatch{URL: "https://new.example.com", TokenKind: "unknown"})
	if err != nil || link.Token != token || !link.Insecure || link.TokenKind != TokenKindServer {
		t.Fatalf("link patch did not preserve omitted fields: %+v err=%v", link, err)
	}
	empty := ""
	link, err = first.service.LinkSet(ctx, LinkPatch{URL: link.URL, Token: &empty})
	if err != nil || link.IsConfigured() {
		t.Fatalf("cleared token still configured: %+v err=%v", link, err)
	}
}
