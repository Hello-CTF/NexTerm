package production

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// 设备远程终端桥 (FLEET 第 2 期): 桌面端经已登录服务端账号把 fleet 设备的
// supervisor v2 终端会话桥到前端。协议编解码全部复用 internal/supervisor
// 客户端 (与 WEB 的 src/ipc/deviceTerminalApi.ts 同一合同): open 是 create
// (独立桥) + attach (独立桥) 两段, 失败按 attempt/身份回收; resume 是带期望
// 身份的 attach; 服务端→桌面的 output/exit/断线经 wails 事件推送。

const (
	TopicDeviceTerminalOutput ipc.Topic = "device-terminal://output"
	TopicDeviceTerminalExit   ipc.Topic = "device-terminal://exit"
	TopicDeviceTerminalClosed ipc.Topic = "device-terminal://closed"
)

type DeviceTerminalOutputEvent struct {
	ChannelID string `json:"channelId"`
	Data      []byte `json:"data"`
}

type DeviceTerminalExitEvent struct {
	ChannelID string `json:"channelId"`
	Code      int    `json:"code"`
	Signal    string `json:"signal"`
}

// DeviceTerminalClosedEvent 的 code/message 为空表示干净关闭 (exit 之后或
// 对端正常结束); 非空是带码断线 (disconnected/protocol/unavailable 等)。
type DeviceTerminalClosedEvent struct {
	ChannelID string `json:"channelId"`
	Code      string `json:"code,omitempty"`
	Message   string `json:"message,omitempty"`
}

type deviceTerminalDial func(ctx context.Context, deviceID string) (net.Conn, error)

type deviceTerminalManager struct {
	dial   deviceTerminalDial
	events ipc.Emitter

	mu       sync.Mutex
	channels map[string]*deviceTerminalChannel
	closed   bool
	wg       sync.WaitGroup
}

type deviceTerminalChannel struct {
	id       string
	stream   *supervisor.Stream
	once     sync.Once
	pumpOnce sync.Once
}

type deviceTerminalOpenResult struct {
	channelID string
	info      supervisor.Info
	identity  supervisor.Identity
}

func newDeviceTerminalManager(dial deviceTerminalDial, events ipc.Emitter) *deviceTerminalManager {
	return &deviceTerminalManager{dial: dial, events: events, channels: make(map[string]*deviceTerminalChannel)}
}

func (m *deviceTerminalManager) client(deviceID, stateDigest string) *supervisor.Client {
	return supervisor.NewRemoteClient(func(ctx context.Context) (net.Conn, error) {
		return m.dial(ctx, deviceID)
	}, stateDigest)
}

// open 建会话并 attach: 自生成稳定 id+attempt (与 WEB openDeviceTerminal 同一
// 合同), create 失败按 attempt 回收结果不确定的会话, attach 失败按身份 kill。
func (m *deviceTerminalManager) open(ctx context.Context, deviceID, stateDigest string, cols, rows uint32) (*deviceTerminalOpenResult, error) {
	client := m.client(deviceID, stateDigest)
	sessionID := ids.New()
	attempt := ids.New()
	created, err := client.Create(ctx, supervisor.CreateOptions{ID: sessionID, Attempt: attempt, Cols: cols, Rows: rows})
	if err != nil {
		reapCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = client.ReconcileCreate(reapCtx, sessionID, attempt)
		return nil, err
	}
	identity := supervisor.Identity{CreatedAt: created.CreatedAt, Incarnation: created.Incarnation}
	stream, err := client.Attach(ctx, sessionID, &identity)
	if err != nil {
		reapCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = client.Kill(reapCtx, sessionID, &identity)
		return nil, err
	}
	channel, err := m.attach(stream)
	if err != nil {
		_ = stream.Kill(context.Background())
		return nil, err
	}
	return &deviceTerminalOpenResult{channelID: channel.id, info: created, identity: identity}, nil
}

// resume 用期望身份重 attach 既有会话; 不匹配回 identity 错误, 会话保留。
func (m *deviceTerminalManager) resume(ctx context.Context, deviceID, stateDigest, sessionID string, identity supervisor.Identity) (*deviceTerminalOpenResult, error) {
	client := m.client(deviceID, stateDigest)
	stream, err := client.Attach(ctx, sessionID, &identity)
	if err != nil {
		return nil, err
	}
	channel, err := m.attach(stream)
	if err != nil {
		_ = stream.Close()
		return nil, err
	}
	return &deviceTerminalOpenResult{channelID: channel.id, info: stream.Info(), identity: identity}, nil
}

func (m *deviceTerminalManager) attach(stream *supervisor.Stream) (*deviceTerminalChannel, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ipc.NewError(ipc.CodeDisconnected, "设备终端服务已关闭")
	}
	channel := &deviceTerminalChannel{id: ids.New(), stream: stream}
	m.channels[channel.id] = channel
	m.mu.Unlock()
	return channel, nil
}

// begin 启动通道的输出泵: open/resume 只建通道不推流, 前端的事件订阅就位后
// 经 device_terminal_begin 显式开闸 (wails 事件在订阅注册前发出即丢; Stream
// readLoop 从 attach 起已在缓冲, 开闸前的 output/exit 不丢)。幂等。
func (m *deviceTerminalManager) begin(channelID string) error {
	channel, err := m.channel(channelID)
	if err != nil {
		return err
	}
	channel.pumpOnce.Do(func() {
		m.wg.Add(1)
		go m.pump(channel)
	})
	return nil
}

