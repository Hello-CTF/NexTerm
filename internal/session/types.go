package session

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Hello-CTF/NexTerm/internal/hub"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/ssh"
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
	ErrUnsupported          = errors.New("session capability unsupported")
	ErrDisconnected         = errors.New("session disconnected")
	ErrInvalidSize          = errors.New("invalid terminal size")
	ErrStaleGeneration      = errors.New("stale session generation")
	ErrAssetSessionConflict = errors.New("asset already has an active session")
	ErrForbidden            = errors.New("forbidden")
	ErrHidden               = errors.New("terminal hidden")
	ErrTabExists            = errors.New("terminal tab already exists")
	ErrInvalidOptions       = errors.New("invalid terminal options")
	ErrInvalidTitle         = errors.New("invalid terminal tab title")
	ErrDurableDisabled      = errors.New("SSH durable terminals disabled for this asset")
	ErrAmbiguousDurable     = errors.New("durable terminal ID exists on multiple sessions")
	ErrRecoveryOverrun      = errors.New("refused suffix exceeds the retained recovery window")
	// ErrDurableUnrecoverable reports a durable tab without a recorded
	// incarnation: checked attach cannot verify it, and unchecked attach no longer exists.
	ErrDurableUnrecoverable = errors.New("durable terminal cannot be recovered: no recorded incarnation")
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
	ID                string
	Name              string
	Kind              string
	Encoding          string
	StartupCommand    string
	TerminalMode      string
	Env               map[string]string
	Options           any
	StoreBacked       bool   `json:"-"`
	ConfigFingerprint string `json:"-"`
}

const (
	TerminalModeAuto    = "auto"
	TerminalModeRegular = "regular"
)

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
	Connector Connector
	Terminals TerminalFactory
	Durable   base.DurableProvider
	// LocalDurable runs local terminals through this machine's helper; it
	// takes precedence over Durable for local sessions.
	LocalDurable LocalDurableSource
	// LocalTerminalMode returns this device's choice for new local terminals
	// (TerminalModeRegular keeps them in the app); nil means automatic.
	LocalTerminalMode    func(context.Context) (string, error)
	DurableResolver      DurableResolver
	TerminalModeResolver func(context.Context, string) (string, error)
	// DurableHistoryLimit returns the recorded output limit for new durable
	// tabs of the context's owner; nil or zero uses the helper default.
	DurableHistoryLimit func(context.Context) uint64
	TranscriptOffsets   OwnerScopedDurableTranscriptOffsetStore
	TabTitles           OwnerScopedDurableTabTitleStore
	Hub                 *hub.Hub
	Emitter             Emitter
	Transcripts         TranscriptSink
	Commands            CommandSink
	Logger              *slog.Logger
	Now                 func() time.Time
	IdleTimeout         time.Duration
	SweepInterval       time.Duration
	ReconnectMax        int
	ReconnectBackoff    []time.Duration
	DefaultReplay       int
	SlowConsumerGrace   time.Duration
	OpenAccess          bool
	// Liveness tunes the per-session connection heartbeat.
	Liveness LivenessPolicy
}

type SessionInfo struct {
	ID                     string     `json:"id"`
	AssetID                string     `json:"assetId"`
	Name                   string     `json:"name"`
	Kind                   string     `json:"kind"`
	Status                 Status     `json:"status"`
	Tabs                   []string   `json:"tabs"`
	Generation             uint64     `json:"generation"`
	CreatedAt              time.Time  `json:"createdAt"`
	Error                  string     `json:"error,omitempty"`
	ErrorCode              ipc.Code   `json:"errorCode,omitempty"`
	Attempt                int        `json:"attempt,omitempty"`
	NextRetryAt            *time.Time `json:"nextRetryAt,omitempty"`
	Liveness               Liveness   `json:"liveness,omitempty"`
	StaleConfig            bool       `json:"staleConfig"`
	AssetConfigFingerprint string     `json:"-"`
}

