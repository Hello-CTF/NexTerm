package sync

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/coder/websocket"
)

// bridgeWSFixture 在 remoteAPIFixture 的真实账号面上加 GET /fleet/devices/{id}/bridge
// 回显路由: 会话校验走 fixture 的 session 中间件 (cookie 缺失/无效一律 401),
// 升级成功后二进制消息原样回显。
type bridgeWSFixture struct {
	*remoteAPIFixture
	failNextDial atomic.Bool
	sawCSRF      atomic.Bool
}

func newBridgeWSFixture(t *testing.T) *bridgeWSFixture {
	t.Helper()
	ctx := context.Background()
	db, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	inner := &remoteAPIFixture{db: db, accounts: account.New(db.GORM(), account.WithTOTPKeyFile(filepath.Join(t.TempDir(), "totp.key"))), mfaTickets: map[string]string{}}
	fixture := &bridgeWSFixture{remoteAPIFixture: inner}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/login", inner.serveLogin)
	mux.HandleFunc("POST /auth/totp/login", inner.serveTOTPLogin)
	mux.Handle("GET /auth/me", inner.session(http.HandlerFunc(inner.serveMe)))
	mux.Handle("GET /auth/dek", inner.session(http.HandlerFunc(inner.serveDEK)))
	mux.Handle("POST /sync/v2/push", inner.session(http.HandlerFunc(inner.serveOK)))
	mux.Handle("GET /fleet/devices/{id}/bridge", inner.session(http.HandlerFunc(fixture.serveBridge)))
	inner.Server = httptest.NewServer(mux)
	t.Cleanup(inner.Server.Close)
	return fixture
}

func (f *bridgeWSFixture) serveBridge(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(csrfHeaderName) != "" {
		f.sawCSRF.Store(true)
	}
	if f.failNextDial.CompareAndSwap(true, false) {
		writeSyncError(w, http.StatusForbidden, ipc.NewError(ipc.CodeForbidden, "设备已关闭终端访问"))
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
	for {
		messageType, payload, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		if err := conn.Write(r.Context(), messageType, payload); err != nil {
			return
		}
	}
}

func TestDialDeviceBridgeRoundTrip(t *testing.T) {
	fixture := newBridgeWSFixture(t)
	fixture.createUser(t, "alice", "alice-pw-123")
	instance := newTestInstance(t, true)
	if _, err := instance.service.LinkSet(context.Background(), linkPatch(fixture.URL, "alice", "alice-pw-123")); err != nil {
		t.Fatal(err)
	}

	conn, err := instance.service.Remote().DialDeviceBridge(context.Background(), "dev-1")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 4)
	if _, err := io.ReadFull(conn, buffer); err != nil || string(buffer) != "ping" {
		t.Fatalf("echo read = %q, %v; want ping", buffer, err)
	}
	if !fixture.sawCSRF.Load() {
		t.Fatal("bridge dial must carry the CSRF header like the HTTP client")
	}
	if got := fixture.logins.Load(); got != 1 {
		t.Fatalf("logins = %d, want 1 (cached session reused)", got)
	}
}

// TestDialDeviceBridgeReLoginAfterRevoke 服务端吊销会话后, 缓存 cookie 首次拨号
// 吃 401, remoteCall 的 dropSession 重登环必须用保存的口令重登并完成拨号。
func TestDialDeviceBridgeReLoginAfterRevoke(t *testing.T) {
	fixture := newBridgeWSFixture(t)
	userID := fixture.createUser(t, "alice", "alice-pw-123")
	instance := newTestInstance(t, true)
	if _, err := instance.service.LinkSet(context.Background(), linkPatch(fixture.URL, "alice", "alice-pw-123")); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.accounts.RevokeUserSessions(context.Background(), userID, ""); err != nil {
		t.Fatal(err)
	}

	conn, err := instance.service.Remote().DialDeviceBridge(context.Background(), "dev-1")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("pong")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 4)
	if _, err := io.ReadFull(conn, buffer); err != nil || string(buffer) != "pong" {
		t.Fatalf("echo read = %q, %v; want pong", buffer, err)
	}
	if got := fixture.logins.Load(); got != 2 {
		t.Fatalf("logins = %d, want 2 (401 后重登)", got)
	}
}

func TestDialDeviceBridgeForbiddenPassthrough(t *testing.T) {
	fixture := newBridgeWSFixture(t)
	fixture.createUser(t, "alice", "alice-pw-123")
	instance := newTestInstance(t, true)
	if _, err := instance.service.LinkSet(context.Background(), linkPatch(fixture.URL, "alice", "alice-pw-123")); err != nil {
		t.Fatal(err)
	}
	fixture.failNextDial.Store(true)
	if _, err := instance.service.Remote().DialDeviceBridge(context.Background(), "dev-1"); err == nil {
		t.Fatal("forbidden dial must fail")
	} else if errors.Is(err, errSessionExpired) {
		t.Fatalf("403 must not be misclassified as session expiry: %v", err)
	}
}

func TestDialDeviceBridgeWithoutSessionIs401(t *testing.T) {
	fixture := newBridgeWSFixture(t)
	fixture.createUser(t, "alice", "alice-pw-123")
	client, err := newRemoteClient(RemoteConfig{URL: fixture.URL, Username: "alice", Password: "alice-pw-123"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.dialDeviceBridge(context.Background(), "dev-1"); !errors.Is(err, errSessionExpired) {
		t.Fatalf("dial without login = %v, want errSessionExpired", err)
	}
}
