package sync

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func dispatchJSON(t *testing.T, dispatcher *ipc.Dispatcher, command string, args string) ipc.Response {
	t.Helper()
	response := dispatcher.Dispatch(context.Background(), ipc.Request{Command: command, Args: json.RawMessage(args)}, ipc.Environment{})
	return response
}

func TestBundleFileReadWriteRoundTripThroughIPC(t *testing.T) {
	ctx := context.Background()
	source := newTestInstance(t, true)
	groupID := ids.New()
	assetID := ids.New()
	credentialID := ids.New()
	putTestGroup(t, source, groupID, nil, "源分组")
	putTestCredential(t, source, credentialID, "源凭据", "password", "s3cret-中文")
	putTestAsset(t, source, store.AssetRow{
		ID: assetID, GroupID: &groupID, Kind: "ssh", Name: "源资产",
		Host: testPtr("192.0.2.10"), Port: testPtr(int32(22)), Username: testPtr("root"),
		AuthKind: testPtr("password"), CredID: &credentialID, UpdatedAt: 10,
	})

	dispatcher := ipc.NewDispatcher()
	if err := source.service.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}

	exported := dispatchJSON(t, dispatcher, CommandExport, `{"args":{"assetIds":["`+assetID+`"],"withCreds":true}}`)
	if !exported.OK {
		t.Fatalf("export response=%+v", exported)
	}
	var bundle Bundle
	if err := json.Unmarshal(exported.Data, &bundle); err != nil {
		t.Fatal(err)
	}
	if len(bundle.Assets) != 1 || len(bundle.Credentials) != 1 || len(bundle.Groups) != 1 {
		t.Fatalf("unexpected bundle shape: %+v", bundle)
	}
	encoded, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "nexterm-assets.json")
	write := dispatchJSON(t, dispatcher, CommandBundleWrite, `{"args":{"path":`+jsonString(path)+`,"content":`+jsonString(string(encoded))+`}}`)
	if !write.OK {
		t.Fatalf("bundle write response=%+v", write)
	}
	read := dispatchJSON(t, dispatcher, CommandBundleRead, `{"args":{"path":`+jsonString(path)+`}}`)
	if !read.OK {
		t.Fatalf("bundle read response=%+v", read)
	}
	var text string
	if err := json.Unmarshal(read.Data, &text); err != nil || text != string(encoded) {
		t.Fatalf("bundle read did not round trip: err=%v", err)
	}

	target := newTestInstance(t, true)
	targetDispatcher := ipc.NewDispatcher()
	if err := target.service.RegisterCommands(targetDispatcher); err != nil {
		t.Fatal(err)
	}
	imported := dispatchJSON(t, targetDispatcher, CommandImport, `{"args":{"bundle":`+string(encoded)+`,"force":false}}`)
	if !imported.OK {
		t.Fatalf("import response=%+v", imported)
	}
	var report ImportReport
	if err := json.Unmarshal(imported.Data, &report); err != nil {
		t.Fatal(err)
	}
	if report.GroupsCreated != 1 || report.AssetsCreated != 1 || report.CredsCreated != 1 || report.Refused != 0 {
		t.Fatalf("import report=%+v", report)
	}
	asset, err := target.db.AssetGet(ctx, assetID)
	if err != nil || asset.CredID == nil || *asset.CredID != credentialID {
		t.Fatalf("imported asset=%+v err=%v", asset, err)
	}
}

func jsonString(s string) string {
	encoded, _ := json.Marshal(s)
	return string(encoded)
}