type Shell string

const (
	ShellUnknown    Shell = "unknown"
	ShellPowerShell Shell = "powershell"
	ShellCmd        Shell = "cmd"
	ShellPOSIX      Shell = "posix"
)

type TabInfo struct {
	ID           string `json:"id"`
	SessionID    string `json:"sessionId"`
	Cols         uint32 `json:"cols"`
	Rows         uint32 `json:"rows"`
	GridRevision uint64 `json:"gridRevision"`
	// SizeOwner is the client whose terminal size the PTY follows: the
	// client that typed last after the handoff hold, or the first to attach.
	// Other clients render the PTY size. Empty when no attached client owns it.
	SizeOwner string `json:"sizeOwner,omitempty"`
	// GuestControl is the preview guest holding input control.
	GuestControl string `json:"guestControl,omitempty"`
	Subscribers  int    `json:"subscribers"`
	Viewers      int    `json:"viewers"`
	Exited       bool   `json:"exited"`
	Retainable   bool   `json:"retainable"`
	Ephemeral    bool   `json:"ephemeral,omitempty"`
	Durable      bool   `json:"durable,omitempty"`
	Cwd          string `json:"cwd,omitempty"`
	Encoding     string `json:"encoding,omitempty"`
	Title        string `json:"title,omitempty"`
	// TitleCustom reports whether Title is a custom title (a manual or AI
	// rename, or a restored durable title) rather than the OSC dynamic title.
	TitleCustom bool `json:"titleCustom,omitempty"`
	// TitleChangedAt is the client time (Unix ms) of the last explicit title
	// decision, so clients can order it against their own; zero when unknown.
	TitleChangedAt  int64  `json:"titleChangedAt,omitempty"`
	Shell           Shell  `json:"shell"`
	LastOutputAt    int64  `json:"lastOutputAt,omitempty"`
	LastOutputMSAgo int64  `json:"-"`
	FallbackNotice  string `json:"-"`
	// AttachmentID is the unique incarnation of this attachment, established
	// at attach time and bound to the ACK and the floor high-water.
	AttachmentID uint64 `json:"attachmentId,omitempty"`
	// CreatedAt is when this process opened the tab (Unix ms). DetachedAt is
	// when its last subscriber left, or CreatedAt when none ever attached;
	// zero while subscribed. Both are in memory only.
	CreatedAt  int64 `json:"createdAt,omitempty"`
	DetachedAt int64 `json:"detachedAt,omitempty"`
	// DetachedMSAgo is the age of DetachedAt when a listing was taken.
	DetachedMSAgo int64 `json:"-"`
	// DetachedSinceOpen reports that DetachedAt is when this app run reopened
	// an existing helper session that no client has left since.
	DetachedSinceOpen bool `json:"detachedSinceOpen,omitempty"`
	// State is the helper's attention state of a durable tab; set only by
	// listings and empty when the helper reports none.
	State string `json:"state,omitempty"`
}

func (t TabInfo) MarshalJSON() ([]byte, error) {
	type Alias TabInfo
	return json.Marshal(struct {
		Alias
		SizeOwner *string `json:"sizeOwner"`
	}{Alias: Alias(t), SizeOwner: nullableClient(t.SizeOwner)})
}

type StatusEvent struct {
	SessionID   string     `json:"sessionId"`
	Status      Status     `json:"status"`
	Error       string     `json:"error,omitempty"`
	ErrorCode   ipc.Code   `json:"errorCode,omitempty"`
	Attempt     int        `json:"attempt,omitempty"`
	NextRetryAt *time.Time `json:"nextRetryAt,omitempty"`
	// Liveness is "suspect" while a connected session's heartbeats go
	// unanswered and is omitted otherwise.
	Liveness Liveness `json:"liveness,omitempty"`
	Version  uint64   `json:"version"`
}

type ExitEvent struct {
	TabID    string `json:"tabId"`
	ExitCode *int   `json:"exitCode"`
	Version  uint64 `json:"version"`
}

