package session

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/hub"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

const (
	KindLocal  = "local"
	KindSSH    = "ssh"
	KindDocker = "docker"
	KindWinRM  = "winrm"
	KindDevice = "device"

	TopicSessionStatus     = "session://status"
	TopicTerminalExit      = "terminal://exit"
	TopicTerminalControl   = "terminal://control"
	TopicTerminalThrottled = "terminal://throttled"
)

var (
	ErrSessionNotFound      = errors.New("session not found")
	ErrTabNotFound          = errors.New("terminal tab not found")
	ErrSessionClosed        = errors.New("session closed")
	ErrTabClosed            = errors.New("terminal tab closed")
	ErrNotController        = errors.New("not_controller")
	ErrUnsupported          = errors.New("session capability unsupported")
	ErrDisconnected         = errors.New("session disconnected")
	ErrInvalidSize          = errors.New("invalid terminal size")
	ErrStaleGeneration      = errors.New("stale session generation")
	ErrAssetSessionConflict = errors.New("asset already has an active session")
	ErrForbidden            = errors.New("forbidden")
	ErrHidden               = errors.New("terminal hidden")
	ErrTabExists            = errors.New("terminal tab already exists")
	ErrInvalidOptions       = errors.New("invalid terminal options")
)

type Status string

const (
	StatusConnecting   Status = "connecting"
	StatusConnected    Status = "connected"
	StatusReconnecting Status = "reconnecting"
	StatusDisconnected Status = "disconnected"
	StatusFailed       Status = "failed"
)

type Asset struct {
	ID             string
	Name           string
	Kind           string
	Encoding       string
	StartupCommand string
	Options        any
}

type Connector interface {
	Connect(context.Context, Asset, uint64) (base.Transport, error)
}

// DurableResolver resolves the durable provider for a session whose transport
// is not local, such as the supervisor daemon reached through the session's
// SSH channel. Implementations must tolerate repeated calls; the manager
// caches the result per session generation.
type DurableResolver interface {
	ResolveDurable(ctx context.Context, transport base.Transport) (base.DurableProvider, error)
}

type DurableResolverFunc func(context.Context, base.Transport) (base.DurableProvider, error)

func (f DurableResolverFunc) ResolveDurable(ctx context.Context, transport base.Transport) (base.DurableProvider, error) {
	return f(ctx, transport)
}

// DurableLister enumerates the durable tab IDs a provider owns. Providers
// that cannot enumerate return nothing through this interface.
type DurableLister interface {
	ListDurable(ctx context.Context) ([]string, error)
}

type ConnectorFunc func(context.Context, Asset, uint64) (base.Transport, error)

func (f ConnectorFunc) Connect(ctx context.Context, asset Asset, generation uint64) (base.Transport, error) {
	return f(ctx, asset, generation)
}

type TerminalConfig struct {
	TabID     string
	SessionID string
	Cols      uint32
	Rows      uint32
	Encoding  string
}

type TerminalState interface {
	Feed([]byte)
	Dump(maxBytes int) []byte
	Resize(cols, rows int) error
	SetResponseHandler(func([]byte))
	Close()
}

type TerminalFactory interface {
	NewTerminal(TerminalConfig) (TerminalState, error)
}

type TerminalFactoryFunc func(TerminalConfig) (TerminalState, error)

func (f TerminalFactoryFunc) NewTerminal(config TerminalConfig) (TerminalState, error) {
	return f(config)
}

type Event struct {
	Topic   string
	Payload any
}

type Emitter interface {
	EmitSessionEvent(context.Context, Event) error
}

type EmitterFunc func(context.Context, Event) error

func (f EmitterFunc) EmitSessionEvent(ctx context.Context, event Event) error {
	return f(ctx, event)
}

type Config struct {
	Connector         Connector
	Terminals         TerminalFactory
	Durable           base.DurableProvider
	DurableResolver   DurableResolver
	TranscriptOffsets DurableTranscriptOffsetStore
	Hub               *hub.Hub
	Emitter           Emitter
	Transcripts       TranscriptSink
	Commands          CommandSink
	Logger            *slog.Logger
	IdleTimeout       time.Duration
	SweepInterval     time.Duration
	ReconnectMax      int
	ReconnectBackoff  []time.Duration
	DefaultReplay     int
	SlowConsumerGrace time.Duration
}

type SessionInfo struct {
	ID         string    `json:"id"`
	AssetID    string    `json:"assetId"`
	Name       string    `json:"name"`
	Kind       string    `json:"kind"`
	Status     Status    `json:"status"`
	Tabs       []string  `json:"tabs"`
	Generation uint64    `json:"generation"`
	CreatedAt  time.Time `json:"createdAt"`
}

type TabInfo struct {
	ID           string `json:"id"`
	SessionID    string `json:"sessionId"`
	Cols         uint32 `json:"cols"`
	Rows         uint32 `json:"rows"`
	GridRevision uint64 `json:"gridRevision"`
	Controller   string `json:"controller,omitempty"`
	Subscribers  int    `json:"subscribers"`
	Viewers      int    `json:"viewers"`
	Exited       bool   `json:"exited"`
	Ephemeral    bool   `json:"ephemeral,omitempty"`
	Durable      bool   `json:"durable,omitempty"`
	Cwd          string `json:"cwd,omitempty"`
	Encoding     string `json:"encoding,omitempty"`
}

func (t TabInfo) MarshalJSON() ([]byte, error) {
	type Alias TabInfo
	return json.Marshal(struct {
		Alias
		Controller *string `json:"controller"`
	}{Alias: Alias(t), Controller: nullableController(t.Controller)})
}

