package fleetserver

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/coder/websocket"
)

// bridgeWaitTimeout 是服务端等待 agent 出站拨号桥接的上限; agent 侧
// ensureHelper 最长 30s, 余量覆盖拨号与 TLS 握手。
const bridgeWaitTimeout = 60 * time.Second

var (
	errDeviceOffline    = errors.New("设备不在线")
	errBridgeNotPending = errors.New("桥接请求不存在或已超时")
)

type controlChannel struct {
	conn *websocket.Conn
}

type pendingBridge struct {
	deviceID string
	result   chan bridgeResult

	// delivered 与 abandoned 由 Registry.mu 保护: "放弃等待"与"交付连接"
	// 必须互斥, 否则等待方离开后 conn 会进无人消费的 channel 而泄漏。
	delivered bool
	abandoned bool
}

type bridgeResult struct {
	conn *wsConn
	err  error
}

// Registry 跟踪设备控制通道与桥接配对: 单设备单控制连接 (新连接顶替旧连接),
// 桥接按 bridge_id 一次性配对, 吊销时踢掉控制连接与该设备的全部桥接。
type Registry struct {
	mu       sync.Mutex
	controls map[string]*controlChannel
	pending  map[string]*pendingBridge
	bridges  map[string]*wsConn
}

func NewRegistry() *Registry {
	return &Registry{
		controls: make(map[string]*controlChannel),
		pending:  make(map[string]*pendingBridge),
		bridges:  make(map[string]*wsConn),
	}
}

// RegisterControl 注册设备控制通道; 同设备已有连接时被顶替关闭。
// 顶替是服务端单方面拆连, 用 CloseNow: 优雅关闭握手会阻塞新连接的
// hello_ok 写出 (对端可能已不在读取)。
func (r *Registry) RegisterControl(deviceID string, conn *websocket.Conn) {
	r.mu.Lock()
	previous := r.controls[deviceID]
	r.controls[deviceID] = &controlChannel{conn: conn}
	r.mu.Unlock()
	if previous != nil {
		previous.conn.CloseNow()
	}
}

// UnregisterControl 仅当 conn 仍是当前注册连接时才移除, 避免旧连接误删新注册。
func (r *Registry) UnregisterControl(deviceID string, conn *websocket.Conn) {
	r.mu.Lock()
	if current := r.controls[deviceID]; current != nil && current.conn == conn {
		delete(r.controls, deviceID)
	}
	r.mu.Unlock()
}

func (r *Registry) control(deviceID string) *websocket.Conn {
	r.mu.Lock()
	defer r.mu.Unlock()
	if channel := r.controls[deviceID]; channel != nil {
		return channel.conn
	}
	return nil
}

// PushControl 向设备控制通道写入一帧 JSON; 设备离线或写入失败返回 false。
func (r *Registry) PushControl(deviceID string, message controlMessage) bool {
	conn := r.control(deviceID)
	if conn == nil {
		return false
	}
	writeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return writeControlJSON(writeCtx, conn, message) == nil
}

// KickDevice 吊销闭环: 控制通道收到 revoke 帧后关闭, 该设备的全部桥接一并关闭。
// revoke 帧写出后同样 CloseNow 拆连, 不等对端的关闭握手。
func (r *Registry) KickDevice(deviceID string) {
	r.mu.Lock()
	control := r.controls[deviceID]
	delete(r.controls, deviceID)
	var bridges []*wsConn
	for bridgeID, conn := range r.bridges {
		if conn.deviceID == deviceID {
			bridges = append(bridges, conn)
			delete(r.bridges, bridgeID)
		}
	}
	r.mu.Unlock()
	if control != nil {
		writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = writeControlJSON(writeCtx, control.conn, controlMessage{Type: "revoke"})
		cancel()
		control.conn.CloseNow()
	}
	for _, conn := range bridges {
		_ = conn.Close()
	}
}