// pump 把 attach 流的 output 推成事件直到结束: EOF (设备发 exit) 先取退出态
// 发 exit 再发干净 closed; 本地 close/kill 触发的 ErrClosed 静默结束; 其余
// 读失败按错误码发断线 closed。
func (m *deviceTerminalManager) pump(channel *deviceTerminalChannel) {
	defer m.wg.Done()
	defer m.remove(channel)
	buffer := make([]byte, 32*1024)
	for {
		count, err := channel.stream.Read(buffer)
		if count > 0 {
			data := make([]byte, count)
			copy(data, buffer[:count])
			m.emit(TopicDeviceTerminalOutput, DeviceTerminalOutputEvent{ChannelID: channel.id, Data: data})
		}
		if err == nil {
			continue
		}
		if errors.Is(err, supervisor.ErrClosed) {
			return
		}
		if errors.Is(err, io.EOF) {
			waitErr := channel.stream.Wait(context.Background())
			if errors.Is(waitErr, supervisor.ErrClosed) {
				return
			}
			exit := DeviceTerminalExitEvent{ChannelID: channel.id}
			var exitErr *base.ExitError
			if errors.As(waitErr, &exitErr) {
				exit.Code = exitErr.Code
				exit.Signal = exitErr.Signal
			}
			m.emit(TopicDeviceTerminalExit, exit)
			m.emit(TopicDeviceTerminalClosed, DeviceTerminalClosedEvent{ChannelID: channel.id})
			return
		}
		m.emit(TopicDeviceTerminalClosed, DeviceTerminalClosedEvent{ChannelID: channel.id, Code: deviceTerminalErrorCode(err), Message: err.Error()})
		return
	}
}

func deviceTerminalErrorCode(err error) string {
	switch {
	case errors.Is(err, supervisor.ErrProtocol):
		return "protocol"
	case errors.Is(err, supervisor.ErrNotFound):
		return "not_found"
	case errors.Is(err, supervisor.ErrIdentity):
		return "identity"
	case errors.Is(err, supervisor.ErrExited):
		return "exited"
	case errors.Is(err, supervisor.ErrUnavailable):
		return "unavailable"
	case errors.Is(err, supervisor.ErrStateMismatch):
		return "state_mismatch"
	default:
		return "disconnected"
	}
}

func (m *deviceTerminalManager) channel(channelID string) (*deviceTerminalChannel, error) {
	m.mu.Lock()
	channel := m.channels[channelID]
	m.mu.Unlock()
	if channel == nil {
		return nil, ipc.NewError(ipc.CodeNotFound, "设备终端通道不存在或已关闭")
	}
	return channel, nil
}

func (m *deviceTerminalManager) input(channelID string, data []byte) (int, error) {
	channel, err := m.channel(channelID)
	if err != nil {
		return 0, err
	}
	return channel.stream.Write(data)
}

func (m *deviceTerminalManager) resize(ctx context.Context, channelID string, cols, rows uint32) error {
	channel, err := m.channel(channelID)
	if err != nil {
		return err
	}
	return channel.stream.Resize(ctx, cols, rows)
}

// kill 结束 attach 的会话本身 (frame 14); 通道随 Stream.Kill 内部的 Close 关闭。
func (m *deviceTerminalManager) kill(ctx context.Context, channelID string) error {
	channel, err := m.channel(channelID)
	if err != nil {
		return err
	}
	return channel.stream.Kill(ctx)
}

// killSession 在独立控制桥接上按 id+期望身份结束会话 (显式确认后调用)。
func (m *deviceTerminalManager) killSession(ctx context.Context, deviceID, stateDigest, sessionID string, identity *supervisor.Identity) error {
	return m.client(deviceID, stateDigest).Kill(ctx, sessionID, identity)
}

// close 是 detach 语义: 设备端会话保留, 通道移除。
func (m *deviceTerminalManager) close(channelID string) error {
	channel, err := m.channel(channelID)
	if err != nil {
		return err
	}
	m.remove(channel)
	_ = channel.stream.Close()
	return nil
}

func (m *deviceTerminalManager) remove(channel *deviceTerminalChannel) {
	channel.once.Do(func() {
		m.mu.Lock()
		if m.channels[channel.id] == channel {
			delete(m.channels, channel.id)
		}
		m.mu.Unlock()
	})
}

func (m *deviceTerminalManager) emit(topic ipc.Topic, payload any) {
	_ = ipc.Emit(context.Background(), m.events, topic, payload)
}

// Shutdown 关闭全部通道 (detach 语义) 并等 pump 退出。
func (m *deviceTerminalManager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	channels := make([]*deviceTerminalChannel, 0, len(m.channels))
	for _, channel := range m.channels {
		channels = append(channels, channel)
	}
	m.mu.Unlock()
	for _, channel := range channels {
		m.remove(channel)
		_ = channel.stream.Close()
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

func (m *deviceTerminalManager) Start(context.Context) error { return nil }

var _ Component = (*deviceTerminalManager)(nil)
