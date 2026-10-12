//go:build darwin || linux

package production

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/server"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/sharing"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/coder/websocket"
)

// previewRelayE2EServer 装配带只读预览的真实服务端: 真账号签发/CSRF、真
// sharing.Service、真 session.Manager 远程壳宿主, 经 hub adapter 供观众通道。
func newPreviewRelayE2EServer(t *testing.T) (*httptest.Server, *session.Manager) {
	t.Helper()
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	accounts := newRemoteTestAccounts(t, database)
	if err := accounts.createVerifiedUser(context.Background(), "alice", "alice-pw-123"); err != nil {
		t.Fatal(err)
	}
	previews, err := sharing.New(sharing.Config{DB: database.GORM(), Accounts: accounts.accounts})
	if err != nil {
		t.Fatal(err)
	}
	shells := session.NewManager(session.Config{})
	t.Cleanup(func() { _ = shells.Close() })
	realServer, err := server.New(server.Config{
		Options:    server.Options{Listen: "127.0.0.1:0", DataDir: t.TempDir(), Auth: server.AuthOn},
		Dispatcher: ipc.NewDispatcher(),
		Accounts:   accounts.accounts,
		Previews:   previews,
		Spectator:  shells,
		Channels:   server.NewHubAdapter(shells.Hub()),
		Version:    "9.9.9-e2e",
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = realServer.Close() })
	httpServer := httptest.NewServer(realServer.Handler())
	t.Cleanup(httpServer.Close)
	return httpServer, shells
}

func dialRelayE2EViewer(t *testing.T, httpURL, token string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(httpURL, "http")+"/share/preview/"+token, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

func readRelayE2EFrame(t *testing.T, conn *websocket.Conn) (websocket.MessageType, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	kind, payload, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("viewer read: %v", err)
	}
	return kind, payload
}

func readRelayE2EText(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	kind, payload := readRelayE2EFrame(t, conn)
	if kind != websocket.MessageText {
		t.Fatalf("frame kind = %v payload = %q, want text", kind, payload)
	}
	var frame map[string]any
	if err := json.Unmarshal(payload, &frame); err != nil {
		t.Fatalf("frame json: %v", err)
	}
	return frame
}

// readRelayE2EBinaryUntil 读帧直到出现包含 marker 的二进制输出帧。
func readRelayE2EBinaryUntil(t *testing.T, conn *websocket.Conn, marker string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		kind, payload := readRelayE2EFrame(t, conn)
		if kind == websocket.MessageBinary && strings.Contains(string(payload), marker) {
			return
		}
	}
	t.Fatalf("viewer never saw %q", marker)
}

// TestPreviewRelayEndToEnd 钉住桌面只读预览全链路: 登录 → 开本地 tab →
// preview_share_create 建壳+链接+中继 → 匿名 viewer 拿 ready/快照 → 本地输出
// 实时到达 → 中继启动后的输出进入壳模型 (新 viewer 快照可见) → tab 退出 →
// end+DELETE: 壳从服务端消失, viewer 收到 ended, Active 清空。
func TestPreviewRelayEndToEnd(t *testing.T) {
	httpServer, shells := newPreviewRelayE2EServer(t)
	production := newUpdateTestProduction(t, true, nil)
	production.Services.previewRelay.pollInterval = 20 * time.Millisecond

	response := dispatchHostKeyTest(t, production, "sync_link_set",
		`{"args":{"url":"`+httpServer.URL+`","username":"alice","password":"alice-pw-123"}}`)
	var link struct {
		Connect string `json:"connect"`
	}
	requireStoreTestResponse(t, response, &link)
	if link.Connect != "connected" {
		t.Fatalf("link connect = %q, want connected", link.Connect)
	}

	connected, err := production.Services.Sessions.Connect(context.Background(), session.Asset{
		ID: "e2e-local-" + ids.New(), Kind: session.KindLocal, Name: "e2e-host",
	})
	if err != nil {
		t.Fatal(err)
	}
	tab, err := production.Services.Sessions.OpenTab(context.Background(), session.OpenTabOptions{
		SessionID: connected.ID, ClientID: "owner", ChannelID: "owner-" + ids.New(), Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}

	response = dispatchHostKeyTest(t, production, "preview_share_create",
		`{"args":{"tabId":"`+tab.ID+`","ttlMs":3600000}}`)
	var created struct {
		Token     string `json:"token"`
		URL       string `json:"url"`
		SessionID string `json:"sessionId"`
	}
	requireStoreTestResponse(t, response, &created)
	if created.Token == "" || created.SessionID == "" {
		t.Fatalf("created = %+v", created)
	}
	if created.URL != httpServer.URL+"/share/preview/"+created.Token {
		t.Fatalf("url = %q, want %s/share/preview/%s", created.URL, httpServer.URL, created.Token)
	}

	viewer := dialRelayE2EViewer(t, httpServer.URL, created.Token)
	ready := readRelayE2EText(t, viewer)
	if ready["type"] != "ready" || ready["session_id"] != created.SessionID {
		t.Fatalf("ready = %v", ready)
	}
	snapshot := readRelayE2EText(t, viewer)
	if snapshot["type"] != "snapshot" {
		t.Fatalf("first channel frame = %v, want snapshot", snapshot)
	}

	marker := "e2e-marker-" + ids.New()
	if err := production.Services.Sessions.Write(context.Background(), tab.ID, "owner", []byte("echo "+marker+"\n")); err != nil {
		t.Fatal(err)
	}
	readRelayE2EBinaryUntil(t, viewer, marker)

	// 中继推入的字节已建成壳的屏幕模型: 新 viewer 的首帧快照应包含标记。
	secondViewer := dialRelayE2EViewer(t, httpServer.URL, created.Token)
	if ready := readRelayE2EText(t, secondViewer); ready["type"] != "ready" {
		t.Fatalf("second ready = %v", ready)
	}
	snapshot = readRelayE2EText(t, secondViewer)
	lines, _ := snapshot["lines"].([]any)
	joined := ""
	for _, line := range lines {
		if text, ok := line.(string); ok {
			joined += text + "\n"
		}
	}
	if !strings.Contains(joined, marker) {
		t.Fatalf("snapshot lines missing %q: %q", marker, joined)
	}
	_ = secondViewer.CloseNow()

	if err := production.Services.Sessions.Write(context.Background(), tab.ID, "owner", []byte("exit\n")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := shells.RemoteSessionInfo(created.SessionID); errors.Is(err, session.ErrTabNotFound) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := shells.RemoteSessionInfo(created.SessionID); !errors.Is(err, session.ErrTabNotFound) {
		t.Fatalf("remote shell still present after tab exit: %v", err)
	}
	for {
		kind, payload := readRelayE2EFrame(t, viewer)
		if kind != websocket.MessageText {
			// 退出回显等残余输出帧, 跳过等终止帧。
			continue
		}
		var frame map[string]any
		if err := json.Unmarshal(payload, &frame); err != nil {
			t.Fatalf("frame json: %v", err)
		}
		if frame["type"] == "error" {
			if frame["code"] != "ended" {
				t.Fatalf("viewer terminal frame = %v, want ended", frame)
			}
			break
		}
	}
	if active := production.Services.previewRelay.Active(tab.ID); active.SessionID != "" {
		t.Fatalf("active after exit = %+v", active)
	}
}