// RequestBridge 通过设备控制通道请求一条桥接并等待 agent 出站拨号配对,
// 返回的 net.Conn 承载 supervisor 协议字节流 (二进制帧, 字节透明)。
// 等待方 ctx 取消或超时即放弃: 在同一锁下与 deliver 互斥决定 conn 归属,
// 已交付的 conn 会被取走并关闭, 未交付的标记放弃后 deliver 改为关闭连接,
// 保证 conn/goroutine/agent 侧 WS 与 helper 子进程都不会泄漏。
func (r *Registry) RequestBridge(ctx context.Context, deviceID string) (net.Conn, error) {
	conn := r.control(deviceID)
	if conn == nil {
		return nil, errDeviceOffline
	}
	bridgeID := ids.New()
	request := &pendingBridge{deviceID: deviceID, result: make(chan bridgeResult, 1)}
	r.mu.Lock()
	r.pending[bridgeID] = request
	r.mu.Unlock()
	writeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err := writeControlJSON(writeCtx, conn, controlMessage{Type: "bridge", BridgeID: bridgeID})
	cancel()
	if err != nil {
		r.mu.Lock()
		delete(r.pending, bridgeID)
		r.mu.Unlock()
		return nil, errDeviceOffline
	}
	waitCtx, waitCancel := context.WithTimeout(ctx, bridgeWaitTimeout)
	defer waitCancel()
	select {
	case result := <-request.result:
		if result.err != nil {
			return nil, result.err
		}
		return result.conn, nil
	case <-waitCtx.Done():
	}
	// 放弃路径: 与 deliver 在同一锁下竞争。deliver 已发生 (conn 在 channel
	// 里, 其发送在置位后必然完成) -> 取走并关闭; 否则标记放弃, 之后的
	// deliver 会改为关闭连接。
	r.mu.Lock()
	if request.delivered {
		r.mu.Unlock()
		result := <-request.result
		if result.conn != nil {
			_ = result.conn.Close()
		}
		return nil, errDeviceOffline
	}
	request.abandoned = true
	delete(r.pending, bridgeID)
	r.mu.Unlock()
	return nil, errDeviceOffline
}

// PairBridge 校验并登记 agent 出站桥接, 返回交付函数; 调用方写出 hello_ok
// 后再交付, 保证 agent 先读到握手应答、再读到 supervisor 字节流 (websocket
// 按连接保序, 但交付与握手应答分属两个 goroutine, 必须显式排序)。
// bridgeID 未知、设备不匹配或请求已取消时返回错误 (连接由调用方负责关闭与报错)。
func (r *Registry) PairBridge(deviceID, bridgeID string, conn *wsConn) (func(), error) {
	r.mu.Lock()
	request := r.pending[bridgeID]
	if request != nil && request.deviceID != deviceID {
		request = nil
	}
	if request == nil || request.abandoned {
		r.mu.Unlock()
		return nil, errBridgeNotPending
	}
	conn.deviceID = deviceID
	conn.bridgeID = bridgeID
	r.bridges[bridgeID] = conn
	r.mu.Unlock()
	deliver := func() {
		r.mu.Lock()
		if request.abandoned {
			r.mu.Unlock()
			r.unregisterAndClose(conn)
			return
		}
		request.delivered = true
		r.mu.Unlock()
		// 容量 1 且只交付一次, 发送不会阻塞; 放弃方的 drain 必然收到。
		request.result <- bridgeResult{conn: conn}
	}
	return deliver, nil
}

// unregisterAndClose 把桥接从活动表移除并关闭, 用于交付前发现等待方已放弃
// 的清理路径。
func (r *Registry) unregisterAndClose(conn *wsConn) {
	r.mu.Lock()
	if current := r.bridges[conn.bridgeID]; current == conn {
		delete(r.bridges, conn.bridgeID)
	}
	r.mu.Unlock()
	_ = conn.Close()
}

// UnregisterBridge 桥接关闭时从活动表中移除。
func (r *Registry) UnregisterBridge(conn *wsConn) {
	r.mu.Lock()
	if current := r.bridges[conn.bridgeID]; current == conn {
		delete(r.bridges, conn.bridgeID)
	}
	r.mu.Unlock()
}

// Close 关闭全部控制连接与桥接 (服务关停)。
func (r *Registry) Close() {
	r.mu.Lock()
	controls := r.controls
	bridges := r.bridges
	r.controls = make(map[string]*controlChannel)
	r.bridges = make(map[string]*wsConn)
	r.pending = make(map[string]*pendingBridge)
	r.mu.Unlock()
	for _, control := range controls {
		control.conn.CloseNow()
	}
	for _, conn := range bridges {
		_ = conn.Close()
	}
}
