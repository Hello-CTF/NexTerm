package production

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	syncservice "github.com/Hello-CTF/NexTerm/internal/sync"
	"github.com/coder/websocket"
)

const (
	hostShareAccountCheckInterval = time.Second
	hostShareStreamWriteTimeout   = 30 * time.Second
)

var (
	errHostShareViewerClosed = errors.New("host share viewer closed")
	errHostShareShutdown     = errors.New("host share proxy stopped")
	errHostShareAccount      = errors.New("host share account changed")
)

type hostShareRemote interface {
	DialHostShareTerminal(ctx context.Context, deviceID string) (*websocket.Conn, error)
	HostShareIdentity(ctx context.Context) (*syncservice.RemoteHostShareIdentity, error)
}

type hostShareIdentityFunc func(ctx context.Context) (*syncservice.RemoteHostShareIdentity, error)

type hostShareConn interface {
	Read(ctx context.Context) (websocket.MessageType, []byte, error)
	Write(ctx context.Context, messageType websocket.MessageType, data []byte) error
	Close(code websocket.StatusCode, reason string) error
	CloseNow() error
}

type hostShareDial func(ctx context.Context, deviceID string) (hostShareConn, error)

type hostShareStream interface {
	SendJSON(ctx context.Context, data json.RawMessage) error
	Close() error
}

type hostShareErrorFrame struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type hostShareCloseFrame struct {
	Type     string `json:"type"`
	Code     int    `json:"code"`
	Reason   string `json:"reason,omitempty"`
	WasClean bool   `json:"wasClean"`
}

type hostShareOutputFrame struct {
	Type string `json:"type"`
	Data []byte `json:"data"`
}

type hostShareProxy struct {
	id           string
	identity     syncservice.RemoteHostShareIdentity
	conn         hostShareConn
	stream       hostShareStream
	ctx          context.Context
	cancel       context.CancelCauseFunc
	writeTimeout time.Duration
	writable     atomic.Bool
	writeMu      sync.Mutex
	watchWG      sync.WaitGroup
}

type hostShareIPC struct {
	identity hostShareIdentityFunc
	dial     hostShareDial

	checkInterval time.Duration
	writeTimeout  time.Duration
	mu            sync.Mutex
	ctx           context.Context
	cancel        context.CancelFunc
	proxies       map[string]*hostShareProxy
	closed        bool
	wg            sync.WaitGroup
}

func newHostShareIPC(remote hostShareRemote) *hostShareIPC {
	manager := &hostShareIPC{
		checkInterval: hostShareAccountCheckInterval, writeTimeout: hostShareStreamWriteTimeout,
		proxies: make(map[string]*hostShareProxy),
	}
	if remote != nil {
		manager.identity = remote.HostShareIdentity
		manager.dial = func(ctx context.Context, deviceID string) (hostShareConn, error) {
			return remote.DialHostShareTerminal(ctx, deviceID)
		}
	}
	return manager
}

func (m *hostShareIPC) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.ctx, m.cancel = context.WithCancel(ctx)
	return nil
}

func (m *hostShareIPC) Identity(ctx context.Context) (*syncservice.RemoteHostShareIdentity, error) {
	if m.identity == nil {
		return nil, ipc.NewError(ipc.CodeUnsupported, "共享主机终端仅在桌面远程账号下可用")
	}
	identity, err := m.identity(ctx)
	if err != nil {
		return nil, err
	}
	if !validHostShareIdentity(identity) {
		return nil, ipc.NewError(ipc.CodeForbidden, "请先登录服务端账号")
	}
	return identity, nil
}

