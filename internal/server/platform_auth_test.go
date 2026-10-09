package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

const testGatewayKey = "gateway-secret"

// newPlatformFixture 装配一个平台托管模式实例(带网关头), 与懒猫上的部署同构:
// 监听非回环地址、访问控制交给网关头、账号库为空。
func newPlatformFixture(t *testing.T) *accountFixture {
	t.Helper()
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	config := testConfig(t, false)
	config.Options.Auth = AuthPlatform
	config.Options.Listen = "0.0.0.0:8080"
	config.GatewayAuthKey = testGatewayKey
	accounts := account.New(database.DB())
	config.Accounts = accounts
	server, httpServer := newTestHTTP(t, config)
	return &accountFixture{
		server: server, http: httpServer, accounts: accounts, db: database.DB(),
		client: &http.Client{},
	}
}

func TestPlatformModeRequiresGatewayKey(t *testing.T) {
	config := testConfig(t, false)
	config.Options.Auth = AuthPlatform
	config.Options.Listen = "0.0.0.0:8080"
	if _, err := New(config); err == nil {
		t.Fatal("平台模式未配置网关密钥时必须启动即失败, 否则无人能进也无人知道")
	}
}

// TestPlatformSessionEstablishesOwner 覆盖懒猫上的完整免码路径:
// 网关头 => 自动成为平台所有者 => 拿到会话 => 前端不见登录/初始化界面。
func TestPlatformSessionEstablishesOwner(t *testing.T) {
	fixture := newPlatformFixture(t)
	gateway := map[string]string{GatewayAuthHeader: testGatewayKey}

	if call := fixture.call(t, http.MethodPost, "/auth/platform-session", nil, nil, "", nil); call.status != http.StatusForbidden {
		t.Fatalf("无网关头换取会话 = %d, want %d", call.status, http.StatusForbidden)
	}

	call := fixture.call(t, http.MethodPost, "/auth/platform-session", nil, nil, "", gateway)
	if call.status != http.StatusOK {
		t.Fatalf("平台会话 = %d body=%v", call.status, call.body)
	}
	session := captureSession(t, call)
	if username, _ := session.user["username"].(string); username != account.PlatformOwnerUsername {
		t.Fatalf("所有者用户名 = %q, want %q", username, account.PlatformOwnerUsername)
	}
	if role, _ := session.user["role"].(string); role != string(account.RoleSuperadmin) {
		t.Fatalf("所有者角色 = %q, want %q", role, account.RoleSuperadmin)
	}

	again := fixture.call(t, http.MethodPost, "/auth/platform-session", nil, session, "", gateway)
	if again.status != http.StatusOK {
		t.Fatalf("复用平台会话 = %d body=%v", again.status, again.body)
	}
	reused, _ := again.body["user"].(map[string]any)
	if id, _ := reused["id"].(string); id != session.user["id"] {
		t.Fatalf("平台所有者被重复创建: %v != %v", reused["id"], session.user["id"])
	}
	if count, err := fixture.accounts.CountUsers(context.Background()); err != nil || count != 1 {
		t.Fatalf("账号数 = %d err=%v, want 1", count, err)
	}

	me := fixture.call(t, http.MethodGet, "/auth/me", nil, session, "", gateway)
	if me.status != http.StatusOK {
		t.Fatalf("/auth/me = %d body=%v", me.status, me.body)
	}

	init := fixture.call(t, http.MethodPost, "/auth/init", map[string]any{"code": "whatever"}, nil, "", gateway)
	if init.status != http.StatusForbidden {
		t.Fatalf("平台模式下的 /auth/init = %d, want %d", init.status, http.StatusForbidden)
	}

	status := fixture.call(t, http.MethodGet, "/auth/status", nil, nil, "", gateway)
	if status.status != http.StatusOK {
		t.Fatalf("/auth/status = %d", status.status)
	}
	if auth, _ := status.body["auth"].(string); auth != AuthPlatform {
		t.Fatalf("auth = %q, want %q", auth, AuthPlatform)
	}
	if initialized, _ := status.body["initialized"].(bool); !initialized {
		t.Fatal("平台所有者建立后 initialized 应为 true, 否则前端会退回初始化向导")
	}
}

// TestPlatformModeStillRefusesForeignGatewayKey 确认网关头仍是硬门:
// 换了密钥的请求既拿不到会话, 也读不到需要会话的路由。
func TestPlatformModeStillRefusesForeignGatewayKey(t *testing.T) {
	fixture := newPlatformFixture(t)
	forged := map[string]string{GatewayAuthHeader: "not-the-key"}

	if call := fixture.call(t, http.MethodPost, "/auth/platform-session", nil, nil, "", forged); call.status != http.StatusForbidden {
		t.Fatalf("伪造网关头换取会话 = %d, want %d", call.status, http.StatusForbidden)
	}
	if count, err := fixture.accounts.CountUsers(context.Background()); err != nil || count != 0 {
		t.Fatalf("伪造请求不应建号: count=%d err=%v", count, err)
	}
	if call := fixture.call(t, http.MethodGet, "/auth/devices", nil, nil, "", forged); call.status != http.StatusUnauthorized {
		t.Fatalf("伪造网关头读取设备列表 = %d, want %d", call.status, http.StatusUnauthorized)
	}
}
