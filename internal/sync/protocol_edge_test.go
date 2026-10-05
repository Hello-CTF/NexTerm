package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func TestImportMissingGroupParentAndEmptyReferences(t *testing.T) {
	ctx := context.Background()
	instance := newTestInstance(t, false)
	missingParent, groupID, assetID := ids.New(), ids.New(), ids.New()
	emptyGroup, emptyCredential := "", ""
	report, err := instance.service.Import(ctx, ImportRequest{Bundle: Bundle{
		Protocol: ProtocolVersion,
		Groups:   []GroupPayload{{ID: groupID, ParentID: &missingParent, Name: "orphan"}},
		Assets: []AssetPayload{{
			ID: assetID, Name: "empty references", Kind: "ssh", GroupID: &emptyGroup, CredID: &emptyCredential,
		}},
	}})
	if err != nil || report.GroupsCreated != 1 || report.AssetsCreated != 1 || len(report.Warnings) != 1 {
		t.Fatalf("unexpected report=%+v err=%v", report, err)
	}
	group, _ := instance.db.GroupGet(ctx, groupID)
	asset, _ := instance.db.AssetGet(ctx, assetID)
	if group.ParentID != nil || asset.GroupID != nil || asset.CredID != nil {
		t.Fatalf("references not normalized: group=%+v asset=%+v", group, asset)
	}
}

func TestGroupDepthGuardTerminates(t *testing.T) {
	groups := make([]GroupPayload, 70)
	for i := range groups {
		groups[i].ID = fmt.Sprintf("%026d", 1000+len(groups)-1-i)
		groups[i].Name = "deep"
		if i > 0 {
			groups[i].ParentID = &groups[i-1].ID
		}
	}
	ordered, warnings := orderGroups(groups)
	if len(ordered) != len(groups) || len(warnings) != 1 {
		t.Fatalf("deep topology ordered=%d warnings=%v", len(ordered), warnings)
	}
}

func TestClientRejectsMalformedRemoteResponses(t *testing.T) {
	tests := []struct {
		name string
		body string
		code ipc.Code
	}{
		{name: "non-json", body: `<html>not nexterm</html>`, code: ipc.CodeInternal},
		{name: "missing-data", body: `{"ok":true}`, code: ipc.CodeInternal},
		{name: "wrong-shape", body: `{"ok":true,"data":{"protocol":"one"}}`, code: ipc.CodeInternal},
		{name: "remote-code", body: `{"ok":false,"error":{"code":"unsupported","message":"future"}}`, code: ipc.CodeUnsupported},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(test.body))
			}))
			t.Cleanup(server.Close)
			instance := newTestInstance(t, false)
			token := "token"
			_, _ = instance.service.LinkSet(context.Background(), LinkPatch{URL: server.URL, Token: &token})
			_, err := NewClient(instance.service).RemoteDigest(context.Background())
			requireCode(t, err, test.code)
		})
	}
}

func setTestOrigin(t *testing.T, instance *testInstance, origin string) {
	t.Helper()
	if err := instance.db.SettingSet(context.Background(), settingOrigin, origin); err != nil {
		t.Fatal(err)
	}
}