type ControlEvent struct {
	TabID          string `json:"tabId"`
	Cols           uint32 `json:"cols"`
	Rows           uint32 `json:"rows"`
	GridRevision   uint64 `json:"gridRevision"`
	SizeOwner      string `json:"sizeOwner,omitempty"`
	GuestControl   string `json:"guestControl,omitempty"`
	Subscribers    int    `json:"subscribers"`
	Viewers        int    `json:"viewers"`
	Exited         bool   `json:"exited"`
	Version        uint64 `json:"version"`
	Cwd            string `json:"cwd,omitempty"`
	Durable        bool   `json:"durable,omitempty"`
	Title          string `json:"title,omitempty"`
	TitleCustom    bool   `json:"titleCustom,omitempty"`
	TitleChangedAt int64  `json:"titleChangedAt,omitempty"`
	// HistoryFrom is the helper offset of a durable tab's first rendered
	// byte: output before it can be read with ReadDurableHistoryFor.
	// HistoryBase is the oldest offset the helper kept at the last attach,
	// and HistoryTruncated reports that output was dropped by the helper's
	// history limit, before HistoryBase or within the stream.
	HistoryFrom      uint64 `json:"historyFrom,omitempty"`
	HistoryBase      uint64 `json:"historyBase,omitempty"`
	HistoryTruncated bool   `json:"historyTruncated,omitempty"`
}

func (e ControlEvent) MarshalJSON() ([]byte, error) {
	type Alias ControlEvent
	return json.Marshal(struct {
		Alias
		SizeOwner *string `json:"sizeOwner"`
	}{Alias: Alias(e), SizeOwner: nullableClient(e.SizeOwner)})
}

func nullableClient(client string) *string {
	if client == "" {
		return nil
	}
	return &client
}

type ThrottleEvent struct {
	TabID         string `json:"tabId"`
	ChannelID     string `json:"channelId"`
	InflightBytes int    `json:"inflightBytes"`
	Recovered     bool   `json:"recovered,omitempty"`
	// Resync reports that ChannelID was disconnected because it could not
	// keep up; its client resumes from its accepted offset. It is sent
	// before the channel closes and replaces the channel's throttle state.
	Resync  bool   `json:"resync,omitempty"`
	Version uint64 `json:"version"`
}

type reconnectCompletion struct {
	done chan struct{}
	err  error
}

type Session struct {
	ID        string
	CreatedAt time.Time

	asset         Asset
	assetSnapshot atomic.Pointer[Asset]
	userID        string

	mu                  sync.Mutex
	status              Status
	staleConfig         bool
	transport           *transportHandle
	generation          uint64
	eventVersion        uint64
	ctx                 context.Context
	cancel              context.CancelFunc
	tabs                map[string]*Tab
	idleSince           time.Time
	closed              bool
	reapRetry           bool
	connectDone         chan struct{}
	connectErr          error
	connectOwnerExpired bool
	connectFinished     bool
	reconnecting        bool
	reconnectCompletion *reconnectCompletion
	reconnectAttempt    int
	reconnectNextRetry  time.Time
	// reconnectWake cuts the current reconnect backoff short (foreground
	// probe); it belongs to the running reconnect loop.
	reconnectWake chan struct{}
	statusErr     error
	// liveness tracks the heartbeat of the connection of generation
	// livenessGen.
	liveness    livenessTracker
	livenessGen uint64

	durableMu            sync.Mutex
	helperCandidateMu    sync.Mutex
	durableProvider      base.DurableProvider
	durableGen           uint64
	durableTransport     *transportHandle
	durableFailure       error
	durableFailureAt     time.Time
	durableCreateFailure error
	fallbackNotified     bool
	fallbackNoticeGen    uint64
	// localFallbackNotice is the last local helper reason shown on this session.
	localFallbackNotice string

	// Idle-reap state for remote session shells (KindRemote): remoteActivity is the last feed/management
	// operation time, remoteFeeds is the active feed connection count (reaping suspends while >0); unused by normal sessions.
	remoteActivity time.Time
	remoteFeeds    int
}

