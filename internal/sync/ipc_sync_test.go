package sync

import (
	"context"
	"encoding/json"
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
		CommandPull, CommandPush, CommandRemoteDigest, CommandToken, CommandTokenRotate,
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