func TestEqualRevisionResolvedByOriginLexicalOrder(t *testing.T) {
	ctx := context.Background()
	source := newTestInstance(t, false)
	setTestOrigin(t, source, "origin-b")
	assetID := ids.New()
	putTestAsset(t, source, store.AssetRow{ID: assetID, Name: "remote-wins", UpdatedAt: 500})
	bundle, err := source.service.Export(ctx, ExportRequest{AssetIDs: []string{assetID}})
	if err != nil {
		t.Fatal(err)
	}

	target := newTestInstance(t, false)
	setTestOrigin(t, target, "origin-a")
	putTestAsset(t, target, store.AssetRow{ID: assetID, Name: "local-loses", UpdatedAt: 500})
	report, err := target.service.Import(ctx, ImportRequest{Bundle: bundle})
	if err != nil || report.AssetsUpdated != 1 || report.SkippedNewer != 0 {
		t.Fatalf("greater remote origin should win: report=%+v err=%v", report, err)
	}
	local, _ := target.db.AssetGet(ctx, assetID)
	if local.Name != "remote-wins" {
		t.Fatalf("greater remote origin lost: %+v", local)
	}
	if !strings.Contains(strings.Join(report.Warnings, "\n"), "Origin") {
		t.Fatalf("tie-break accept should warn: %v", report.Warnings)
	}

	dominant := newTestInstance(t, false)
	setTestOrigin(t, dominant, "origin-c")
	putTestAsset(t, dominant, store.AssetRow{ID: assetID, Name: "local-kept", UpdatedAt: 500})
	report, err = dominant.service.Import(ctx, ImportRequest{Bundle: bundle})
	if err != nil || report.SkippedNewer != 1 || report.AssetsUpdated != 0 {
		t.Fatalf("greater local origin should keep local: report=%+v err=%v", report, err)
	}
	local, _ = dominant.db.AssetGet(ctx, assetID)
	if local.Name != "local-kept" {
		t.Fatalf("greater local origin lost: %+v", local)
	}
	if len(report.SkippedNewerDetails) != 1 {
		t.Fatalf("skipped details missing: %+v", report)
	}
	detail := report.SkippedNewerDetails[0]
	if detail.Kind != "asset" || detail.ID != assetID || !detail.EqualRevision || detail.LocalRevision != 500 || detail.RemoteRevision != 500 {
		t.Fatalf("unexpected skip detail: %+v", detail)
	}

	self := newTestInstance(t, false)
	setTestOrigin(t, self, "origin-b")
	putTestAsset(t, self, store.AssetRow{ID: assetID, Name: "self-old", UpdatedAt: 500})
	report, err = self.service.Import(ctx, ImportRequest{Bundle: bundle})
	if err != nil || report.AssetsUpdated != 1 || len(report.Warnings) != 0 {
		t.Fatalf("same origin re-import should accept silently: report=%+v err=%v", report, err)
	}
	local, _ = self.db.AssetGet(ctx, assetID)
	if local.Name != "remote-wins" {
		t.Fatalf("same origin re-import lost data: %+v", local)
	}
}

func TestClockSkewFollowsRevisionWithoutCorrection(t *testing.T) {
	ctx := context.Background()
	target := newTestInstance(t, false)
	assetID := ids.New()
	future := ids.NowMS() + 48*60*60*1000
	remoteBundle := Bundle{
		Protocol: ProtocolVersion, Origin: "skewed-peer",
		Assets: []AssetPayload{{ID: assetID, Kind: "ssh", Name: "remote-future", OptionsJSON: `{}`, UpdatedAt: future}},
	}

	putTestAsset(t, target, store.AssetRow{ID: assetID, Name: "local-past", UpdatedAt: 100})
	report, err := target.service.Import(ctx, ImportRequest{Bundle: remoteBundle})
	if err != nil || report.AssetsUpdated != 1 {
		t.Fatalf("future remote revision should win: report=%+v err=%v", report, err)
	}
	local, _ := target.db.AssetGet(ctx, assetID)
	if local.Name != "remote-future" {
		t.Fatalf("future remote revision lost: %+v", local)
	}

	putTestAsset(t, target, store.AssetRow{ID: assetID, Name: "local-future", UpdatedAt: future + 1000})
	report, err = target.service.Import(ctx, ImportRequest{Bundle: remoteBundle})
	if err != nil || report.SkippedNewer != 1 || report.AssetsUpdated != 0 {
		t.Fatalf("future local revision should protect local: report=%+v err=%v", report, err)
	}
	if len(report.SkippedNewerDetails) != 1 ||
		report.SkippedNewerDetails[0].LocalRevision != future+1000 || report.SkippedNewerDetails[0].RemoteRevision != future {
		t.Fatalf("skew skip detail mismatch: %+v", report.SkippedNewerDetails)
	}
	local, _ = target.db.AssetGet(ctx, assetID)
	if local.Name != "local-future" {
		t.Fatalf("future local revision was overwritten: %+v", local)
	}
}