type assetOwnerKey struct {
	assetID    string
	ownerScope string
}

func (s *Session) assetOwnerKey() assetOwnerKey {
	return assetOwnerKey{assetID: s.asset.ID, ownerScope: s.userID}
}

func (s *Session) statusEventLocked(status Status, cause error) StatusEvent {
	if status == StatusConnecting || status == StatusConnected || (status == StatusDisconnected && cause == nil) {
		s.reconnectAttempt = 0
	}
	if status != StatusReconnecting {
		s.reconnectNextRetry = time.Time{}
	}
	s.statusErr = cause
	s.eventVersion++
	event := StatusEvent{SessionID: s.ID, Status: status, Attempt: s.reconnectAttempt, Liveness: s.livenessLabelLocked(status), Version: s.eventVersion}
	if !s.reconnectNextRetry.IsZero() {
		next := s.reconnectNextRetry
		event.NextRetryAt = &next
	}
	if cause != nil {
		event.Error = userMessage(cause)
		event.ErrorCode = sessionStatusErrorCode(cause)
	}
	return event
}

func sessionStatusErrorCode(err error) ipc.Code {
	var connectErr *ssh.ConnectError
	if errors.As(err, &connectErr) {
		switch connectErr.Kind {
		case ssh.ErrorKindConfig:
			return ipc.CodeBadParam
		case ssh.ErrorKindAuth, ssh.ErrorKindHostKeyChanged:
			return ipc.Code(connectErr.Kind)
		case ssh.ErrorKindHostKeyPending:
			return ipc.CodeHostKeyPending
		case ssh.ErrorKindTimeout:
			return ipc.CodeTimeout
		default:
			return ipc.CodeSSH
		}
	}
	var hostKeyErr *ssh.HostKeyError
	if errors.As(err, &hostKeyErr) {
		if hostKeyErr.Pending {
			return ipc.CodeHostKeyPending
		}
		return ipc.Code(ssh.ErrorKindHostKeyChanged)
	}
	var appErr *ipc.Error
	if errors.As(err, &appErr) {
		return appErr.Code
	}
	if errors.Is(err, context.Canceled) {
		return ipc.CodeDisconnected
	}
	return IPCError(err).Code
}

func (s *Session) Info() SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	tabs := make([]string, 0, len(s.tabs))
	for id := range s.tabs {
		tabs = append(tabs, id)
	}
	sort.Strings(tabs)
	asset := s.Asset()
	info := SessionInfo{
		ID: s.ID, AssetID: asset.ID, Name: asset.Name, Kind: asset.Kind,
		Status: s.status, Tabs: tabs, Generation: s.generation, CreatedAt: s.CreatedAt,
		Attempt: s.reconnectAttempt, Liveness: s.livenessLabelLocked(s.status), StaleConfig: s.staleConfig, AssetConfigFingerprint: asset.ConfigFingerprint,
	}
	if !s.reconnectNextRetry.IsZero() {
		next := s.reconnectNextRetry
		info.NextRetryAt = &next
	}
	if s.statusErr != nil {
		info.Error = userMessage(s.statusErr)
		info.ErrorCode = sessionStatusErrorCode(s.statusErr)
	}
	return info
}

func (s *Session) Asset() Asset {
	if snapshot := s.assetSnapshot.Load(); snapshot != nil {
		return *snapshot
	}
	return s.asset
}

type OpenTabOptions struct {
	TabID      string
	SessionID  string
	ClientID   string
	ChannelID  string
	Cols       uint32
	Rows       uint32
	Term       string
	Ephemeral  bool
	Retainable bool
	Durable    *DurableTabOptions
	// DurableFallback permits a new SSH tab to use a regular PTY when the remote
	// helper cannot be initialized. Recovered tabs never use this path.
	DurableFallback bool
	// Detached opens the tab without attaching a channel: ChannelID must be
	// empty, the tab starts with no subscribers, and clients attach later via
	// AttachTab. Used by CreateTab.
	Detached bool
}

