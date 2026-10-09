package production

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
)

func TestProductionSessionConnectQuickHostKeyFlow(t *testing.T) {
	server := newHostKeyTestServer(t)
	production := newHostKeyTestProduction(t)
	port := server.port()

	response := dispatchHostKeyTest(t, production, "session_connect_quick", `{"args":{"host":"127.0.0.1","port":`+strconv.Itoa(port)+`,"username":"test","authKind":"password","password":"secret"}}`)
	detail := requireHostKeyPending(t, response, false)
	if detail.Host != "127.0.0.1" || detail.Port != port {
		t.Fatalf("pending detail endpoint = %+v", detail)
	}
	acceptHostKeyTest(t, production, detail)

	connectedResponse := dispatchHostKeyTest(t, production, "session_connect_quick", `{"args":{"host":"127.0.0.1","port":`+strconv.Itoa(port)+`,"username":"test","authKind":"password","password":"secret"}}`)
	var connected sessionInfoDTO
	requireStoreTestResponse(t, connectedResponse, &connected)
	if connected.ID == "" {
		t.Fatal("quick connect returned an empty session")
	}
	if connected.AssetID == nil || *connected.AssetID == "" {
		t.Fatalf("quick connect session assetId = %+v, want synthetic id", connected.AssetID)
	}
	if connected.Name != "test@127.0.0.1:"+strconv.Itoa(port) {
		t.Fatalf("quick connect session name = %q", connected.Name)
	}
	if connected.Kind != session.KindSSH {
		t.Fatalf("quick connect session kind = %q", connected.Kind)
	}
	assetResponse := dispatchHostKeyTest(t, production, "asset_get", `{"id":"`+*connected.AssetID+`"}`)
	if assetResponse.OK || assetResponse.Error == nil || assetResponse.Error.Code != ipc.CodeNotFound {
		t.Fatalf("quick connect must not persist an asset, asset_get = %+v", assetResponse)
	}
	requireProductionNullHostKey(t, dispatchHostKeyTest(t, production, "session_disconnect", `{"sessionId":"`+connected.ID+`"}`))
}

func TestProductionSessionConnectQuickAuthFailureSurfacesSSHCode(t *testing.T) {
	server := newHostKeyTestServer(t)
	production := newHostKeyTestProduction(t)

	response := dispatchHostKeyTest(t, production, "session_connect_quick", `{"args":{"host":"127.0.0.1","port":`+strconv.Itoa(server.port())+`,"username":"not-test","authKind":"password","password":"secret","acceptHostKey":true}}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeSSH {
		t.Fatalf("quick connect auth failure = %+v, want ssh code", response)
	}
	if !strings.Contains(response.Error.Message, "认证失败") {
		t.Fatalf("quick connect auth failure message = %q, want user guidance", response.Error.Message)
	}
}

func TestProductionSessionConnectQuickValidation(t *testing.T) {
	production := newHostKeyTestProduction(t)

	cases := []struct {
		name string
		args string
	}{
		{"missing host", `{"args":{"authKind":"agent"}}`},
		{"port out of range", `{"args":{"host":"127.0.0.1","port":70000,"authKind":"agent"}}`},
		{"password auth without password", `{"args":{"host":"127.0.0.1","authKind":"password"}}`},
		{"unsupported auth kind", `{"args":{"host":"127.0.0.1","authKind":"key"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := dispatchHostKeyTest(t, production, "session_connect_quick", tc.args)
			if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
				t.Fatalf("quick connect %s = %+v, want bad_param", tc.name, response)
			}
		})
	}
}

func TestQuickConnectSessionAssetDefaults(t *testing.T) {
	asset, err := quickConnectSessionAsset(sessionQuickConnectRequest{Host: "example.com", AuthKind: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	if asset.Kind != session.KindSSH {
		t.Fatalf("asset kind = %q", asset.Kind)
	}
	if asset.ID == "" {
		t.Fatal("synthetic asset id is empty")
	}
	options, ok := asset.Options.(map[string]any)
	if !ok {
		t.Fatalf("asset options = %T", asset.Options)
	}
	if options["port"] != 22 {
		t.Fatalf("default port = %+v, want 22", options["port"])
	}
	username, _ := options["username"].(string)
	if username == "" {
		t.Fatal("default username is empty, want current OS user")
	}
	if !strings.Contains(asset.Name, "@example.com") {
		t.Fatalf("asset name = %q", asset.Name)
	}

	named, err := quickConnectSessionAsset(sessionQuickConnectRequest{Host: "example.com", Port: 2222, Username: "deploy", AuthKind: "password", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if named.Name != "deploy@example.com:2222" {
		t.Fatalf("asset name = %q", named.Name)
	}
	namedOptions := named.Options.(map[string]any)
	if namedOptions["authKind"] != "password" || namedOptions["password"] != "secret" {
		t.Fatalf("password options = %+v", namedOptions)
	}
	if _, leaked := namedOptions["autoAcceptUnknownHost"]; leaked {
		t.Fatal("acceptHostKey=false must not set autoAcceptUnknownHost")
	}
}

// 服务端装配(Desktop=false)下快速连接一律拒绝: 任意登录用户不能借服务器
// pivot 到任意 host:port; 桌面单用户模式不受影响 (见上文连接流程测试)。
func TestProductionSessionConnectQuickDisabledOnServerAssembly(t *testing.T) {
	production, err := NewProduction(t.Context(), ProductionConfig{
		Config: Config{
			Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
			Streams: ipc.StreamFactoryFuncs{},
		},
		DataDir: t.TempDir(), Desktop: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = production.Shutdown(context.Background()) })

	response := dispatchHostKeyTest(t, production, "session_connect_quick", `{"args":{"host":"127.0.0.1","authKind":"agent"}}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("server assembly quick connect = %+v, want unsupported", response)
	}
	response = dispatchHostKeyTest(t, production, "session_quick_connect_user", `{}`)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("server assembly quick connect user = %+v, want unsupported", response)
	}
}