func TestCredentialTombstoneRoundTripAndAudit(t *testing.T) {
	ctx := context.Background()
	source := newTestInstance(t, true)
	target := newTestInstance(t, true)
	credentialID := ids.New()
	putTestCredential(t, source, credentialID, "shared", "password", "source-secret")
	putTestCredential(t, target, credentialID, "shared", "password", "target-secret")

	if err := source.db.CredentialDelete(ctx, credentialID); err != nil {
		t.Fatal(err)
	}
	bundle, err := source.service.Export(ctx, ExportRequest{})
	if err != nil || len(bundle.CredTombstones) != 1 {
		t.Fatalf("export after real delete bundle=%+v err=%v", bundle, err)
	}
	tombstone := bundle.CredTombstones[0]
	if tombstone.ID != credentialID || tombstone.DeletedAt <= 0 {
		t.Fatalf("exported tombstone=%+v", tombstone)
	}

	report, err := target.service.Import(ctx, ImportRequest{Bundle: bundle})
	if err != nil || report.CredsDeleted != 1 {
		t.Fatalf("tombstone import report=%+v err=%v", report, err)
	}
	if _, err := target.db.CredentialGetRow(ctx, credentialID); !isNotFound(err) {
		t.Fatalf("credential survived its tombstone: %v", err)
	}
	if !strings.Contains(strings.Join(report.Warnings, "\n"), "墓碑") {
		t.Fatalf("deletion audit missing from warnings: %v", report.Warnings)
	}

	report, err = target.service.Import(ctx, ImportRequest{Bundle: bundle})
	if err != nil || report.CredsDeleted != 0 || len(report.Warnings) != 0 {
		t.Fatalf("tombstone replay must be a silent no-op: %+v err=%v", report, err)
	}

	stale := Bundle{Protocol: ProtocolVersion, Origin: "legacy-peer",
		Credentials: []CredentialPayload{{ID: credentialID, Name: "shared", Kind: "password", Secret: "stale-secret"}}}
	report, err = target.service.Import(ctx, ImportRequest{Bundle: stale})
	if err != nil || report.SkippedNewer != 1 || report.CredsCreated != 0 || report.CredsUpdated != 0 {
		t.Fatalf("stale credential must not resurrect: %+v err=%v", report, err)
	}
	if len(report.SkippedNewerDetails) != 1 || report.SkippedNewerDetails[0].Kind != "credential" {
		t.Fatalf("credential skip detail missing: %+v", report)
	}
	if _, err := target.db.CredentialGetRow(ctx, credentialID); !isNotFound(err) {
		t.Fatal("stale credential bypassed the tombstone")
	}

	newer := Bundle{Protocol: ProtocolVersion, Origin: "legacy-peer",
		Credentials: []CredentialPayload{{ID: credentialID, Name: "shared", Kind: "password", Secret: "newer-secret", UpdatedAt: tombstone.DeletedAt + 100}}}
	report, err = target.service.Import(ctx, ImportRequest{Bundle: newer})
	if err != nil || report.CredsCreated != 1 {
		t.Fatalf("newer credential should recreate: %+v err=%v", report, err)
	}
	if _, err := target.db.CredentialTombstoneGet(ctx, credentialID); !isNotFound(err) {
		t.Fatalf("tombstone should be cleared after recreation: %v", err)
	}
	row, err := target.db.CredentialGetRow(ctx, credentialID)
	if err != nil {
		t.Fatal(err)
	}
	if plaintext, err := target.vault.DecryptCredentialString(ctx, row); err != nil || plaintext != "newer-secret" {
		t.Fatalf("recreated plaintext=%q err=%v", plaintext, err)
	}

	report, err = target.service.Import(ctx, ImportRequest{Bundle: stale, Force: true})
	if err != nil || report.CredsUpdated != 1 {
		t.Fatalf("force must override the tombstone gate: %+v err=%v", report, err)
	}
	row, _ = target.db.CredentialGetRow(ctx, credentialID)
	if plaintext, _ := target.vault.DecryptCredentialString(ctx, row); plaintext != "stale-secret" {
		t.Fatalf("forced import plaintext=%q", plaintext)
	}
}

