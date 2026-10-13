package fleetserver

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/coder/websocket"
)

// bridgeWaitTimeout is the upper bound the server waits for the agent to dial a bridge outbound; the agent-side
// ensureHelper takes at most 30s, and the headroom covers dialing and the TLS handshake.
const bridgeWaitTimeout = 60 * time.Second

var (
	errDeviceOffline      = errors.New("设备不在线")
	errBridgeNotPending   = errors.New("桥接请求不存在或已超时")
	errPurposeUnsupported = errors.New("设备代理不支持该桥接用途")
)

type controlChannel struct {
	conn *websocket.Conn
	// stateDigest is the device-side supervisor state digest the agent reports in hello;
	// the managed-share data path uses it to pass the bridge helper's hello verification. The digest is carried by the hello
	// authenticated with device credentials and contains no credential material.
	stateDigest string
	// capabilities are the bridge purposes advertised in the agent hello; old agents report none and are
	// limited to implicit terminal bridges by the RequestBridge purpose gate.
	capabilities []string
}

type pendingBridge struct {
	deviceID string
	// purpose is the canonical (normalizePurpose) form of the requested purpose; pairing accepts only a
	// bridge hello echoing the same purpose.
	purpose string
	result  chan bridgeResult

	// delivered and abandoned are guarded by Registry.mu: "abandon waiting" and "deliver the connection"
	// must be mutually exclusive, otherwise the conn would land in a channel nobody consumes after the waiter leaves and leak.
	delivered bool
	abandoned bool
}

type bridgeResult struct {
	conn *wsConn
	err  error
}

// Registry tracks device control channels and bridge pairing: one control connection per device (a new connection replaces the old one),
// bridges pair one-shot by bridge_id, and revocation kicks the control connection plus all of the device's bridges.
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

// RegisterControl registers a device control channel; an existing connection for the same device is replaced and closed.
// Replacement is a unilateral server-side teardown, so it uses CloseNow: a graceful close handshake would block the new connection's
// hello_ok write (the peer may have stopped reading).
// Returning true means a replacement happened (the device was previously online, so this is not a new online transition).
func (r *Registry) RegisterControl(deviceID string, conn *websocket.Conn, stateDigest string, capabilities []string) (replaced bool) {
	r.mu.Lock()
	previous := r.controls[deviceID]
	r.controls[deviceID] = &controlChannel{conn: conn, stateDigest: stateDigest, capabilities: append([]string(nil), capabilities...)}
	r.mu.Unlock()
	if previous != nil {
		previous.conn.CloseNow()
	}
	return previous != nil
}

// UnregisterControl removes the registration only when conn is still the currently registered connection, so an old connection never deletes a new registration.
// Returning true means conn was the current registered connection and was removed (a real device offline); a replaced old connection,
// or service shutdown (Close already swapped the table), returns false, and callers must not record offline from it.
func (r *Registry) UnregisterControl(deviceID string, conn *websocket.Conn) (removed bool) {
	r.mu.Lock()
	if current := r.controls[deviceID]; current != nil && current.conn == conn {
		delete(r.controls, deviceID)
		removed = true
	}
	r.mu.Unlock()
	return removed
}

func (r *Registry) control(deviceID string) *websocket.Conn {
	r.mu.Lock()
	defer r.mu.Unlock()
	if channel := r.controls[deviceID]; channel != nil {
		return channel.conn
	}
	return nil
}

// AgentOnline reports whether the device control channel is currently registered (a real-time signal of the agent process being online,
// more timely than the staleness window of device_agent.last_seen_at).
func (r *Registry) AgentOnline(deviceID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.controls[deviceID] != nil
}

// StateDigest returns the supervisor state digest reported by the device control channel hello;
// the second return value is false when the device is offline or has not reported one.
func (r *Registry) StateDigest(deviceID string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	channel := r.controls[deviceID]
	if channel == nil || channel.stateDigest == "" {
		return "", false
	}
	return channel.stateDigest, true
}

// Capabilities returns the bridge purposes advertised by the device control channel hello;
// the second return value is false when the device is offline or has not reported any.
func (r *Registry) Capabilities(deviceID string) ([]string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	channel := r.controls[deviceID]
	if channel == nil || len(channel.capabilities) == 0 {
		return nil, false
	}
	return append([]string(nil), channel.capabilities...), true
}

// PushControl writes one JSON frame to the device control channel; it returns false when the device is offline or the write fails.
func (r *Registry) PushControl(deviceID string, message controlMessage) bool {
	conn := r.control(deviceID)
	if conn == nil {
		return false
	}
	writeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return writeControlJSON(writeCtx, conn, message) == nil
}

