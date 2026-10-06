package fleetserver

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// wsPair 建一条真实 websocket 对: 返回客户端侧与服务端侧连接。
func wsPair(t *testing.T) (*websocket.Conn, *websocket.Conn) {
	t.Helper()
	accepted := make(chan *websocket.Conn, 1)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		accepted <- conn
	}))
	t.Cleanup(httpServer.Close)
	client, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(httpServer.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	server := <-accepted
	t.Cleanup(func() {
		client.CloseNow()
		server.CloseNow()
	})
	return client, server
}

type bridgeOutcome struct {
	conn net.Conn
	err  error
}

// startBridgeRequest 注册控制通道并发起 RequestBridge, 返回取消函数、结果通道
// 与控制通道上读到的 bridgeID。
func startBridgeRequest(t *testing.T, r *Registry, deviceID string) (context.CancelFunc, chan bridgeOutcome, string) {
	t.Helper()
	controlClient, controlServer := wsPair(t)
	r.RegisterControl(deviceID, controlServer, "")
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan bridgeOutcome, 1)
	go func() {
		conn, err := r.RequestBridge(ctx, deviceID)
		result <- bridgeOutcome{conn: conn, err: err}
	}()
	readCtx, readCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer readCancel()
	_, raw, err := controlClient.Read(readCtx)
	if err != nil {
		t.Fatal(err)
	}
	var message controlMessage
	if err := json.Unmarshal(raw, &message); err != nil {
		t.Fatal(err)
	}
	if message.Type != "bridge" || message.BridgeID == "" {
		t.Fatalf("control message = %+v, want bridge request", message)
	}
	return cancel, result, message.BridgeID
}

func bridgeIDs(r *Registry) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.bridges)
}

func pendingIDs(r *Registry) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pending)
}

// 放弃先于配对: 等待方取消后, 同一 bridgeID 的配对必须被拒绝, 且不登记 conn。
func TestRegistryAbandonBeforePair(t *testing.T) {
	r := NewRegistry()
	cancel, result, bridgeID := startBridgeRequest(t, r, "dev-abandon-pair")
	cancel()
	if outcome := <-result; outcome.err != errDeviceOffline {
		t.Fatalf("RequestBridge = %v, want errDeviceOffline", outcome.err)
	}
	_, agentConn := wsPair(t)
	if _, err := r.PairBridge("dev-abandon-pair", bridgeID, newWSConn(agentConn)); err != errBridgeNotPending {
		t.Fatalf("PairBridge = %v, want errBridgeNotPending", err)
	}
	if count := bridgeIDs(r); count != 0 {
		t.Fatalf("bridges = %d, want 0", count)
	}
	if count := pendingIDs(r); count != 0 {
		t.Fatalf("pending = %d, want 0", count)
	}
}

// 放弃先于交付: 已配对但等待方已取消时, deliver 必须关闭 conn 并从活动表移除,
// 不能送进无人消费的 channel。
func TestRegistryAbandonBeforeDeliverClosesConn(t *testing.T) {
	r := NewRegistry()
	cancel, result, bridgeID := startBridgeRequest(t, r, "dev-abandon-deliver")
	agentClient, agentConn := wsPair(t)
	deliver, err := r.PairBridge("dev-abandon-deliver", bridgeID, newWSConn(agentConn))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if outcome := <-result; outcome.err != errDeviceOffline {
		t.Fatalf("RequestBridge = %v, want errDeviceOffline", outcome.err)
	}
	deliver()
	if count := bridgeIDs(r); count != 0 {
		t.Fatalf("bridges = %d, want 0", count)
	}
	if count := pendingIDs(r); count != 0 {
		t.Fatalf("pending = %d, want 0", count)
	}
	readCtx, readCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer readCancel()
	if _, _, err := agentClient.Read(readCtx); err == nil {
		t.Fatal("agent bridge connection still open after abandoned deliver")
	}
}

// 交付先于放弃: conn 被等待方正常取走; 关闭并注销后活动表清空。
func TestRegistryDeliverBeforeAbandon(t *testing.T) {
	r := NewRegistry()
	cancel, result, bridgeID := startBridgeRequest(t, r, "dev-deliver-first")
	defer cancel()
	agentClient, agentConn := wsPair(t)
	conn := newWSConn(agentConn)
	deliver, err := r.PairBridge("dev-deliver-first", bridgeID, conn)
	if err != nil {
		t.Fatal(err)
	}
	deliver()
	outcome := <-result
	if outcome.err != nil {
		t.Fatalf("RequestBridge = %v, want delivered conn", outcome.err)
	}
	if outcome.conn != net.Conn(conn) {
		t.Fatal("RequestBridge returned a different conn")
	}
	// 调用方关闭 conn (等同 relay 断开) 后注销, 活动表必须清空。
	_ = conn.Close()
	r.UnregisterBridge(conn)
	if count := bridgeIDs(r); count != 0 {
		t.Fatalf("bridges = %d, want 0", count)
	}
	if count := pendingIDs(r); count != 0 {
		t.Fatalf("pending = %d, want 0", count)
	}
	readCtx, readCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer readCancel()
	if _, _, err := agentClient.Read(readCtx); err == nil {
		t.Fatal("agent bridge connection still open after caller close")
	}
}

// 双就绪交错: deliver 与取消并发, 无论 select 选中哪边都不得泄漏 conn——
// 要么 conn 交给调用方 (测试负责关闭并注销), 要么被放弃方 drain 关闭。
func TestRegistryDeliverAbandonDoubleReadyNoLeak(t *testing.T) {
	for iteration := 0; iteration < 30; iteration++ {
		r := NewRegistry()
		deviceID := "dev-double-ready"
		cancel, result, bridgeID := startBridgeRequest(t, r, deviceID)
		agentClient, agentConn := wsPair(t)
		conn := newWSConn(agentConn)
		deliver, err := r.PairBridge(deviceID, bridgeID, conn)
		if err != nil {
			t.Fatal(err)
		}
		go deliver()
		cancel()
		outcome := <-result
		if outcome.conn != nil {
			// select 选中交付: 调用方接管所有权并关闭。
			_ = outcome.conn.Close()
		}
		// 生产路径里 serveDeviceBridge 在 conn 关闭后注销 (defer UnregisterBridge);
		// 这里由测试扮演 handler 角色, 两种交错下 conn 此时都必须已关闭。
		r.UnregisterBridge(conn)
		if count := bridgeIDs(r); count != 0 {
			t.Fatalf("iteration %d: bridges = %d, want 0", iteration, count)
		}
		if count := pendingIDs(r); count != 0 {
			t.Fatalf("iteration %d: pending = %d, want 0", iteration, count)
		}
		readCtx, readCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if _, _, readErr := agentClient.Read(readCtx); readErr == nil {
			readCancel()
			t.Fatalf("iteration %d: agent bridge connection still open", iteration)
		}
		readCancel()
	}
}