func TestCredentialTombstoneSkipsNewerLocalAndWorksLocked(t *testing.T) {
	ctx := context.Background()
	target := newTestInstance(t, true)
	credentialID := ids.New()
	putTestCredential(t, target, credentialID, "local", "password", "local-secret")
	stale := int64(1)
	tombstoneBundle := Bundle{Protocol: ProtocolVersion, Origin: "tombstone-peer",
		CredTombstones: []store.CredentialTombstone{{ID: credentialID, DeletedAt: stale}}}
	report, err := target.service.Import(ctx, ImportRequest{Bundle: tombstoneBundle})
	if err != nil || report.SkippedNewer != 1 || report.CredsDeleted != 0 {
		t.Fatalf("stale tombstone must not delete a newer credential: %+v err=%v", report, err)
	}
	if len(report.SkippedNewerDetails) != 1 || report.SkippedNewerDetails[0].Kind != "credential" {
		t.Fatalf("credential skip detail missing: %+v", report)
	}
	if _, err := target.db.CredentialGetRow(ctx, credentialID); err != nil {
		t.Fatalf("newer credential was deleted by a stale tombstone: %v", err)
	}

	locked := newTestInstance(t, true)
	putTestCredential(t, locked, credentialID, "local", "password", "locked-secret")
	locked.vault.Lock()
	report, err = locked.service.Import(ctx, ImportRequest{Bundle: Bundle{
		Protocol: ProtocolVersion, Origin: "tombstone-peer",
		CredTombstones: []store.CredentialTombstone{{ID: credentialID, DeletedAt: ids.NowMS() + 1000}},
	}})
	if err != nil {
		t.Fatalf("tombstone-only bundle must not require an unlocked vault: %v", err)
	}
	if report.CredsDeleted != 1 {
		t.Fatalf("locked-vault tombstone not applied: %+v", report)
	}
	if _, err := locked.db.CredentialGetRow(ctx, credentialID); !isNotFound(err) {
		t.Fatalf("locked-vault tombstone did not delete: %v", err)
	}

	report, err = target.service.Import(ctx, ImportRequest{Bundle: tombstoneBundle, Force: true})
	if err != nil || report.CredsDeleted != 1 {
		t.Fatalf("force must apply the tombstone: %+v err=%v", report, err)
	}
	if _, err := target.db.CredentialGetRow(ctx, credentialID); !isNotFound(err) {
		t.Fatalf("forced tombstone did not delete: %v", err)
	}
}

type legacyCredentialPayload struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Secret string `json:"secret"`
}

type legacyBundle struct {
	Protocol    int                       `json:"protocol"`
	Origin      string                    `json:"origin"`
	ExportedAt  int64                     `json:"exportedAt"`
	Groups      []GroupPayload            `json:"groups"`
	Assets      []AssetPayload            `json:"assets"`
	Credentials []legacyCredentialPayload `json:"creds"`
}

