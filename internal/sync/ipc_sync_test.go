package sync

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/app"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

var _ app.TokenStore = (*Service)(nil)
var _ app.Component = (*Service)(nil)

func TestCommandRegistrationPeerSurfaceAndLifecycle(t *testing.T) {
	ctx := context.Background()
	instance := newTestInstance(t, false)
	server := New(instance.db, instance.vault)
	desktop := New(instance.db, instance.vault, WithMetadata("test", true))

	if err := desktop.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := instance.db.SettingGet(ctx, settingToken); found {
		t.Fatal("desktop lifecycle generated a server token")
	}
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := instance.db.SettingGet(ctx, settingToken); !found {
		t.Fatal("server lifecycle did not ensure token")
	}
	if err := server.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}

	dispatcher := ipc.NewDispatcher()
	if err := server.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	wantCommands := []string{
		CommandBundleRead, CommandBundleWrite, CommandDigest, CommandExport, CommandImport, CommandLinkGet, CommandLinkSet, CommandOrigin,
		CommandPull, CommandPush, CommandRemoteDigest, CommandToken, CommandTokenIssue, CommandTokenList, CommandTokenRevoke, CommandTokenRotate,
	}
	if got := dispatcher.Commands(); !reflect.DeepEqual(got, wantCommands) {
		t.Fatalf("full commands=%v want=%v", got, wantCommands)
	}
	peer, err := server.PeerDispatcher()
	if err != nil {
		t.Fatal(err)
	}
	wantPeer := []string{CommandDigest, CommandExport, CommandImport}
	if got := peer.Commands(); !reflect.DeepEqual(got, wantPeer) {
		t.Fatalf("peer commands=%v want=%v", got, wantPeer)
	}

	response := dispatcher.Dispatch(ctx, ipc.Request{Command: CommandRemoteDigest}, ipc.Environment{})
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("server remote digest response=%+v", response)
	}
	response = dispatcher.Dispatch(ctx, ipc.Request{Command: CommandExport, Args: json.RawMessage(`{"args":{"assetIds":[]}}`)}, ipc.Environment{})
	if !response.OK {
		t.Fatalf("nested export response=%+v", response)
	}
	var bundle Bundle
	if err := json.Unmarshal(response.Data, &bundle); err != nil || bundle.Protocol != ProtocolVersion {
		t.Fatalf("nested export bundle=%+v err=%v", bundle, err)
	}
	response = dispatcher.Dispatch(ctx, ipc.Request{Command: CommandToken}, ipc.Environment{})
	var token string
	if !response.OK || json.Unmarshal(response.Data, &token) != nil || token == "" {
		t.Fatalf("server token response=%+v", response)
	}
	response = dispatcher.Dispatch(ctx, ipc.Request{Command: CommandTokenRotate}, ipc.Environment{})
	var rotated string
	if !response.OK || json.Unmarshal(response.Data, &rotated) != nil || rotated == "" || rotated == token {
		t.Fatalf("server rotation response=%+v", response)
	}
	if valid, _ := server.VerifyToken(ctx, token); valid {
		t.Fatal("IPC token rotation kept old token valid")
	}

	desktopDispatcher := ipc.NewDispatcher()
	if err := desktop.RegisterCommands(desktopDispatcher); err != nil {
		t.Fatal(err)
	}
	response = desktopDispatcher.Dispatch(ctx, ipc.Request{Command: CommandToken}, ipc.Environment{})
	if !response.OK || string(response.Data) != "null" {
		t.Fatalf("desktop token response=%+v data=%s", response, response.Data)
	}
	response = desktopDispatcher.Dispatch(ctx, ipc.Request{Command: CommandLinkSet, Args: json.RawMessage(`{"args":{"url":"http://localhost","token":"abc"}}`)}, ipc.Environment{})
	if !response.OK || !strings.Contains(string(response.Data), `"verifiedAt":0`) || !strings.Contains(string(response.Data), `"lastError":""`) {
		t.Fatalf("link DTO response=%+v data=%s", response, response.Data)
	}
	emptyToken := ""
	if _, err := desktop.LinkSet(ctx, LinkPatch{URL: "", Token: &emptyToken}); err != nil {
		t.Fatal(err)
	}
	response = desktopDispatcher.Dispatch(ctx, ipc.Request{Command: CommandRemoteDigest}, ipc.Environment{})
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("desktop command should reach configured-client validation: %+v", response)
	}
}

func TestBundlePasswordAndPlaintextWarningFlowThroughIPC(t *testing.T) {
	instance := newTestInstance(t, false)
	dispatcher := ipc.NewDispatcher()
	if err := instance.service.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + "/bundle-nxbm.json"

	write := dispatchJSON(t, dispatcher, CommandBundleWrite, `{"args":{"path":`+jsonString(path)+`,"content":"{\"protocol\":1}","password":"bundle-pass-中文"}}`)
	if !write.OK {
		t.Fatalf("encrypted write response=%+v", write)
	}
	var writeResult BundleWriteResult
	if err := json.Unmarshal(write.Data, &writeResult); err != nil {
		t.Fatal(err)
	}
	if !writeResult.Encrypted || writeResult.Warning != "" {
		t.Fatalf("encrypted write result=%+v", writeResult)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < bundleHeaderSize || data[5]&bundleFlagEncrypted == 0 {
		t.Fatalf("bundle file is not an encrypted container: %q", data)
	}

	read := dispatchJSON(t, dispatcher, CommandBundleRead, `{"args":{"path":`+jsonString(path)+`,"password":"bundle-pass-中文"}}`)
	if !read.OK {
		t.Fatalf("encrypted read response=%+v", read)
	}
	var text string
	if err := json.Unmarshal(read.Data, &text); err != nil || text != `{"protocol":1}` {
		t.Fatalf("encrypted read text=%q err=%v", text, err)
	}
	wrong := dispatchJSON(t, dispatcher, CommandBundleRead, `{"args":{"path":`+jsonString(path)+`,"password":"wrong"}}`)
	if wrong.OK || wrong.Error == nil || wrong.Error.Code != ipc.CodeDecrypt {
		t.Fatalf("wrong-password read response=%+v", wrong)
	}

	plainPath := t.TempDir() + "/bundle-plain.json"
	plain := dispatchJSON(t, dispatcher, CommandBundleWrite, `{"args":{"path":`+jsonString(plainPath)+`,"content":"{\"protocol\":1}"}}`)
	if !plain.OK {
		t.Fatalf("plaintext write response=%+v", plain)
	}
	var plainResult BundleWriteResult
	if err := json.Unmarshal(plain.Data, &plainResult); err != nil {
		t.Fatal(err)
	}
	if plainResult.Encrypted || plainResult.Warning == "" {
		t.Fatalf("plaintext write must warn explicitly: %+v", plainResult)
	}
	plainRead := dispatchJSON(t, dispatcher, CommandBundleRead, `{"args":{"path":`+jsonString(plainPath)+`}}`)
	if !plainRead.OK {
		t.Fatalf("plaintext container read response=%+v", plainRead)
	}
	var plainText string
	if err := json.Unmarshal(plainRead.Data, &plainText); err != nil || plainText != `{"protocol":1}` {
		t.Fatalf("plaintext container read text=%q err=%v", plainText, err)
	}
}