func (m *hostShareIPC) Open(ctx context.Context, call *ipc.Call, deviceID, streamID string) error {
	if deviceID == "" || streamID == "" || call == nil || call.Channel.ID == "" {
		return ipc.NewError(ipc.CodeBadParam, "deviceId/streamId/channel 不能为空")
	}
	if streamID != call.Channel.ID {
		return ipc.NewError(ipc.CodeBadParam, "streamId 必须与终端通道一致")
	}
	if m.dial == nil {
		return ipc.NewError(ipc.CodeUnsupported, "共享主机终端仅在桌面远程账号下可用")
	}
	identity, err := m.Identity(ctx)
	if err != nil {
		return err
	}

	m.mu.Lock()
	if m.closed || m.ctx == nil {
		m.mu.Unlock()
		return ipc.NewError(ipc.CodeDisconnected, "共享终端代理未就绪")
	}
	if _, exists := m.proxies[streamID]; exists {
		m.mu.Unlock()
		return ipc.NewError(ipc.CodeBadParam, "共享终端通道已在使用")
	}
	proxyCtx, cancel := context.WithCancelCause(m.ctx)
	proxy := &hostShareProxy{id: streamID, identity: *identity, ctx: proxyCtx, cancel: cancel, writeTimeout: m.writeTimeout}
	m.proxies[streamID] = proxy
	m.mu.Unlock()

	keep := false
	defer func() {
		if !keep {
			cancel(errHostShareViewerClosed)
			m.remove(proxy)
		}
	}()

	if call.Streams == nil {
		return ipc.ErrStreamsUnavailable
	}
	stream, err := call.Streams.OpenJSON(ctx, call.Channel)
	if err != nil {
		return err
	}
	proxy.stream = stream
	dialCtx, cancelDial := context.WithCancel(ctx)
	stopDial := context.AfterFunc(proxy.ctx, cancelDial)
	conn, err := m.dial(dialCtx, deviceID)
	stopDial()
	cancelDial()
	if err != nil {
		_ = proxy.stream.Close()
		return err
	}
	proxy.publishConn(conn)
	if proxy.ctx.Err() != nil {
		_ = conn.CloseNow()
		_ = proxy.stream.Close()
		return ipc.NewError(ipc.CodeDisconnected, "共享终端已关闭")
	}

	current, err := m.identity(ctx)
	if err != nil || !sameHostShareIdentity(current, proxy.identity) {
		_ = conn.CloseNow()
		_ = proxy.stream.Close()
		if err != nil {
			return err
		}
		return ipc.NewError(ipc.CodeForbidden, "账号或服务器已切换，请重新打开共享终端")
	}

	m.mu.Lock()
	if m.closed || m.ctx == nil || m.proxies[streamID] != proxy {
		m.mu.Unlock()
		_ = conn.CloseNow()
		_ = proxy.stream.Close()
		return ipc.NewError(ipc.CodeDisconnected, "共享终端代理已关闭")
	}
	m.wg.Add(1)
	m.mu.Unlock()
	keep = true
	go m.run(proxy)
	return nil
}

func (m *hostShareIPC) Input(streamID string, data []byte) error {
	if streamID == "" {
		return ipc.NewError(ipc.CodeBadParam, "streamId 不能为空")
	}
	m.mu.Lock()
	proxy := m.proxies[streamID]
	m.mu.Unlock()
	if proxy == nil {
		return ipc.NewError(ipc.CodeNotFound, "共享终端已关闭")
	}
	if !proxy.writable.Load() {
		return ipc.NewError(ipc.CodeForbidden, "分享授权为只读")
	}
	if len(data) == 0 {
		return nil
	}
	proxy.writeMu.Lock()
	defer proxy.writeMu.Unlock()
	if err := proxy.ctx.Err(); err != nil {
		return ipc.NewError(ipc.CodeDisconnected, "共享终端已关闭")
	}
	writeCtx, cancel := context.WithTimeout(proxy.ctx, proxy.writeTimeout)
	defer cancel()
	if err := proxy.conn.Write(writeCtx, websocket.MessageBinary, data); err != nil {
		proxy.cancel(err)
		_ = proxy.conn.CloseNow()
		return err
	}
	return nil
}