// legacyImportCredentials 复刻 rwig 旧端 importCredentials 的实际行为：
// 没有墓碑分支，对 creds 数组逐项重加密写入（空 Secret 也会覆盖同 ID 现有秘密）。
func legacyImportCredentials(t *testing.T, instance *testInstance, credentials []legacyCredentialPayload) {
	t.Helper()
	ctx := context.Background()
	for _, credential := range credentials {
		if strings.TrimSpace(credential.ID) == "" {
			continue
		}
		nonce, blob, err := instance.vault.EncryptCredential(ctx, credential.Secret)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := instance.db.CredentialPut(ctx, store.CredentialInput{
			ID: credential.ID, Name: credential.Name, Kind: credential.Kind,
			Nonce: nonce, Blob: blob, KEKHint: instance.vault.KEKHint(),
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOldPeersIgnoreNewWireFields(t *testing.T) {
	if ProtocolVersion != 1 {
		t.Fatal("ProtocolVersion 必须保持 1")
	}
	modern := Bundle{
		Protocol: ProtocolVersion, Origin: "modern-peer", ExportedAt: 1,
		Credentials:    []CredentialPayload{{ID: ids.New(), Name: "real", Kind: "password", Secret: "real-secret", UpdatedAt: 5}},
		CredTombstones: []store.CredentialTombstone{{ID: ids.New(), DeletedAt: 100}},
		Snippets:       []SnippetPayload{{ID: ids.New(), Name: "snippet", Body: "body", CreatedAt: 1, UpdatedAt: 2}},
	}
	encoded, err := json.Marshal(modern)
	if err != nil {
		t.Fatal(err)
	}
	wire := string(encoded)
	if !strings.Contains(wire, "credTombstones") || !strings.Contains(wire, "snippets") || !strings.Contains(wire, "updatedAt") {
		t.Fatalf("new fields missing on the wire: %s", wire)
	}
	var legacy legacyBundle
	if err := json.Unmarshal(encoded, &legacy); err != nil {
		t.Fatalf("旧对端必须忽略未知字段: %v", err)
	}
	if len(legacy.Credentials) != 1 || legacy.Credentials[0].ID != modern.Credentials[0].ID {
		t.Fatalf("legacy decode dropped credentials: %+v", legacy)
	}

	roundTrip, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Bundle
	if err := json.Unmarshal(roundTrip, &decoded); err != nil {
		t.Fatalf("新对端必须容忍旧 JSON: %v", err)
	}
	if decoded.Credentials[0].UpdatedAt != 0 || len(decoded.CredTombstones) != 0 || len(decoded.Snippets) != 0 {
		t.Fatalf("legacy JSON leaked into new fields: %+v", decoded)
	}
}

func TestLegacyPeerImportKeepsSecretsIntact(t *testing.T) {
	ctx := context.Background()
	existingID, incomingID := ids.New(), ids.New()
	legacyPeer := newTestInstance(t, true)
	putTestCredential(t, legacyPeer, existingID, "keepme", "password", "real-secret")

	modern := Bundle{
		Protocol: ProtocolVersion, Origin: "modern-peer",
		Credentials:    []CredentialPayload{{ID: incomingID, Name: "incoming", Kind: "password", Secret: "incoming-secret", UpdatedAt: 5}},
		CredTombstones: []store.CredentialTombstone{{ID: existingID, DeletedAt: ids.NowMS()}},
	}
	encoded, err := json.Marshal(modern)
	if err != nil {
		t.Fatal(err)
	}
	var legacy legacyBundle
	if err := json.Unmarshal(encoded, &legacy); err != nil {
		t.Fatal(err)
	}
	for _, credential := range legacy.Credentials {
		if credential.ID == existingID {
			t.Fatal("墓碑不得以任何形式进入旧端 creds 数组")
		}
	}

	legacyImportCredentials(t, legacyPeer, legacy.Credentials)

	row, err := legacyPeer.db.CredentialGetRow(ctx, existingID)
	if err != nil {
		t.Fatalf("旧端已有凭据受墓碑影响: %v", err)
	}
	if plaintext, err := legacyPeer.vault.DecryptCredentialString(ctx, row); err != nil || plaintext != "real-secret" {
		t.Fatalf("旧端秘密被覆盖 plaintext=%q err=%v", plaintext, err)
	}
	if _, err := legacyPeer.db.CredentialGetRow(ctx, incomingID); err != nil {
		t.Fatalf("旧端未导入正常凭据项: %v", err)
	}
}

func TestSnippetSyncRoundTrip(t *testing.T) {
	ctx := context.Background()
	source := newTestInstance(t, false)
	groupID, snippetID := ids.New(), ids.New()
	putTestGroup(t, source, groupID, nil, "snippets")
	fixture := Bundle{
		Protocol: ProtocolVersion, Origin: "snippet-fixture",
		Snippets: []SnippetPayload{{ID: snippetID, GroupID: &groupID, Name: "greet", Body: "echo hi", Sort: 3, CreatedAt: 10, UpdatedAt: 100}},
	}
	if _, err := source.service.Import(ctx, ImportRequest{Bundle: fixture}); err != nil {
		t.Fatal(err)
	}

	bundle, err := source.service.Export(ctx, ExportRequest{})
	if err != nil || len(bundle.Snippets) != 1 {
		t.Fatalf("snippet export bundle=%+v err=%v", bundle, err)
	}
	if bundle.Snippets[0].ID != snippetID || bundle.Snippets[0].Body != "echo hi" || bundle.Snippets[0].Sort != 3 {
		t.Fatalf("snippet payload mismatch: %+v", bundle.Snippets[0])
	}
	foundGroup := false
	for _, group := range bundle.Groups {
		if group.ID == groupID {
			foundGroup = true
		}
	}
	if !foundGroup {
		t.Fatalf("snippet group ancestor was not exported: %+v", bundle.Groups)
	}

	target := newTestInstance(t, false)
	report, err := target.service.Import(ctx, ImportRequest{Bundle: bundle})
	if err != nil || report.SnippetsCreated != 1 || report.GroupsCreated != 1 {
		t.Fatalf("snippet import report=%+v err=%v", report, err)
	}
	row, err := target.db.SnippetGet(ctx, snippetID)
	if err != nil || row.Body != "echo hi" || row.GroupID == nil || *row.GroupID != groupID || row.Sort != 3 || row.UpdatedAt != 100 {
		t.Fatalf("imported snippet=%+v err=%v", row, err)
	}

	report, err = target.service.Import(ctx, ImportRequest{Bundle: bundle})
	if err != nil || report.SnippetsUpdated != 1 || report.SnippetsCreated != 0 {
		t.Fatalf("idempotent re-import report=%+v err=%v", report, err)
	}
	snippets, _ := target.db.SnippetList(ctx)
	if len(snippets) != 1 {
		t.Fatalf("repeat import duplicated snippets: %+v", snippets)
	}

	if err := target.db.SnippetUpdate(ctx, snippetID, "greet-local", "echo local"); err != nil {
		t.Fatal(err)
	}
	report, err = target.service.Import(ctx, ImportRequest{Bundle: bundle})
	if err != nil || report.SkippedNewer != 1 || report.SnippetsUpdated != 0 {
		t.Fatalf("newer local snippet should win: %+v err=%v", report, err)
	}
	if len(report.SkippedNewerDetails) != 1 || report.SkippedNewerDetails[0].Kind != "snippet" {
		t.Fatalf("snippet skip detail missing: %+v", report)
	}
	row, _ = target.db.SnippetGet(ctx, snippetID)
	if row.Body != "echo local" {
		t.Fatalf("newer local snippet was overwritten: %+v", row)
	}

	report, err = target.service.Import(ctx, ImportRequest{Bundle: bundle, Force: true})
	if err != nil || report.SnippetsUpdated != 1 {
		t.Fatalf("forced snippet import report=%+v err=%v", report, err)
	}
	row, _ = target.db.SnippetGet(ctx, snippetID)
	if row.Body != "echo hi" {
		t.Fatalf("force did not restore remote snippet: %+v", row)
	}

	newer := fixture
	newer.Snippets = []SnippetPayload{{ID: snippetID, GroupID: &groupID, Name: "greet-v2", Body: "echo v2", Sort: 3, CreatedAt: 10, UpdatedAt: ids.NowMS() + 10000}}
	report, err = target.service.Import(ctx, ImportRequest{Bundle: newer})
	if err != nil || report.SnippetsUpdated != 1 {
		t.Fatalf("newer remote snippet should win: %+v err=%v", report, err)
	}
	row, _ = target.db.SnippetGet(ctx, snippetID)
	if row.Name != "greet-v2" {
		t.Fatalf("newer remote snippet lost: %+v", row)
	}
}

func TestCredentialTombstoneMergeKeepsMaxRevision(t *testing.T) {
	ctx := context.Background()
	target := newTestInstance(t, true)
	credentialID := ids.New()
	if err := target.db.CredentialTombstonePut(ctx, credentialID, 200); err != nil {
		t.Fatal(err)
	}

	olderRemote := Bundle{Protocol: ProtocolVersion, Origin: "peer-a",
		CredTombstones: []store.CredentialTombstone{{ID: credentialID, DeletedAt: 100}}}
	report, err := target.service.Import(ctx, ImportRequest{Bundle: olderRemote})
	if err != nil || report.SkippedNewer != 1 || report.CredsDeleted != 0 {
		t.Fatalf("newer local tombstone must win: %+v err=%v", report, err)
	}
	if len(report.SkippedNewerDetails) != 1 {
		t.Fatalf("skip detail missing: %+v", report)
	}
	detail := report.SkippedNewerDetails[0]
	if detail.Kind != "credential" || detail.LocalRevision != 200 || detail.RemoteRevision != 100 || detail.EqualRevision {
		t.Fatalf("unexpected detail: %+v", detail)
	}
	tombstone, err := target.db.CredentialTombstoneGet(ctx, credentialID)
	if err != nil || tombstone.DeletedAt != 200 {
		t.Fatalf("tombstone revision regressed: %+v err=%v", tombstone, err)
	}

	middle := Bundle{Protocol: ProtocolVersion, Origin: "peer-a",
		Credentials: []CredentialPayload{{ID: credentialID, Name: "shared", Kind: "password", Secret: "mid-secret", UpdatedAt: 150}}}
	report, err = target.service.Import(ctx, ImportRequest{Bundle: middle})
	if err != nil || report.SkippedNewer != 1 || report.CredsCreated != 0 || report.CredsUpdated != 0 {
		t.Fatalf("mid-revision credential must not resurrect: %+v err=%v", report, err)
	}
	if _, err := target.db.CredentialGetRow(ctx, credentialID); !isNotFound(err) {
		t.Fatal("mid-revision credential bypassed the tombstone")
	}

	mixed := Bundle{Protocol: ProtocolVersion, Origin: "peer-a",
		CredTombstones: []store.CredentialTombstone{{ID: credentialID, DeletedAt: 300}, {ID: credentialID, DeletedAt: 250}}}
	report, err = target.service.Import(ctx, ImportRequest{Bundle: mixed})
	if err != nil || report.SkippedNewer != 1 {
		t.Fatalf("out-of-order duplicate tombstones: %+v err=%v", report, err)
	}
	tombstone, _ = target.db.CredentialTombstoneGet(ctx, credentialID)
	if tombstone.DeletedAt != 300 {
		t.Fatalf("out-of-order tombstone regressed revision: %+v", tombstone)
	}

	putTestCredential(t, target, credentialID, "shared", "password", "local-secret")
	report, err = target.service.Import(ctx, ImportRequest{Bundle: olderRemote, Force: true})
	if err != nil || report.CredsDeleted != 1 {
		t.Fatalf("force must delete the live row: %+v err=%v", report, err)
	}
	if _, err := target.db.CredentialGetRow(ctx, credentialID); !isNotFound(err) {
		t.Fatal("force tombstone did not delete the live row")
	}
	tombstone, _ = target.db.CredentialTombstoneGet(ctx, credentialID)
	if tombstone.DeletedAt != 300 {
		t.Fatalf("force must not shrink the persisted revision: %+v", tombstone)
	}
}