// KickDevice is the revocation closed loop: the control channel closes after receiving the revoke frame, and all of the device's bridges close with it.
// After the revoke frame is written, the teardown also uses CloseNow, without waiting for the peer's close handshake.
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

// RequestBridge requests a bridge over the device control channel and waits for the agent's outbound dial to pair,
// returning a net.Conn carrying the supervisor protocol byte stream (binary frames, byte-transparent).
// purpose selects the bridge service: empty or "terminal" is the supervisor terminal stream every agent serves;
// any other purpose is rejected with errPurposeUnsupported unless the agent advertised it in hello capabilities,
// which keeps old agents from mis-handling a purpose bridge as terminal.
// When the waiter's ctx is canceled or times out it abandons: the conn's ownership is decided against deliver under the same lock,
// an already-delivered conn is taken out and closed, and after the abandoned mark later delivers close the connection instead,
// so neither conn/goroutine nor the agent-side WS and helper subprocess ever leak.
func (r *Registry) RequestBridge(ctx context.Context, deviceID, purpose string) (net.Conn, error) {
	r.mu.Lock()
	channel := r.controls[deviceID]
	r.mu.Unlock()
	if channel == nil {
		return nil, errDeviceOffline
	}
	if !servesPurpose(channel.capabilities, purpose) {
		return nil, errPurposeUnsupported
	}
	conn := channel.conn
	bridgeID := ids.New()
	request := &pendingBridge{deviceID: deviceID, purpose: normalizePurpose(purpose), result: make(chan bridgeResult, 1)}
	r.mu.Lock()
	r.pending[bridgeID] = request
	r.mu.Unlock()
	// The pending table entry must be cleaned up on every path: the successful-delivery and the "abandon after delivery" drain paths
	// do not delete explicitly, so this defer is the backstop (a duplicate delete on the abandon path is harmless).
	defer func() {
		r.mu.Lock()
		delete(r.pending, bridgeID)
		r.mu.Unlock()
	}()
	writeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err := writeControlJSON(writeCtx, conn, controlMessage{Type: "bridge", BridgeID: bridgeID, Purpose: purpose})
	cancel()
	if err != nil {
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
	// Abandon path: races deliver under the same lock. If deliver already happened (the conn is in the channel,
	// its send guaranteed to complete after the flag is set) -> take it out and close; otherwise mark abandoned, and any later
	// deliver closes the connection instead.
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
	r.mu.Unlock()
	return nil, errDeviceOffline
}

// servesPurpose reports whether an agent with the advertised capabilities serves a bridge purpose:
// terminal is implicit (old agents predate capability advertisement), any other purpose must be advertised.
func servesPurpose(capabilities []string, purpose string) bool {
	if purpose == "" || purpose == agent.PurposeTerminal {
		return true
	}
	for _, capability := range capabilities {
		if capability == purpose {
			return true
		}
	}
	return false
}

// normalizePurpose maps the explicit terminal purpose to the empty canonical form for comparison.
func normalizePurpose(purpose string) string {
	if purpose == agent.PurposeTerminal {
		return ""
	}
	return purpose
}

// PairBridge verifies and registers an agent outbound bridge, returning the deliver function; the caller delivers only after
// writing hello_ok, guaranteeing the agent reads the handshake response before the supervisor byte stream (websocket
// preserves order per connection, but delivery and the handshake response come from two goroutines and must be explicitly ordered).
// An unknown bridgeID, a device or purpose mismatch, or an already-canceled request returns an error (the caller is responsible for closing the conn and reporting).
func (r *Registry) PairBridge(deviceID, bridgeID, purpose string, conn *wsConn) (func(), error) {
	r.mu.Lock()
	request := r.pending[bridgeID]
	if request != nil && (request.deviceID != deviceID || request.purpose != normalizePurpose(purpose)) {
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
		// Capacity 1 and delivered only once, so the send never blocks; the abandoning party's drain is guaranteed to receive it.
		request.result <- bridgeResult{conn: conn}
	}
	return deliver, nil
}

// unregisterAndClose removes the bridge from the active table and closes it, used on the cleanup path when the waiter is found
// to have abandoned before delivery.
func (r *Registry) unregisterAndClose(conn *wsConn) {
	r.mu.Lock()
	if current := r.bridges[conn.bridgeID]; current == conn {
		delete(r.bridges, conn.bridgeID)
	}
	r.mu.Unlock()
	_ = conn.Close()
}

// UnregisterBridge removes the bridge from the active table when it closes.
func (r *Registry) UnregisterBridge(conn *wsConn) {
	r.mu.Lock()
	if current := r.bridges[conn.bridgeID]; current == conn {
		delete(r.bridges, conn.bridgeID)
	}
	r.mu.Unlock()
}

// Close closes all control connections and bridges (service shutdown).
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