func (m *hostShareIPC) Close(streamID string) error {
	if streamID == "" {
		return ipc.NewError(ipc.CodeBadParam, "streamId 不能为空")
	}
	m.mu.Lock()
	proxy := m.proxies[streamID]
	m.mu.Unlock()
	if proxy == nil {
		return nil
	}
	proxy.cancel(errHostShareViewerClosed)
	proxy.writeMu.Lock()
	if proxy.conn != nil {
		_ = proxy.conn.Close(websocket.StatusNormalClosure, "viewer closed")
	}
	proxy.writeMu.Unlock()
	return nil
}

func (p *hostShareProxy) publishConn(conn hostShareConn) {
	p.writeMu.Lock()
	p.conn = conn
	p.writeMu.Unlock()
}

func (p *hostShareProxy) connSnapshot() hostShareConn {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	return p.conn
}

func validHostShareIdentity(identity *syncservice.RemoteHostShareIdentity) bool {
	return identity != nil && identity.ServerURL != "" && identity.UserID != "" && identity.Generation != ""
}

func sameHostShareIdentity(current *syncservice.RemoteHostShareIdentity, expected syncservice.RemoteHostShareIdentity) bool {
	return validHostShareIdentity(current) && *current == expected
}

func (m *hostShareIPC) run(proxy *hostShareProxy) {
	defer m.wg.Done()
	defer m.remove(proxy)
	defer func() { _ = proxy.stream.Close() }()
	conn := proxy.connSnapshot()
	if conn == nil {
		return
	}
	defer func() { _ = conn.CloseNow() }()
	proxy.watchWG.Add(1)
	go func() {
		defer proxy.watchWG.Done()
		m.watchAccount(proxy)
	}()
	defer proxy.watchWG.Wait()
	defer proxy.cancel(errHostShareViewerClosed)

	for {
		messageType, payload, err := conn.Read(proxy.ctx)
		if err != nil {
			m.finish(proxy, conn, err)
			return
		}
		switch messageType {
		case websocket.MessageText:
			var frame struct {
				Type       string `json:"type"`
				Permission string `json:"permission"`
			}
			if err := json.Unmarshal(payload, &frame); err != nil {
				continue
			}
			if frame.Type == "ready" {
				proxy.writable.Store(frame.Permission == "read_write")
			}
			if err := proxy.sendJSON(proxy.ctx, json.RawMessage(payload)); err != nil {
				proxy.cancel(err)
				_ = conn.CloseNow()
				return
			}
		case websocket.MessageBinary:
			if err := proxy.sendJSON(proxy.ctx, mustHostShareJSON(hostShareOutputFrame{Type: "output", Data: payload})); err != nil {
				proxy.cancel(err)
				_ = conn.CloseNow()
				return
			}
		}
	}
}

func (m *hostShareIPC) watchAccount(proxy *hostShareProxy) {
	ticker := time.NewTicker(m.checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-proxy.ctx.Done():
			return
		case <-ticker.C:
			current, err := m.identity(proxy.ctx)
			if err != nil {
				proxy.cancel(err)
			} else if !sameHostShareIdentity(current, proxy.identity) {
				proxy.cancel(errHostShareAccount)
			} else {
				continue
			}
			if conn := proxy.connSnapshot(); conn != nil {
				_ = conn.CloseNow()
			}
			return
		}
	}
}

func (m *hostShareIPC) finish(proxy *hostShareProxy, conn hostShareConn, readErr error) {
	cause := context.Cause(proxy.ctx)
	intentional := errors.Is(cause, errHostShareViewerClosed) || errors.Is(cause, errHostShareShutdown)
	if !intentional {
		code, reason, clean := closeDetails(readErr)
		var remoteClose websocket.CloseError
		if errors.Is(cause, errHostShareAccount) {
			_ = proxy.sendJSON(context.Background(), mustHostShareJSON(hostShareErrorFrame{
				Type: "error", Code: "forbidden", Message: "账号或服务器已切换，或已登出，共享终端已关闭",
			}))
		} else if !errors.As(readErr, &remoteClose) {
			_ = proxy.sendJSON(context.Background(), mustHostShareJSON(hostShareErrorFrame{
				Type: "error", Code: "disconnected", Message: "共享终端连接已断开",
			}))
		}
		_ = proxy.sendJSON(context.Background(), mustHostShareJSON(hostShareCloseFrame{
			Type: "close", Code: code, Reason: reason, WasClean: clean,
		}))
	}
	proxy.cancel(errHostShareViewerClosed)
	_ = conn.CloseNow()
}