func TestBundleExportCredentialExclusionLeavesDanglingReference(t *testing.T) {
	source := newTestInstance(t, true)
	assetID := ids.New()
	credentialID := ids.New()
	putTestCredential(t, source, credentialID, "凭据", "password", "top-secret")
	putTestAsset(t, source, store.AssetRow{
		ID: assetID, Kind: "ssh", Name: "资产", AuthKind: testPtr("password"), CredID: &credentialID, UpdatedAt: 10,
	})

	dispatcher := ipc.NewDispatcher()
	if err := source.service.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	exported := dispatchJSON(t, dispatcher, CommandExport, `{"args":{"assetIds":["`+assetID+`"],"withCreds":false}}`)
	if !exported.OK {
		t.Fatalf("export response=%+v", exported)
	}
	var bundle Bundle
	if err := json.Unmarshal(exported.Data, &bundle); err != nil {
		t.Fatal(err)
	}
	if len(bundle.Credentials) != 0 {
		t.Fatalf("credential-free export carried credentials: %+v", bundle.Credentials)
	}
	if len(bundle.Assets) != 1 || bundle.Assets[0].CredID == nil || *bundle.Assets[0].CredID != credentialID {
		t.Fatalf("credential-free export dropped the reference: %+v", bundle.Assets)
	}

	target := newTestInstance(t, true)
	targetDispatcher := ipc.NewDispatcher()
	if err := target.service.RegisterCommands(targetDispatcher); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	imported := dispatchJSON(t, targetDispatcher, CommandImport, `{"args":{"bundle":`+string(encoded)+`,"force":false}}`)
	if !imported.OK {
		t.Fatalf("import response=%+v", imported)
	}
	var report ImportReport
	if err := json.Unmarshal(imported.Data, &report); err != nil {
		t.Fatal(err)
	}
	if report.AssetsCreated != 1 || report.CredsCreated != 0 {
		t.Fatalf("import report=%+v", report)
	}
	joined := strings.Join(report.Warnings, "\n")
	if !strings.Contains(joined, "凭据") {
		t.Fatalf("expected dangling credential warning, got %v", report.Warnings)
	}
	asset, err := target.db.AssetGet(context.Background(), assetID)
	if err != nil || asset.CredID != nil {
		t.Fatalf("dangling credential reference survived import: %+v err=%v", asset, err)
	}
}

func TestBundleImportRejectsInvalidInput(t *testing.T) {
	target := newTestInstance(t, false)
	dispatcher := ipc.NewDispatcher()
	if err := target.service.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}

	response := dispatchJSON(t, dispatcher, CommandImport, `{"args":{"bundle":{"protocol":99,"groups":[],"assets":[],"creds":[]},"force":false}}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("protocol gate response=%+v", response)
	}

	response = dispatchJSON(t, dispatcher, CommandImport, `{"args":{"bundle":"not-an-object","force":false}}`)
	if response.OK {
		t.Fatalf("malformed bundle accepted: %+v", response)
	}

	response = dispatchJSON(t, dispatcher, CommandImport, `{"args":{"bundle":{"protocol":1,"groups":[],"assets":[],"creds":[{"id":"c1","name":"n","kind":"password","secret":"x"}]},"force":false}}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeVaultLocked {
		t.Fatalf("locked vault gate response=%+v", response)
	}
	if _, err := target.db.CredentialGetRow(context.Background(), "c1"); !isNotFound(err) {
		t.Fatalf("rejected import wrote credential: %v", err)
	}
}

func TestBundleFileCommandsValidateInputAndRequireDesktop(t *testing.T) {
	server := newTestInstance(t, true)
	serverService := New(server.db, server.vault)
	serverDispatcher := ipc.NewDispatcher()
	if err := serverService.RegisterCommands(serverDispatcher); err != nil {
		t.Fatal(err)
	}
	response := dispatchJSON(t, serverDispatcher, CommandBundleRead, `{"args":{"path":"/tmp/x.json"}}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("server bundle read response=%+v", response)
	}
	response = dispatchJSON(t, serverDispatcher, CommandBundleWrite, `{"args":{"path":"/tmp/x.json","content":"{}"}}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("server bundle write response=%+v", response)
	}

	desktop := newTestInstance(t, true)
	dispatcher := ipc.NewDispatcher()
	if err := desktop.service.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	response = dispatchJSON(t, dispatcher, CommandBundleRead, `{"args":{"path":"  "}}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("empty path response=%+v", response)
	}
	response = dispatchJSON(t, dispatcher, CommandBundleRead, `{"args":{"path":`+jsonString(filepath.Join(t.TempDir(), "missing.json"))+`}}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("missing file response=%+v", response)
	}

	path := filepath.Join(t.TempDir(), "bundle.json")
	oversize := strings.Repeat("x", maxBundleFileBytes+1)
	response = dispatchJSON(t, dispatcher, CommandBundleWrite, `{"args":{"path":`+jsonString(path)+`,"content":`+jsonString(oversize)+`}}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("oversize write response=%+v", response)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("oversize write created a file: %v", err)
	}

	peer, err := desktop.service.PeerDispatcher()
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range peer.Commands() {
		if command == CommandBundleRead || command == CommandBundleWrite {
			t.Fatalf("peer surface must not expose %s", command)
		}
	}
}