type DurableTabOptions struct {
	Command           []string
	Dir               string
	Env               []string
	Recover           bool
	Identity          *base.DurableIdentity
	Title             string
	MetadataRevision  uint64
	CanonicalMetadata bool
	// RecoveredExited carries the authoritative dead state from the descriptor used for recovery.
	RecoveredExited bool
}

type durableRenameAttempt struct {
	requestID                string
	incarnation              string
	expectedMetadataRevision uint64
	title                    string
	changedAt                int64
	completed                bool
	resultTitle              string
	resultMetadataRevision   uint64
	err                      error
}

type AttachOptions struct {
	ClientID    string
	ChannelID   string
	ReplayBytes int
	// FromSeq/FromEpoch are the client's anchors into the consumed output stream: FromSeq is a ring byte sequence number,
	// FromEpoch is the stream epoch delivered with the last attach's anchor frame. With both provided, the server attempts
	// to resume from the FromSeq offset (no clear screen); if the anchor is stale or the epoch mismatches, it falls back
	// to a full clear-screen replay and marks expired explicitly in the anchor frame. Zero values keep the existing full-replay behavior.
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

// createTabCommandSupported reports whether a detached CreateTab shell accepts
// a startup line written into its stdin: any interactive-shell kind (local
// PTY, ssh, docker, device daemon PTY) does; line-mode winrm does not.
func createTabCommandSupported(kind string) bool {
	return startupCommandSupported(kind) || kind == KindLocal || kind == KindDevice
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

// maxTabTitleRunes bounds custom tab titles; the frontend sanitizer caps at
// the same length.
const maxTabTitleRunes = 80

// blankTabTitle reports a rename that clears the custom title.
func blankTabTitle(title string) bool {
	return strings.TrimSpace(title) == ""
}

func normalizeTabTitle(title string) (string, error) {
	trimmed := strings.TrimSpace(title)
	if trimmed == "" {
		return "", ErrInvalidTitle
	}
	if utf8.RuneCountInString(trimmed) > maxTabTitleRunes {
		return "", ErrInvalidTitle
	}
	for _, r := range trimmed {
		if r < 0x20 || r == 0x7f {
			return "", ErrInvalidTitle
		}
	}
	return trimmed, nil
}

func clientID(id string) string {
	if id == "" {
		return "desktop"
	}
	return id
}

type openAccessContextKey struct{}

func WithOpenAccess(ctx context.Context) context.Context {
	return context.WithValue(ctx, openAccessContextKey{}, true)
}

func openAccessFromContext(ctx context.Context) bool {
	open, _ := ctx.Value(openAccessContextKey{}).(bool)
	return open
}

// userIDFromContext captures the account user that opened a session, when the
// call chain carries one (server account sessions); desktop and background
// callers yield "" and the command log stores NULL.
func userIDFromContext(ctx context.Context) string {
	userID, _ := ipc.UserIDFromContext(ctx)
	return userID
}

// sessionOwnerAllowed reports whether the requester may access the session: device pseudo-asset IDs are deterministic,
// so without an owner gate they would be reachable cross-user with zero authorization; device sessions must be isolated by owner.
// Behavior is unchanged when both sides are empty (desktop/background). The caller must hold session.mu.
func sessionOwnerAllowed(session *Session, requester string) bool {
	if session.asset.Kind != KindDevice {
		return true
	}
	return session.userID == "" || requester == "" || session.userID == requester
}

// sessionRequestAllowed applies account ownership at request-aware entry points.
// Empty requesters require an explicit open deployment scope or trusted marker.
func sessionRequestAllowed(session *Session, requester string, openAccess bool) bool {
	if requester == "" {
		return openAccess
	}
	return session.userID != "" && session.userID == requester
}