func closeDetails(err error) (int, string, bool) {
	var closeErr websocket.CloseError
	if errors.As(err, &closeErr) {
		return int(closeErr.Code), closeErr.Reason, closeErr.Code == websocket.StatusNormalClosure
	}
	return int(websocket.StatusAbnormalClosure), "", false
}

func (p *hostShareProxy) sendJSON(ctx context.Context, payload json.RawMessage) error {
	writeCtx, cancel := context.WithTimeout(ctx, p.writeTimeout)
	defer cancel()
	return p.stream.SendJSON(writeCtx, payload)
}

func mustHostShareJSON(frame any) json.RawMessage {
	payload, _ := json.Marshal(frame)
	return payload
}

func (m *hostShareIPC) remove(proxy *hostShareProxy) {
	m.mu.Lock()
	if m.proxies[proxy.id] == proxy {
		delete(m.proxies, proxy.id)
	}
	m.mu.Unlock()
}

func (m *hostShareIPC) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	cancel := m.cancel
	m.cancel = nil
	m.ctx = nil
	proxies := make([]*hostShareProxy, 0, len(m.proxies))
	for _, proxy := range m.proxies {
		proxies = append(proxies, proxy)
	}
	m.mu.Unlock()
	for _, proxy := range proxies {
		proxy.cancel(errHostShareShutdown)
		if conn := proxy.connSnapshot(); conn != nil {
			_ = conn.CloseNow()
		}
	}
	if cancel != nil {
		cancel()
	}
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type hostShareOpenRequest struct {
	DeviceID string `json:"deviceId"`
	StreamID string `json:"streamId"`
}

type hostShareStreamRequest struct {
	StreamID string `json:"streamId"`
}

type hostShareInputRequest struct {
	StreamID string `json:"streamId"`
	Data     []byte `json:"data"`
}

func registerHostShareCommands(dispatcher *ipc.Dispatcher, manager *hostShareIPC) error {
	registrations := []func() error{
		func() error {
			return ipc.Register(dispatcher, "host_share_identity", func(ctx context.Context, _ *ipc.Call, _ struct{}) (*syncservice.RemoteHostShareIdentity, error) {
				return manager.Identity(ctx)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "host_share_terminal_open", func(ctx context.Context, call *ipc.Call, input hostShareOpenRequest) (syncservice.RemoteOK, error) {
				if err := manager.Open(ctx, call, input.DeviceID, input.StreamID); err != nil {
					return syncservice.RemoteOK{}, err
				}
				return syncservice.RemoteOK{OK: true}, nil
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "host_share_terminal_input", func(ctx context.Context, _ *ipc.Call, input hostShareInputRequest) (syncservice.RemoteOK, error) {
				if err := manager.Input(input.StreamID, input.Data); err != nil {
					return syncservice.RemoteOK{}, err
				}
				return syncservice.RemoteOK{OK: true}, nil
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "host_share_terminal_close", func(_ context.Context, _ *ipc.Call, input hostShareStreamRequest) (syncservice.RemoteOK, error) {
				if err := manager.Close(input.StreamID); err != nil {
					return syncservice.RemoteOK{}, err
				}
				return syncservice.RemoteOK{OK: true}, nil
			})
		},
	}
	for _, register := range registrations {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}

var _ Component = (*hostShareIPC)(nil)
