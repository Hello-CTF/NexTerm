package session

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/hub"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

const (
	KindLocal  = "local"
	KindSSH    = "ssh"
	KindDocker = "docker"
	KindWinRM  = "winrm"

	TopicSessionStatus     = "session://status"
	TopicTerminalExit      = "terminal://exit"
	TopicTerminalControl   = "terminal://control"
	TopicTerminalThrottled = "terminal://throttled"
)

var (
	ErrSessionNotFound = errors.New("session not found")
	ErrTabNotFound     = errors.New("terminal tab not found")
	ErrSessionClosed   = errors.New("session closed")
	ErrTabClosed       = errors.New("terminal tab closed")
	ErrNotController   = errors.New("not_controller")
	ErrUnsupported     = errors.New("session capability unsupported")
	ErrDisconnected    = errors.New("session disconnected")
	ErrInvalidSize     = errors.New("invalid terminal size")
	ErrStaleGeneration = errors.New("stale session generation")
	ErrHidden          = errors.New("terminal hidden")
	ErrTabExists       = errors.New("terminal tab already exists")
	ErrInvalidOptions  = errors.New("invalid terminal options")
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
	ID       string
	Name     string
	Kind     string
	Encoding string
	Options  any
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
	Connector        Connector
	Terminals        TerminalFactory
	Durable          base.DurableProvider
	DurableResolver  DurableResolver
	Hub              *hub.Hub
	Emitter          Emitter
	Transcripts      TranscriptSink
	NewID            func() string
	IdleTimeout      time.Duration
	SweepInterval    time.Duration
	ReconnectMax     int
	ReconnectBackoff []time.Duration
	DefaultReplay    int
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

	asset Asset

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

	durableMu       sync.Mutex
	durableProvider base.DurableProvider
	durableGen      uint64
}

func (s *Session) statusEventLocked(status Status, cause error) StatusEvent {
	s.eventVersion++
	event := StatusEvent{SessionID: s.ID, Status: status, Version: s.eventVersion}
	if cause != nil {
		event.Error = cause.Error()
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
}

func reconnectable(kind string) bool {
	return kind == KindSSH || kind == KindDocker || kind == KindWinRM
}

func ptyBacked(kind string) bool {
	return kind == KindLocal || kind == KindSSH || kind == KindDocker
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
