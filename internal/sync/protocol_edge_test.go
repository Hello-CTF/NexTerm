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
	target := newTestInstance(t, true)
	credentialID := ids.New()
	putTestCredential(t, target, credentialID, "local", "password", "local-secret")
	target.vault.Lock()

	deletedAt := ids.NowMS() + 1000
	tombstones := []CredentialPayload{{ID: credentialID, Name: "local", Kind: "password", DeletedAt: &deletedAt}}
	report, err := target.service.Import(ctx, ImportRequest{Bundle: Bundle{
		Protocol: ProtocolVersion, Origin: "tombstone-peer", Credentials: tombstones,
	}})
	if err != nil {
		t.Fatalf("tombstone-only bundle must not require an unlocked vault: %v", err)
	}
	if report.CredsDeleted != 1 {
		t.Fatalf("tombstone was not applied: %+v", report)
	}
	if _, err := target.db.CredentialGetRow(ctx, credentialID); !isNotFound(err) {
		t.Fatalf("credential survived its tombstone: %v", err)
	}
	if !strings.Contains(strings.Join(report.Warnings, "\n"), "墓碑") {
		t.Fatalf("deletion audit missing from warnings: %v", report.Warnings)
	}

	report, err = target.service.Import(ctx, ImportRequest{Bundle: Bundle{
		Protocol: ProtocolVersion, Origin: "tombstone-peer", Credentials: tombstones,
	}})
	if err != nil || report.CredsDeleted != 0 || len(report.Warnings) != 0 {
		t.Fatalf("tombstone replay must be a silent no-op: %+v err=%v", report, err)
	}

	newer := newTestInstance(t, true)
	putTestCredential(t, newer, credentialID, "local", "password", "local-secret")
	stale := int64(1)
	staleBundle := Bundle{Protocol: ProtocolVersion, Origin: "tombstone-peer",
		Credentials: []CredentialPayload{{ID: credentialID, DeletedAt: &stale}}}
	report, err = newer.service.Import(ctx, ImportRequest{Bundle: staleBundle})
	if err != nil || report.SkippedNewer != 1 || report.CredsDeleted != 0 {
		t.Fatalf("stale tombstone must not delete a newer credential: %+v err=%v", report, err)
	}
	if len(report.SkippedNewerDetails) != 1 || report.SkippedNewerDetails[0].Kind != "credential" {
		t.Fatalf("credential skip detail missing: %+v", report)
	}
	if _, err := newer.db.CredentialGetRow(ctx, credentialID); err != nil {
		t.Fatalf("newer credential was deleted by a stale tombstone: %v", err)
	}

	report, err = newer.service.Import(ctx, ImportRequest{Bundle: staleBundle, Force: true})
	if err != nil || report.CredsDeleted != 1 {
		t.Fatalf("force must apply the tombstone: %+v err=%v", report, err)
	}
	if _, err := newer.db.CredentialGetRow(ctx, credentialID); !isNotFound(err) {
		t.Fatalf("forced tombstone did not delete: %v", err)
	}
}

func TestOldPeersIgnoreNewWireFields(t *testing.T) {
	if ProtocolVersion != 1 {
		t.Fatal("ProtocolVersion 必须保持 1")
	}
	type legacyCredential struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Kind   string `json:"kind"`
		Secret string `json:"secret"`
	}
	type legacyBundle struct {
		Protocol    int                `json:"protocol"`
		Origin      string             `json:"origin"`
		ExportedAt  int64              `json:"exportedAt"`
		Groups      []GroupPayload     `json:"groups"`
		Assets      []AssetPayload     `json:"assets"`
		Credentials []legacyCredential `json:"creds"`
	}

	deletedAt := int64(100)
	modern := Bundle{
		Protocol: ProtocolVersion, Origin: "modern-peer", ExportedAt: 1,
		Credentials: []CredentialPayload{{ID: ids.New(), Name: "gone", Kind: "password", DeletedAt: &deletedAt}},
		Snippets:    []SnippetPayload{{ID: ids.New(), Name: "snippet", Body: "body", CreatedAt: 1, UpdatedAt: 2}},
	}
	encoded, err := json.Marshal(modern)
	if err != nil {
		t.Fatal(err)
	}
	wire := string(encoded)
	if !strings.Contains(wire, "deletedAt") || !strings.Contains(wire, "snippets") {
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
	if decoded.Credentials[0].DeletedAt != nil || len(decoded.Snippets) != 0 {
		t.Fatalf("legacy JSON leaked into new fields: %+v", decoded)
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