type StatusEvent struct {
	SessionID string `json:"sessionId"`
	Status    Status `json:"status"`
	Error     string `json:"error,omitempty"`
	Version   uint64 `json:"version"`
}

type ExitEvent struct {
	TabID    string `json:"tabId"`
	ExitCode *int   `json:"exitCode"`
	Version  uint64 `json:"version"`
}

type ControlEvent struct {
	TabID        string `json:"tabId"`
	Cols         uint32 `json:"cols"`
	Rows         uint32 `json:"rows"`
	GridRevision uint64 `json:"gridRevision"`
	Controller   string `json:"controller,omitempty"`
	Subscribers  int    `json:"subscribers"`
	Viewers      int    `json:"viewers"`
	Exited       bool   `json:"exited"`
	Version      uint64 `json:"version"`
	Cwd          string `json:"cwd,omitempty"`
	Durable      bool   `json:"durable,omitempty"`
}

func (e ControlEvent) MarshalJSON() ([]byte, error) {
	type Alias ControlEvent
	return json.Marshal(struct {
		Alias
		Controller *string `json:"controller"`
	}{Alias: Alias(e), Controller: nullableController(e.Controller)})
}

func nullableController(controller string) *string {
	if controller == "" {
		return nil
	}
	return &controller
}

type ThrottleEvent struct {
	TabID         string `json:"tabId"`
	ChannelID     string `json:"channelId"`
	InflightBytes int    `json:"inflightBytes"`
	Recovered     bool   `json:"recovered,omitempty"`
	Version       uint64 `json:"version"`
}

type Session struct {
	ID        string
	CreatedAt time.Time

	asset  Asset
	userID string

	mu              sync.Mutex
	status          Status
	transport       *transportHandle
	generation      uint64
	eventVersion    uint64
	ctx             context.Context
	cancel          context.CancelFunc
	tabs            map[string]*Tab
	idleSince       time.Time
	closed          bool
	connectDone     chan struct{}
	connectErr      error
	connectFinished bool
	reconnecting    bool
	reconnectDone   chan struct{}
	reconnectErr    error

	durableMu        sync.Mutex
	durableProvider  base.DurableProvider
	durableGen       uint64
	durableTransport *transportHandle

	// 远程会话壳 (KindRemote) 的空闲回收状态: remoteActivity 是上次推流/管理
	// 操作时刻, remoteFeeds 是活跃推流连接数 (>0 时回收挂起); 普通会话不用。
	remoteActivity time.Time
	remoteFeeds    int
}

func (s *Session) statusEventLocked(status Status, cause error) StatusEvent {
	s.eventVersion++
	event := StatusEvent{SessionID: s.ID, Status: status, Version: s.eventVersion}
	if cause != nil {
		event.Error = userMessage(cause)
	}
	return event
}

func (s *Session) Info() SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	tabs := make([]string, 0, len(s.tabs))
	for id := range s.tabs {
		tabs = append(tabs, id)
	}
	sort.Strings(tabs)
	return SessionInfo{
		ID: s.ID, AssetID: s.asset.ID, Name: s.asset.Name, Kind: s.asset.Kind,
		Status: s.status, Tabs: tabs, Generation: s.generation, CreatedAt: s.CreatedAt,
	}
}

func (s *Session) Asset() Asset {
	return s.asset
}

type OpenTabOptions struct {
	TabID     string
	SessionID string
	ClientID  string
	ChannelID string
	Cols      uint32
	Rows      uint32
	Term      string
	Ephemeral bool
	Durable   *DurableTabOptions
}

type DurableTabOptions struct {
	Command []string
	Dir     string
	Env     []string
	Recover bool
}

type AttachOptions struct {
	ClientID    string
	ChannelID   string
	ReplayBytes int
	// FromSeq/FromEpoch 是客户端已消费输出流的锚点: FromSeq 为 ring 字节序号,
	// FromEpoch 为上次 attach 锚点帧下发的流纪元。两者都提供时服务端尝试从
	// FromSeq 偏移续传 (不清屏); 锚点过期或纪元不符则回退全量清屏重放并在
	// 锚点帧里显式标记 expired。零值保持既有全量重放行为。
	FromSeq   uint64
	FromEpoch string
}

func reconnectable(kind string) bool {
	return kind == KindSSH || kind == KindDocker || kind == KindWinRM || kind == KindDevice
}

func ptyBacked(kind string) bool {
	return kind == KindLocal || kind == KindSSH || kind == KindDocker || kind == KindDevice
}

func startupCommandSupported(kind string) bool {
	return kind == KindSSH || kind == KindDocker
}

func canonicalEncoding(encoding string) string {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "utf-8", "utf8":
		return "utf-8"
	case "latin1", "iso-8859-1":
		return "latin1"
	default:
		return strings.ToLower(strings.TrimSpace(encoding))
	}
}

func validSize(cols, rows uint32) bool {
	return cols > 0 && rows > 0 && cols <= 1024 && rows <= 1024
}

func clientID(id string) string {
	if id == "" {
		return "desktop"
	}
	return id
}

// userIDFromContext captures the account user that opened a session, when the
// call chain carries one (server account sessions); desktop and background
// callers yield "" and the command log stores NULL.
func userIDFromContext(ctx context.Context) string {
	userID, _ := ipc.UserIDFromContext(ctx)
	return userID
}

// sessionOwnerAllowed 报告请求者是否可访问会话: 设备伪资产 ID 是确定性的,
// 无属主闸则跨用户零授权可达, 设备会话必须按属主隔离; 双端皆空 (桌面/后台)
// 行为不变。调用方须持有 session.mu。
func sessionOwnerAllowed(session *Session, requester string) bool {
	if session.asset.Kind != KindDevice {
		return true
	}
	return session.userID == "" || requester == "" || session.userID == requester
}
