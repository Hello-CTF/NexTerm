package sync

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
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
