package production

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/docker"
	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/localhelper"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/terminal"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/ssh"
)

type terminalCommandService struct {
	database     *store.Store
	sessions     *session.Manager
	docker       *docker.Service
	bridge       *terminalBridge
	hostKeys     *productionHostKeyStore
	sshConnector *productionConnector
	events       ipc.Emitter
	stagedBlobs  StagedBlobResolver
	fsUploads    *fsChunkUploadRegistry
	desktop      bool
	helpers      *productionHelperService
	databases    databaseLoginTester
	// localHelper is set when local terminals run through the local helper.
	localHelper *localhelper.Service

	mu         sync.Mutex
	dockerTabs map[string]*dockerTabInfo
	sinks      map[string]ipc.BinaryStream
}

// databaseLoginTester opens and closes a stored database asset without registering the handle.
type databaseLoginTester interface {
	TestAsset(ctx context.Context, assetID string, timeout time.Duration) error
}

type dockerTabInfo struct {
	sessionID string
	cols      uint
	rows      uint
	// sizeOwner is the client whose size the exec follows: the client that
	// typed last, or the one that opened it.
	sizeOwner string
	channels  map[string]string
}

type sessionConnectRequest struct {
	AssetID       string `json:"assetId"`
	AcceptHostKey bool   `json:"acceptHostKey"`
}

type sessionConnectDeviceRequest struct {
	DeviceID string `json:"deviceId"`
	Name     string `json:"name"`
}

type sessionIDRequest struct {
	SessionID string `json:"sessionId"`
}

type sessionProbeRequest struct {
	Host      string `json:"host"`
	Port      int    `json:"port"`
	TimeoutMS int64  `json:"timeoutMs"`
}

type sessionProbeResult struct {
	Open  bool   `json:"open"`
	Error string `json:"error,omitempty"`
}

type sessionInfoDTO struct {
	ID          string     `json:"id"`
	AssetID     *string    `json:"assetId"`
	Name        string     `json:"name"`
	Kind        string     `json:"kind"`
	Status      string     `json:"status"`
	Tabs        []string   `json:"tabs"`
	CreatedAt   int64      `json:"createdAt"`
	Error       string     `json:"error,omitempty"`
	ErrorCode   ipc.Code   `json:"errorCode,omitempty"`
	Attempt     int        `json:"attempt,omitempty"`
	NextRetryAt *time.Time `json:"nextRetryAt,omitempty"`
	// Liveness is "suspect" while a connected session's heartbeats go
	// unanswered and is omitted otherwise.
	Liveness    string `json:"liveness,omitempty"`
	StaleConfig bool   `json:"staleConfig"`
}

type openTerminalRequest struct {
	SessionID string `json:"sessionId"`
	Cols      uint32 `json:"cols"`
	Rows      uint32 `json:"rows"`
	Durable   *bool  `json:"durable,omitempty"`
}

type helperOperationRequest struct {
	SessionID       string `json:"sessionId"`
	OperationID     string `json:"operationId,omitempty"`
	Action          string `json:"action"`
	InterruptActive bool   `json:"interruptActive,omitempty"`
	// ConsentToken is the consentToken of the legacy helper the user agreed
	// to end, for the replace_legacy action.
	ConsentToken string `json:"consentToken,omitempty"`
}

// durableReadinessDTO reports whether a new terminal can be durable now: ready, preparing, unavailable (with a
// reason) or disabled when the asset or account chose regular terminals.
type durableReadinessDTO struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

type helperOfflineRequest struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
	SHA256    string `json:"sha256,omitempty"`
	Confirmed bool   `json:"confirmed"`
}

type durableDescriptorDTO struct {
	ID               string `json:"id"`
	Incarnation      string `json:"incarnation"`
	CreatedAt        int64  `json:"createdAt"`
	Title            string `json:"title,omitempty"`
	MetadataRevision uint64 `json:"metadataRevision"`
	Cols             uint32 `json:"cols"`
	Rows             uint32 `json:"rows"`
	Dead             bool   `json:"dead"`
	ExitCode         *int   `json:"exitCode"`
	Signal           string `json:"signal,omitempty"`
	FinishedAt       *int64 `json:"finishedAt"`
	// State is the helper's attention state; omitted when it reports none.
	State string `json:"state,omitempty"`
	// Activity is omitted when the helper predates activity reporting.
	Activity *durableActivityDTO `json:"activity,omitempty"`
}

// durableActivityDTO carries Unix ms times on the host's clock; a nil time
// is unknown to the helper. The ages and asOf are measured on that clock too,
// so clients show ages and rebase times instead of comparing clocks; they are
// nil for a helper that reports times without ages.
type durableActivityDTO struct {
	LastOutputAt    *int64 `json:"lastOutputAt"`
	DetachedAt      *int64 `json:"detachedAt"`
	Attached        bool   `json:"attached"`
	AsOf            *int64 `json:"asOf,omitempty"`
	LastOutputMSAgo *int64 `json:"lastOutputMsAgo,omitempty"`
	DetachedMSAgo   *int64 `json:"detachedMsAgo,omitempty"`
}

func newDurableActivityDTO(activity *base.DurableActivity) *durableActivityDTO {
	if activity == nil {
		return nil
	}
	dto := &durableActivityDTO{
		LastOutputAt:    unixMilliPointer(activity.LastOutputAt),
		DetachedAt:      unixMilliPointer(activity.DetachedAt),
		Attached:        activity.Attached,
		LastOutputMSAgo: activity.LastOutputMSAgo,
		DetachedMSAgo:   activity.DetachedMSAgo,
	}
	if !activity.AsOf.IsZero() {
		dto.AsOf = unixMilliPointer(&activity.AsOf)
	}
	return dto
}

func unixMilliPointer(at *time.Time) *int64 {
	if at == nil {
		return nil
	}
	value := at.UnixMilli()
	return &value
}

type terminalListRequest struct {
	SessionID string `json:"sessionId,omitempty"`
	Durable   bool   `json:"durable,omitempty"`
}

type attachTerminalRequest struct {
	TabID       string `json:"tabId"`
	SessionID   string `json:"sessionId,omitempty"`
	ReplayBytes int    `json:"replayBytes"`
	FromSeq     uint64 `json:"fromSeq,omitempty"`
	FromEpoch   string `json:"fromEpoch,omitempty"`
}

type attachedTabDTO struct {
	TabID          string  `json:"tabId"`
	SessionID      string  `json:"sessionId"`
	Cols           uint32  `json:"cols"`
	Rows           uint32  `json:"rows"`
	SizeOwner      *string `json:"sizeOwner"`
	Subscribers    int     `json:"subscribers"`
	Viewers        int     `json:"viewers"`
	Exited         bool    `json:"exited"`
	Retainable     bool    `json:"retainable"`
	Durable        bool    `json:"durable"`
	Ephemeral      bool    `json:"ephemeral"`
	Encoding       string  `json:"encoding,omitempty"`
	Title          string  `json:"title,omitempty"`
	TitleCustom    bool    `json:"titleCustom,omitempty"`
	TitleChangedAt int64   `json:"titleChangedAt,omitempty"`
	Shell          string  `json:"shell"`
	FallbackNotice string  `json:"fallbackNotice,omitempty"`
	AttachmentID   uint64  `json:"attachmentId,omitempty"`
}

type renameTerminalRequest struct {
	TabID string `json:"tabId"`
	Title string `json:"title"`
	// At is the client's decision time (Unix ms) that orders title decisions
	// across devices; omitted means now.
	At int64 `json:"at,omitempty"`
}

// attentionSeenRequest names the attention state the user saw by the since
// value of its event.
type attentionSeenRequest struct {
	TabID string `json:"tabId"`
	Since string `json:"since"`
}

// promptTapRequest answers the detected prompt of an attention event: the
// generation, epoch and fingerprint come from that event's prompt and
// option indexes its options.
type promptTapRequest struct {
	TabID       string `json:"tabId"`
	ClientID    string `json:"clientId,omitempty"`
	Generation  string `json:"generation"`
	Epoch       uint64 `json:"epoch"`
	Fingerprint string `json:"fingerprint"`
	Option      int    `json:"option"`
}

type createTerminalRequest struct {
	SessionID string `json:"sessionId"`
	Cols      uint32 `json:"cols"`
	Rows      uint32 `json:"rows"`
	Title     string `json:"title"`
	Command   string `json:"command"`
}

type terminalWriteRequest struct {
	TabID     string                         `json:"tabId"`
	Data      []int                          `json:"data"`
	ClientID  string                         `json:"clientId"`
	Broadcast *terminalBroadcastWriteOptions `json:"broadcast,omitempty"`
}

type terminalResizeRequest struct {
	TabID    string `json:"tabId"`
	ClientID string `json:"clientId"`
	Cols     uint32 `json:"cols"`
	Rows     uint32 `json:"rows"`
}

type terminalIDRequest struct {
	TabID     string `json:"tabId"`
	SessionID string `json:"sessionId,omitempty"`
	ChannelID string `json:"channelId"`
	Mode      string `json:"mode"`
	N         int    `json:"n"`

	MaxBytes int    `json:"maxBytes"`
	Visible  bool   `json:"visible"`
	Encoding string `json:"encoding"`
	Path     string `json:"path"`
	Line     string `json:"line"`
}

type terminalHistoryReadRequest struct {
	TabID string `json:"tabId"`
	// Before is the exclusive end offset of the requested output.
	Before   uint64 `json:"before"`
	MaxBytes int    `json:"maxBytes"`
	// Latest reads up to the newest output instead of Before.
	Latest bool `json:"latest"`
}

// terminalHistoryDTO is recorded output of a durable tab in [start, end), in
// the helper's absolute offsets. truncated means the helper dropped the output
// before start under its history limit.
type terminalHistoryDTO struct {
	Start       uint64 `json:"start"`
	End         uint64 `json:"end"`
	HistoryBase uint64 `json:"historyBase"`
	HistoryNext uint64 `json:"historyNext"`
	Truncated   bool   `json:"truncated"`
	DataBase64  string `json:"dataBase64"`
}

type terminalRecoverRequest struct {
	TabID     string `json:"tabId"`
	ChannelID string `json:"channelId"`
	ClientID  string `json:"clientId"`
	// AttachmentID is the unique incarnation of this attachment, established
	// at attach time; a stale report from a previous incarnation is rejected.
	AttachmentID uint64 `json:"attachmentId"`
	// AcceptedBytes is the client's absolute accepted stream offset for this
	// channel; the recovery ring replays everything flushed after it.
	AcceptedBytes int64 `json:"acceptedBytes"`
}

type terminalPauseRequest struct {
	TabID     string `json:"tabId"`
	ChannelID string `json:"channelId"`
	ClientID  string `json:"clientId"`
	// AttachmentID is the unique incarnation of this attachment, established
	// at attach time; a stale report from a previous incarnation is rejected.
	AttachmentID uint64 `json:"attachmentId"`
	// AcceptedBytes is the client's absolute accepted stream offset when
	// pausing; it opens the recovery ring's retention floor. Negative leaves
	// the floor unchanged.
	AcceptedBytes int64 `json:"acceptedBytes"`
}

type terminalResumeRequest struct {
	TabID     string `json:"tabId"`
	ChannelID string `json:"channelId"`
	ClientID  string `json:"clientId"`
	// AttachmentID is the unique incarnation of this attachment, established
	// at attach time; a stale report from a previous incarnation is rejected.
	AttachmentID uint64 `json:"attachmentId"`
	// EpisodeToken is the generation of the refusal episode this resume
	// answers, as issued by the pause or recover that opened it. A resume
	// carrying a previous episode's token is rejected: it must not clear the
	// current episode's retention floor.
	EpisodeToken uint64 `json:"episodeToken"`
}

type terminalStreamAckRequest struct {
	TabID     string `json:"tabId"`
	ChannelID string `json:"channelId"`
	ClientID  string `json:"clientId"`
	// AttachmentID is the unique incarnation of this attachment, established
	// at attach time; a stale report from a previous incarnation is rejected.
	AttachmentID uint64 `json:"attachmentId"`
	// AcceptedBytes is the client's absolute accepted stream offset for this
	// channel; it opens the recovery ring's retention floor. The floor only
	// advances.
	AcceptedBytes int64 `json:"acceptedBytes"`
}

// streamControlCaller resolves the trusted caller for a stream-control
// command (pause, resume, recover, and the accepted-offset ACK). The caller
// is bound to the attachment: when both the environment and the payload name
// a caller, they must agree — a payload cannot override the environment
// caller; a single source (environment-only or payload-only) is a legitimate
// fallback; an empty caller is rejected.
func streamControlCaller(call *ipc.Call, payloadClient string) (string, error) {
	if call.ClientID != "" && payloadClient != "" && call.ClientID != payloadClient {
		return "", session.ErrForbidden
	}
	client := call.ClientID
	if client == "" {
		client = payloadClient
	}
	if client == "" {
		return "", session.ErrForbidden
	}
	return client, nil
}

type liveTabDTO struct {
	TabID           string  `json:"tabId"`
	SessionID       string  `json:"sessionId"`
	SessionName     string  `json:"sessionName"`
	SessionKind     string  `json:"sessionKind"`
	Cols            uint32  `json:"cols"`
	Rows            uint32  `json:"rows"`
	SizeOwner       *string `json:"sizeOwner"`
	Subscribers     int     `json:"subscribers"`
	Viewers         int     `json:"viewers"`
	Exited          bool    `json:"exited"`
	LastOutputAt    int64   `json:"lastOutputAt"`
	LastOutputMSAgo int64   `json:"lastOutputMsAgo"`
	Title           string  `json:"title,omitempty"`
	TitleCustom     bool    `json:"titleCustom,omitempty"`
	TitleChangedAt  int64   `json:"titleChangedAt,omitempty"`
	CreatedAt       int64   `json:"createdAt,omitempty"`
	DetachedAt      int64   `json:"detachedAt,omitempty"`
	DetachedMSAgo   int64   `json:"detachedMsAgo,omitempty"`
	// DetachedSinceOpen: the detach time is only when this app reopened the helper session.
	DetachedSinceOpen bool   `json:"detachedSinceOpen,omitempty"`
	Durable           bool   `json:"durable,omitempty"`
	State             string `json:"state,omitempty"`
}

func newTerminalCommandService(database *store.Store, sessions *session.Manager, dockerService *docker.Service, bridge *terminalBridge, hostKeys *productionHostKeyStore, sshConnector *productionConnector, events ipc.Emitter, stagedBlobs StagedBlobResolver, desktop bool) *terminalCommandService {
	return &terminalCommandService{
		database: database, sessions: sessions, docker: dockerService, bridge: bridge,
		dockerTabs: make(map[string]*dockerTabInfo), sinks: make(map[string]ipc.BinaryStream), hostKeys: hostKeys, sshConnector: sshConnector,
		events: events, stagedBlobs: stagedBlobs, desktop: desktop,
	}
}

func (s *terminalCommandService) registerSession(dispatcher *ipc.Dispatcher) error {
	registrations := []func() error{
		func() error {
			return ipc.RegisterNested(dispatcher, "session_connect", func(ctx context.Context, _ *ipc.Call, input sessionConnectRequest) (sessionInfoDTO, error) {
				ctx = productionSessionRequestContext(ctx)
				row, err := s.database.AssetGetLive(ctx, input.AssetID)
				if err != nil {
					return sessionInfoDTO{}, err
				}
				asset, err := productionSessionAsset(row, input.AcceptHostKey, ipc.OwnerID(ctx))
				if err != nil {
					return sessionInfoDTO{}, err
				}
				connected, err := s.sessions.ConnectFor(ctx, asset)
				if err != nil {
					return sessionInfoDTO{}, terminalIPCError(err)
				}
				infos := []session.SessionInfo{connected.Info()}
				s.refreshStaleConfigs(ctx, infos)
				return productionSessionInfo(infos[0]), nil
			})
		},
		// Quick connect lets the server initiate SSH directly to arbitrary host:ports; in the server-side assembly (desktop=false)
		// it is always disabled, so no logged-in user can pivot through the server to arbitrary intranet addresses. Desktop single-user mode stays available.
		func() error {
			return ipc.RegisterNested(dispatcher, "session_connect_quick", func(ctx context.Context, _ *ipc.Call, input sessionQuickConnectRequest) (sessionInfoDTO, error) {
				if !s.desktop {
					return sessionInfoDTO{}, ipc.NewError(ipc.CodeUnsupported, "快速连接只在桌面端可用")
				}
				return s.connectQuick(ctx, input)
			})
		},
		// A device is not an asset table record: it joins the session system as a pseudo asset (id=device/<deviceId>),
		// with dialing/authorization done by the device dialing surface held by the connector, not via asset options and credentials.
		// The authenticated user is written into options: reconnects run in a background ctx, and authorization falls back to reading it.
		func() error {
			return ipc.RegisterNested(dispatcher, "session_connect_device", func(ctx context.Context, _ *ipc.Call, input sessionConnectDeviceRequest) (sessionInfoDTO, error) {
				ctx = productionSessionRequestContext(ctx)
				if input.DeviceID == "" {
					return sessionInfoDTO{}, ipc.BadParam(errors.New("deviceId 不能为空"))
				}
				name := strings.TrimSpace(input.Name)
				if name == "" {
					name = input.DeviceID
				}
				userID, _ := ipc.UserIDFromContext(ctx)
				asset := session.Asset{
					ID:      "device/" + input.DeviceID,
					Name:    name,
					Kind:    session.KindDevice,
					Options: map[string]any{"deviceId": input.DeviceID, "userId": userID},
				}
				connected, err := s.sessions.ConnectFor(ctx, asset)
				if err != nil {
					return sessionInfoDTO{}, terminalIPCError(err)
				}
				return productionSessionInfo(connected.Info()), nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "session_quick_connect_user", func(_ context.Context, _ *ipc.Call, _ struct{}) (string, error) {
				if !s.desktop {
					return "", ipc.NewError(ipc.CodeUnsupported, "快速连接只在桌面端可用")
				}
				return quickConnectDefaultUsername(), nil
			})
		},
		func() error {
			// The machine name of a local session's host, so terminal file:// links
			// can be matched against it rather than a host a nested shell reports.
			return ipc.Register(dispatcher, "session_local_hostname", func(ctx context.Context, _ *ipc.Call, input sessionIDRequest) (string, error) {
				ctx = productionSessionRequestContext(ctx)
				connected, err := s.sessions.SessionFor(ctx, input.SessionID)
				if err != nil {
					return "", terminalIPCError(err)
				}
				if connected.Asset().Kind != session.KindLocal {
					return "", ipc.NewError(ipc.CodeUnsupported, "只有本机会话提供本机主机名")
				}
				name, err := os.Hostname()
				if err != nil {
					return "", ipc.NewError(ipc.CodeUnsupported, "无法读取本机主机名")
				}
				return name, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "session_connect_local", func(ctx context.Context, _ *ipc.Call, _ struct{}) (sessionInfoDTO, error) {
				ctx = productionSessionRequestContext(ctx)
				connected, err := s.connectLocal(ctx)
				if err != nil {
					return sessionInfoDTO{}, terminalIPCError(err)
				}
				return productionSessionInfo(connected.Info()), nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "session_disconnect", func(ctx context.Context, _ *ipc.Call, input sessionIDRequest) (any, error) {
				ctx = productionSessionRequestContext(ctx)
				if err := s.sessions.DisconnectFor(ctx, input.SessionID); err != nil {
					return nil, terminalIPCError(err)
				}
				if s.docker != nil {
					s.closeDockerSession(ctx, input.SessionID)
				}
				return nil, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "session_reconnect", func(ctx context.Context, _ *ipc.Call, input sessionIDRequest) (bool, error) {
				ctx = productionSessionRequestContext(ctx)
				connected, err := s.sessions.SessionFor(ctx, input.SessionID)
				if err != nil {
					return false, terminalIPCError(err)
				}
				asset := connected.Asset()
				if asset.StoreBacked {
					row, loadErr := s.database.AssetGetLive(ctx, asset.ID)
					if loadErr != nil {
						return false, loadErr
					}
					asset, err = productionSessionAsset(row, false, ipc.OwnerID(ctx))
					if err != nil {
						return false, err
					}
					err = s.sessions.StartReconnectWithAssetFor(ctx, input.SessionID, asset)
				} else {
					err = s.sessions.StartReconnectFor(ctx, input.SessionID)
				}
				if err != nil {
					return false, terminalIPCError(err)
				}
				if s.docker != nil {
					s.closeDockerSession(ctx, input.SessionID)
				}
				return true, nil
			})
		},
		func() error {
			// session_liveness_probe checks a session's connection now (the client
			// returned to the foreground): a dead connection reconnects at once and
			// a pending reconnect backoff is cut short.
			return ipc.Register(dispatcher, "session_liveness_probe", func(ctx context.Context, _ *ipc.Call, input sessionIDRequest) (sessionInfoDTO, error) {
				ctx = productionSessionRequestContext(ctx)
				info, err := s.sessions.ProbeLivenessFor(ctx, input.SessionID)
				if err != nil {
					return sessionInfoDTO{}, terminalIPCError(err)
				}
				return productionSessionInfo(info), nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "session_list", func(ctx context.Context, _ *ipc.Call, _ struct{}) ([]sessionInfoDTO, error) {
				ctx = productionSessionRequestContext(ctx)
				infos, err := s.sessions.ListSessionsFor(ctx)
				if err != nil {
					return nil, terminalIPCError(err)
				}
				s.refreshStaleConfigs(ctx, infos)
				result := make([]sessionInfoDTO, len(infos))
				for index, info := range infos {
					result[index] = productionSessionInfo(info)
				}
				return result, nil
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "session_probe", func(ctx context.Context, _ *ipc.Call, input sessionProbeRequest) (sessionProbeResult, error) {
				timeout := time.Duration(input.TimeoutMS) * time.Millisecond
				if timeout <= 0 {
					timeout = 3 * time.Second
				}
				dialCtx, cancel := context.WithTimeout(ctx, timeout)
				defer cancel()
				connection, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", net.JoinHostPort(input.Host, strconv.Itoa(input.Port)))
				if err != nil {
					return sessionProbeResult{Open: false, Error: err.Error()}, nil
				}
				_ = connection.Close()
				return sessionProbeResult{Open: true}, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "session_open_line_tab", func(ctx context.Context, call *ipc.Call, input openTerminalRequest) (attachedTabDTO, error) {
				ctx = productionSessionRequestContext(ctx)
				bridged, err := s.bridge.bridge(call.Channel.ID)
				if err != nil {
					return attachedTabDTO{}, terminalIPCError(err)
				}
				info, err := s.sessions.OpenTabFor(context.WithoutCancel(ctx), session.OpenTabOptions{
					SessionID: input.SessionID, ClientID: call.ClientID, ChannelID: call.Channel.ID,
					Cols: input.Cols, Rows: input.Rows, Ephemeral: true,
				})
				if err != nil {
					if bridged {
						s.bridge.Unbridge(call.Channel.ID)
					}
					return attachedTabDTO{}, terminalIPCError(err)
				}
				return attachedSessionTab(info), nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "session_line_exec", func(ctx context.Context, call *ipc.Call, input terminalIDRequest) (any, error) {
				ctx = productionSessionRequestContext(ctx)
				_, err := s.sessions.ExecLineFor(ctx, input.TabID, call.ClientID, input.Line)
				return nil, terminalIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "terminal_attach", func(ctx context.Context, call *ipc.Call, input openTerminalRequest) (attachedTabDTO, error) {
				ctx = productionSessionRequestContext(ctx)
				connected, err := s.sessions.SessionFor(ctx, input.SessionID)
				if err != nil {
					return attachedTabDTO{}, terminalIPCError(err)
				}
				asset := connected.Asset()
				kind := asset.Kind
				mode, err := s.sessions.TerminalModeFor(ctx, input.SessionID)
				if err != nil {
					return attachedTabDTO{}, terminalIPCError(err)
				}
				ephemeral := kind == session.KindLocal
				retainable := ephemeral
				var durable *session.DurableTabOptions
				fallback := false
				useDurable := kind == session.KindDevice || kind == session.KindSSH && mode != session.TerminalModeRegular
				if kind == session.KindSSH && input.Durable != nil {
					if mode == session.TerminalModeRegular && *input.Durable {
						return attachedTabDTO{}, terminalIPCError(session.ErrDurableDisabled)
					}
					useDurable = *input.Durable
				}
				// A local terminal is durable by default when this app runs a
				// local helper; an explicit regular request or a regular local
				// asset keeps it in this process.
				localDurable := kind == session.KindLocal && s.localHelper != nil && mode != session.TerminalModeRegular && (input.Durable == nil || *input.Durable)
				if localDurable {
					useDurable, ephemeral, retainable = true, false, false
				}
				if useDurable {
					durable = &session.DurableTabOptions{}
					fallback = (kind == session.KindSSH || kind == session.KindLocal) && input.Durable == nil
				}
				bridged, err := s.bridge.bridge(call.Channel.ID)
				if err != nil {
					return attachedTabDTO{}, terminalIPCError(err)
				}
				info, err := s.sessions.OpenTabFor(context.WithoutCancel(ctx), session.OpenTabOptions{
					SessionID: input.SessionID, ClientID: call.ClientID, ChannelID: call.Channel.ID, Cols: input.Cols, Rows: input.Rows,
					Ephemeral: ephemeral, Retainable: retainable, Durable: durable, DurableFallback: fallback,
				})
				if err != nil {
					if bridged {
						s.bridge.Unbridge(call.Channel.ID)
					}
					return attachedTabDTO{}, terminalIPCError(err)
				}
				return attachedSessionTab(info), nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "terminal_attach_tab", func(ctx context.Context, call *ipc.Call, input attachTerminalRequest) (attachedTabDTO, error) {
				ctx = productionSessionRequestContext(ctx)
				tab, lookupErr := s.sessions.TabFor(ctx, input.TabID)
				if lookupErr != nil && !errors.Is(lookupErr, session.ErrTabNotFound) {
					return attachedTabDTO{}, terminalIPCError(lookupErr)
				}
				if lookupErr == nil && (input.SessionID == "" || tab.SessionID == input.SessionID) {
					if input.SessionID == "" && tab.Info().Durable {
						infos, err := s.sessions.ListSessionsFor(ctx)
						if err != nil {
							return attachedTabDTO{}, terminalIPCError(err)
						}
						remoteIDs := make([]string, 0, len(infos))
						for _, info := range infos {
							if s.durableHostKind(info.Kind) && info.Status == session.StatusConnected {
								remoteIDs = append(remoteIDs, info.ID)
							}
						}
						if len(remoteIDs) > 1 {
							owners, err := s.sessions.RemoteDurableOwners(ctx, input.TabID, remoteIDs)
							if err != nil {
								return attachedTabDTO{}, terminalIPCError(err)
							}
							if len(owners) > 1 {
								return attachedTabDTO{}, terminalIPCError(session.ErrAmbiguousDurable)
							}
							if len(owners) == 1 && owners[0] != tab.SessionID {
								return attachedTabDTO{}, terminalIPCError(session.ErrTabNotFound)
							}
						}
					}
					bridged, err := s.bridge.bridge(call.Channel.ID)
					if err != nil {
						return attachedTabDTO{}, terminalIPCError(err)
					}
					replayBytes := input.ReplayBytes
					if replayBytes == 0 {
						replayBytes = -1
					}
					info, err := s.sessions.AttachTabFor(ctx, input.TabID, session.AttachOptions{
						ClientID: call.ClientID, ChannelID: call.Channel.ID, ReplayBytes: replayBytes,
						FromSeq: input.FromSeq, FromEpoch: input.FromEpoch,
					})
					if err == nil {
						return attachedSessionTab(info), nil
					}
					if bridged {
						s.bridge.Unbridge(call.Channel.ID)
					}
					if !errors.Is(err, session.ErrTabNotFound) {
						return attachedTabDTO{}, terminalIPCError(err)
					}
				}
				if input.SessionID == "" && s.docker != nil {
					if _, _, statusErr := s.docker.StreamStatus(input.TabID); statusErr == nil {
						if err := s.authorizeDockerTab(ctx, input.TabID); err != nil {
							return attachedTabDTO{}, terminalIPCError(err)
						}
						return s.attachDocker(ctx, call, input.TabID)
					}
				}
				if ids.Valid(input.TabID) {
					recovered, recoveryErr := s.recoverRemoteDurable(ctx, call, input.TabID, input.SessionID)
					if recoveryErr == nil {
						return recovered, nil
					}
					if !errors.Is(recoveryErr, session.ErrTabNotFound) {
						return attachedTabDTO{}, terminalIPCError(recoveryErr)
					}
					return attachedTabDTO{}, terminalIPCError(durable.ErrNotFound)
				}
				if !validTabID(input.TabID) {
					return attachedTabDTO{}, ipc.BadParam(errors.New("invalid terminal tab id"))
				}
				return attachedTabDTO{}, terminalIPCError(session.ErrTabNotFound)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "terminal_broadcast_prepare", func(ctx context.Context, _ *ipc.Call, input terminalBroadcastPrepareRequest) (any, error) {
				ctx = productionSessionRequestContext(ctx)
				data, err := productionBytes(input.Data)
				if err != nil {
					return nil, err
				}
				return nil, broadcastConfirmationIPCError(s.prepareBroadcast(ctx, input.TabIDs, data, input.ProductionConfirmation))
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "terminal_write", func(ctx context.Context, call *ipc.Call, input terminalWriteRequest) (any, error) {
				ctx = productionSessionRequestContext(ctx)
				data, err := productionBytes(input.Data)
				if err != nil {
					return nil, err
				}
				if err := s.authorizeBroadcastWrite(ctx, input.TabID, data, input.Broadcast); err != nil {
					return nil, broadcastConfirmationIPCError(err)
				}
				client := gridRequestClient(call.ClientID, input.ClientID)
				err = s.sessions.WriteFor(ctx, input.TabID, client, data)
				if errors.Is(err, session.ErrTabNotFound) && s.docker != nil {
					if authErr := s.authorizeDockerTab(ctx, input.TabID); authErr != nil {
						err = authErr
					} else {
						err = s.writeDocker(ctx, input.TabID, client, data)
					}
				}
				return nil, terminalIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, session.CommandTerminalResize, func(ctx context.Context, call *ipc.Call, input terminalResizeRequest) (any, error) {
				ctx = productionSessionRequestContext(ctx)
				client := gridRequestClient(call.ClientID, input.ClientID)
				err := s.sessions.ResizeFor(ctx, input.TabID, client, input.Cols, input.Rows)
				if errors.Is(err, session.ErrTabNotFound) && s.docker != nil {
					if authErr := s.authorizeDockerTab(ctx, input.TabID); authErr != nil {
						err = authErr
					} else {
						err = s.resizeDocker(ctx, input.TabID, client, uint(input.Cols), uint(input.Rows))
					}
				}
				return nil, terminalIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, session.CommandTerminalResizeFlush, func(ctx context.Context, call *ipc.Call, input terminalResizeRequest) (any, error) {
				ctx = productionSessionRequestContext(ctx)
				client := gridRequestClient(call.ClientID, input.ClientID)
				return nil, terminalIPCError(s.sessions.FlushResizeFor(ctx, input.TabID, client))
			})
		},
		func() error {
			return ipc.Register(dispatcher, "terminal_detach", func(ctx context.Context, _ *ipc.Call, input terminalIDRequest) (any, error) {
				ctx = productionSessionRequestContext(ctx)
				var err error
				if input.ChannelID != "" {
					err = s.sessions.DetachChannelFor(ctx, input.ChannelID)
					if errors.Is(err, session.ErrForbidden) {
						return nil, terminalIPCError(err)
					}
					s.bridge.Unbridge(input.ChannelID)
				} else {
					err = s.sessions.DetachAllFor(ctx, input.TabID)
				}
				if errors.Is(err, session.ErrTabNotFound) && s.docker != nil {
					if authErr := s.authorizeDockerTab(ctx, input.TabID); authErr != nil {
						err = authErr
					} else {
						err = s.detachDocker(input.TabID, input.ChannelID)
					}
				}
				return nil, terminalIPCError(err)
			})
		},
		func() error {
			// Client-driven producer backpressure: pause/resume one channel's
			// output feed without detaching (ephemeral local/WinRM tabs stay alive).
			// Both carry the trusted caller and the attachment incarnation, bound
			// to the attached subscriber like the accepted-offset ACK.
			return ipc.Register(dispatcher, "terminal_pause", func(ctx context.Context, call *ipc.Call, input terminalPauseRequest) (any, error) {
				ctx = productionSessionRequestContext(ctx)
				client, err := streamControlCaller(call, input.ClientID)
				if err != nil {
					return nil, terminalIPCError(err)
				}
				episodeToken, err := s.sessions.PauseChannelFor(ctx, input.TabID, input.ChannelID, client, input.AttachmentID, input.AcceptedBytes)
				if err != nil {
					return nil, terminalIPCError(err)
				}
				// The episode the client must reference on resume; joining the
				// already-open episode is idempotent, so a retried pause
				// returns the same token.
				return map[string]any{"episodeToken": episodeToken}, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "terminal_resume", func(ctx context.Context, call *ipc.Call, input terminalResumeRequest) (any, error) {
				ctx = productionSessionRequestContext(ctx)
				client, err := streamControlCaller(call, input.ClientID)
				if err != nil {
					return nil, terminalIPCError(err)
				}
				return nil, terminalIPCError(s.sessions.ResumeChannelFor(ctx, input.TabID, input.ChannelID, client, input.AttachmentID, input.EpisodeToken))
			})
		},
		func() error {
			// Redeliver the suffix a saturated client refused, from the tab's
			// bounded recovery ring, without reaping an ephemeral tab. The
			// trusted caller and attachment incarnation are bound to the
			// attached subscriber like the accepted-offset ACK.
			return ipc.Register(dispatcher, "terminal_recover", func(ctx context.Context, call *ipc.Call, input terminalRecoverRequest) (any, error) {
				ctx = productionSessionRequestContext(ctx)
				client, err := streamControlCaller(call, input.ClientID)
				if err != nil {
					return nil, terminalIPCError(err)
				}
				flushed, episodeToken, err := s.sessions.RecoverChannelFor(ctx, input.TabID, input.ChannelID, client, input.AttachmentID, input.AcceptedBytes)
				if err != nil {
					return nil, terminalIPCError(err)
				}
				return map[string]any{"flushedOffset": flushed, "episodeToken": episodeToken}, nil
			})
		},
		func() error {
			// The client's accepted-offset ACK for one channel: opens the
			// recovery ring's retention floor without waiting for a pause/recover
			// RPC. The caller must own the tab and be attached to the channel.
			return ipc.Register(dispatcher, "terminal_stream_ack", func(ctx context.Context, call *ipc.Call, input terminalStreamAckRequest) (any, error) {
				ctx = productionSessionRequestContext(ctx)
				client, err := streamControlCaller(call, input.ClientID)
				if err != nil {
					return nil, terminalIPCError(err)
				}
				return nil, terminalIPCError(s.sessions.StreamAckFor(ctx, input.TabID, input.ChannelID, client, input.AttachmentID, input.AcceptedBytes))
			})
		},
		func() error {
			return ipc.Register(dispatcher, "terminal_list", func(ctx context.Context, _ *ipc.Call, input terminalListRequest) (any, error) {
				ctx = productionSessionRequestContext(ctx)
				if input.Durable {
					if _, err := s.sessions.SessionFor(ctx, input.SessionID); err != nil {
						return nil, terminalIPCError(err)
					}
					descriptors, err := s.sessions.ListRemoteDurable(ctx, input.SessionID)
					if err != nil {
						return nil, terminalIPCError(err)
					}
					result := make([]durableDescriptorDTO, 0, len(descriptors))
					for _, descriptor := range descriptors {
						item := durableDescriptorDTO{
							ID: descriptor.ID, Incarnation: descriptor.Incarnation, CreatedAt: descriptor.CreatedAt.UnixMilli(),
							Title: descriptor.Title, MetadataRevision: descriptor.MetadataRevision,
							Cols: descriptor.Cols, Rows: descriptor.Rows, Dead: descriptor.Dead, ExitCode: descriptor.ExitCode, Signal: descriptor.Signal,
							Activity: newDurableActivityDTO(descriptor.Activity),
						}
						if descriptor.State != nil {
							item.State = descriptor.State.State
						}
						if descriptor.FinishedAt != nil {
							finishedAt := descriptor.FinishedAt.UnixMilli()
							item.FinishedAt = &finishedAt
						}
						result = append(result, item)
					}
					return result, nil
				}
				infos, err := s.sessions.ListTabsFor(ctx)
				if err != nil {
					return nil, terminalIPCError(err)
				}
				result := make([]liveTabDTO, 0, len(infos))
				for _, info := range infos {
					connected, err := s.sessions.SessionFor(ctx, info.SessionID)
					if err != nil {
						continue
					}
					sessionInfo := connected.Info()
					result = append(result, liveTabDTO{
						TabID: info.ID, SessionID: info.SessionID, SessionName: sessionInfo.Name, SessionKind: sessionInfo.Kind,
						Cols: info.Cols, Rows: info.Rows, SizeOwner: nullableProductionString(info.SizeOwner),
						Subscribers: info.Subscribers, Viewers: info.Viewers, Exited: info.Exited,
						LastOutputAt: info.LastOutputAt, LastOutputMSAgo: info.LastOutputMSAgo, Title: info.Title, TitleCustom: info.TitleCustom, TitleChangedAt: info.TitleChangedAt,
						CreatedAt: info.CreatedAt, DetachedAt: info.DetachedAt, DetachedMSAgo: info.DetachedMSAgo, DetachedSinceOpen: info.DetachedSinceOpen, Durable: info.Durable, State: info.State,
					})
				}
				return result, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "terminal_screen_text", func(ctx context.Context, _ *ipc.Call, input terminalIDRequest) (string, error) {
				ctx = productionSessionRequestContext(ctx)
				value, err := s.sessions.ScreenTextFor(ctx, input.TabID)
				return value, terminalIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "terminal_snapshot", func(ctx context.Context, _ *ipc.Call, input terminalIDRequest) (any, error) {
				ctx = productionSessionRequestContext(ctx)
				value, err := s.sessions.TerminalSnapshotFor(ctx, input.TabID)
				return value, terminalIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "terminal_tail", func(ctx context.Context, _ *ipc.Call, input terminalIDRequest) ([]string, error) {
				ctx = productionSessionRequestContext(ctx)
				value, err := s.sessions.TailLinesFor(ctx, input.TabID, input.N)
				return value, terminalIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "terminal_history_read", func(ctx context.Context, _ *ipc.Call, input terminalHistoryReadRequest) (terminalHistoryDTO, error) {
				ctx = productionSessionRequestContext(ctx)
				before := input.Before
				if input.Latest {
					before = session.DurableHistoryLatest
				}
				chunk, err := s.sessions.ReadDurableHistoryFor(ctx, input.TabID, before, input.MaxBytes)
				if err != nil {
					return terminalHistoryDTO{}, terminalIPCError(err)
				}
				return terminalHistoryDTO{
					Start: chunk.Start, End: chunk.End, HistoryBase: chunk.HistoryBase, HistoryNext: chunk.HistoryNext,
					Truncated: chunk.Truncated, DataBase64: base64.StdEncoding.EncodeToString(chunk.Data),
				}, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "terminal_set_visible", func(ctx context.Context, _ *ipc.Call, input terminalIDRequest) (any, error) {
				ctx = productionSessionRequestContext(ctx)
				return nil, terminalIPCError(s.sessions.SetVisibleFor(ctx, input.TabID, input.Visible))
			})
		},
		func() error {
			return ipc.Register(dispatcher, "terminal_dump", func(ctx context.Context, _ *ipc.Call, input terminalIDRequest) (string, error) {
				ctx = productionSessionRequestContext(ctx)
				value, err := s.sessions.RawDumpFor(ctx, input.TabID, input.MaxBytes)
				return string(value), terminalIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "terminal_switch_encoding", func(ctx context.Context, _ *ipc.Call, input terminalIDRequest) (any, error) {
				ctx = productionSessionRequestContext(ctx)
				return nil, terminalIPCError(s.sessions.SwitchEncodingFor(ctx, input.TabID, input.Encoding))
			})
		},
		func() error {
			return ipc.Register(dispatcher, "terminal_record_start", func(ctx context.Context, _ *ipc.Call, input terminalIDRequest) (any, error) {
				ctx = productionSessionRequestContext(ctx)
				path, err := resolveStagedLocalPath(ctx, s.stagedBlobs, input.Path)
				if err != nil {
					return nil, err
				}
				return nil, terminalIPCError(s.sessions.StartRecordingFor(ctx, input.TabID, path))
			})
		},
		func() error {
			return ipc.Register(dispatcher, "terminal_record_stop", func(ctx context.Context, _ *ipc.Call, input terminalIDRequest) (uint64, error) {
				ctx = productionSessionRequestContext(ctx)
				value, err := s.sessions.StopRecordingFor(ctx, input.TabID)
				return value, terminalIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "terminal_record_status", func(ctx context.Context, _ *ipc.Call, input terminalIDRequest) (terminal.RecordingStatus, error) {
				ctx = productionSessionRequestContext(ctx)
				value, err := s.sessions.RecordingStatusFor(ctx, input.TabID)
				return value, terminalIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "terminal_export_log", func(ctx context.Context, _ *ipc.Call, input terminalIDRequest) (uint64, error) {
				ctx = productionSessionRequestContext(ctx)
				path, err := resolveStagedLocalPath(ctx, s.stagedBlobs, input.Path)
				if err != nil {
					return 0, err
				}
				value, err := s.sessions.ExportLogFor(ctx, input.TabID, path, input.MaxBytes)
				return value, terminalIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "terminal_close_tab", func(ctx context.Context, call *ipc.Call, input terminalIDRequest) (any, error) {
				ctx = productionSessionRequestContext(ctx)
				if input.Mode != "" && input.Mode != "detach" && input.Mode != "kill" {
					return nil, ipc.BadParam(errors.New("终端关闭模式无效"))
				}
				err := s.closeSessionTab(ctx, call, input)
				if errors.Is(err, session.ErrTabNotFound) && input.SessionID != "" {
					if _, sessionErr := s.sessions.SessionFor(ctx, input.SessionID); sessionErr != nil {
						err = sessionErr
					} else if input.Mode == "kill" {
						_, err = s.sessions.RecoverRemoteDurable(ctx, input.TabID, session.OpenTabOptions{
							SessionID: input.SessionID, ClientID: call.ClientID, Detached: true, Cols: 80, Rows: 24,
						})
						if err == nil {
							err = s.closeSessionTab(ctx, call, input)
						}
					}
				}
				if errors.Is(err, session.ErrTabNotFound) && s.docker != nil {
					if authErr := s.authorizeDockerTab(ctx, input.TabID); authErr != nil {
						if errors.Is(authErr, docker.ErrStreamClosed) {
							err = session.ErrTabNotFound
						} else {
							err = authErr
						}
					} else if input.Mode == "detach" {
						err = s.detachDocker(input.TabID, call.Channel.ID)
					} else {
						err = s.closeDocker(input.TabID)
					}
				}
				return nil, terminalIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "terminal_rename_tab", func(ctx context.Context, _ *ipc.Call, input renameTerminalRequest) (attachedTabDTO, error) {
				ctx = productionSessionRequestContext(ctx)
				info, err := s.sessions.RenameTabForAt(ctx, input.TabID, input.Title, input.At)
				if err != nil {
					return attachedTabDTO{}, terminalIPCError(err)
				}
				return attachedSessionTab(info), nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "terminal_attention_list", func(ctx context.Context, _ *ipc.Call, _ struct{}) ([]session.AttentionEvent, error) {
				ctx = productionSessionRequestContext(ctx)
				states, err := s.sessions.ListAttentionFor(ctx)
				if err != nil {
					return nil, terminalIPCError(err)
				}
				return states, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "terminal_attention_seen", func(ctx context.Context, _ *ipc.Call, input attentionSeenRequest) (any, error) {
				ctx = productionSessionRequestContext(ctx)
				return nil, terminalIPCError(s.sessions.MarkAttentionSeenFor(ctx, input.TabID, input.Since))
			})
		},
		func() error {
			return ipc.Register(dispatcher, "terminal_prompt_tap", func(ctx context.Context, call *ipc.Call, input promptTapRequest) (any, error) {
				ctx = productionSessionRequestContext(ctx)
				client := gridRequestClient(call.ClientID, input.ClientID)
				return nil, terminalIPCError(s.sessions.TapPromptFor(ctx, input.TabID, client, session.PromptTapRequest{
					Generation: input.Generation, Epoch: input.Epoch, Fingerprint: input.Fingerprint, Option: input.Option,
				}))
			})
		},
		func() error {
			return ipc.Register(dispatcher, "terminal_create_tab", func(ctx context.Context, _ *ipc.Call, input createTerminalRequest) (attachedTabDTO, error) {
				ctx = productionSessionRequestContext(ctx)
				info, err := s.sessions.CreateTabFor(context.WithoutCancel(ctx), session.CreateTabOptions{
					SessionID: input.SessionID, Cols: input.Cols, Rows: input.Rows, Title: input.Title, Command: input.Command,
				})
				if err != nil {
					return attachedTabDTO{}, terminalIPCError(err)
				}
				return attachedSessionTab(info), nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "terminal_helper_operation", func(ctx context.Context, _ *ipc.Call, input helperOperationRequest) (helperOperationDTO, error) {
				ctx = productionSessionRequestContext(ctx)
				action := input.Action
				if action == "update" && input.InterruptActive {
					action = "update_interrupt"
				}
				switch action {
				case "cancel", "retry":
					if input.OperationID == "" {
						return helperOperationDTO{}, ipc.BadParam(errors.New("持久终端操作缺少操作标识"))
					}
					var err error
					if action == "cancel" {
						err = s.sessions.CancelHelperOperation(ctx, input.SessionID, input.OperationID)
					} else {
						err = s.sessions.RetryHelperOperation(ctx, input.SessionID, input.OperationID)
					}
					if err != nil {
						return helperOperationDTO{}, terminalIPCError(err)
					}
					return helperOperationDTO{
						SessionID: input.SessionID, OperationID: input.OperationID, Action: action, Accepted: true,
						Local: helperVersionDTO{Known: false}, Running: helperVersionDTO{Known: false}, Compatibility: "unknown",
					}, nil
				case "status", "update", "update_interrupt", "replace_legacy":
					if s.helpers == nil {
						return helperOperationDTO{}, terminalIPCError(session.ErrUnsupported)
					}
					if action == "replace_legacy" && input.ConsentToken == "" {
						return helperOperationDTO{}, ipc.BadParam(errors.New("缺少升级确认"))
					}
					result, err := s.helpers.ensure(ctx, input.SessionID, action, input.ConsentToken)
					if err != nil {
						if result.Update != nil && result.Update.RolledBack {
							return result, ipc.NormalizeError(err).WithDetail(result)
						}
						return helperOperationDTO{}, terminalIPCError(err)
					}
					result.Accepted = action != "status"
					return result, nil
				default:
					return helperOperationDTO{}, ipc.BadParam(errors.New("未知的持久终端操作"))
				}
			})
		},
		func() error {
			return ipc.Register(dispatcher, "session_prepare_durable", func(ctx context.Context, _ *ipc.Call, input sessionIDRequest) (durableReadinessDTO, error) {
				ctx = productionSessionRequestContext(ctx)
				readiness, err := s.sessions.PrepareDurable(ctx, input.SessionID)
				if err != nil {
					return durableReadinessDTO{}, terminalIPCError(err)
				}
				return durableReadinessDTO{State: readiness.State, Reason: readiness.Reason}, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "terminal_helper_offline", func(ctx context.Context, _ *ipc.Call, input helperOfflineRequest) (helperOperationDTO, error) {
				ctx = productionSessionRequestContext(ctx)
				if !s.desktop || s.helpers == nil {
					return helperOperationDTO{}, terminalIPCError(session.ErrUnsupported)
				}
				result, err := s.helpers.inspectOffline(ctx, input.SessionID, input.Path, input.SHA256, input.Confirmed)
				if err != nil {
					return helperOperationDTO{}, ipc.BadParam(err)
				}
				return result, nil
			})
		},
	}
	for _, register := range registrations {
		if err := register(); err != nil {
			return err
		}
	}
	if err := s.registerSSHDiagnostics(dispatcher); err != nil {
		return err
	}
	if err := registerFSCommands(dispatcher, s.sessions, s.stagedBlobs, s.fsUploads); err != nil {
		return err
	}
	if !s.desktop {
		return nil
	}
	return registerLocalFolderCommands(dispatcher)
}

func (s *terminalCommandService) closeSessionTab(ctx context.Context, call *ipc.Call, input terminalIDRequest) error {
	if input.Mode == "detach" {
		// A regular remote PTY cannot run without its viewer; detaching would only orphan the process.
		tab, err := s.sessions.TabFor(ctx, input.TabID)
		if err != nil {
			return err
		}
		if !tab.Info().Retainable {
			return session.ErrUnsupported
		}
		return s.sessions.BackgroundTab(ctx, input.TabID, call.ClientID)
	}
	if input.Mode == "kill" {
		return s.sessions.CloseTabFor(ctx, input.TabID)
	}
	tab, err := s.sessions.TabFor(ctx, input.TabID)
	if err != nil {
		return err
	}
	if tab.Info().Exited {
		return s.sessions.CloseTabFor(ctx, input.TabID)
	}
	connected, err := s.sessions.SessionFor(ctx, tab.SessionID)
	if err != nil {
		return err
	}
	// Without an explicit mode only a durable terminal goes to the background; a regular PTY just closes.
	if kind := connected.Asset().Kind; s.durableHostKind(kind) && tab.Info().Durable {
		return s.sessions.BackgroundTab(ctx, input.TabID, call.ClientID)
	}
	return s.sessions.CloseTabFor(ctx, input.TabID)
}

func (s *terminalCommandService) recoverRemoteDurable(ctx context.Context, call *ipc.Call, tabID, sessionID string) (attachedTabDTO, error) {
	sessionIDs := []string{sessionID}
	if sessionID != "" {
		if _, err := s.sessions.SessionFor(ctx, sessionID); err != nil {
			return attachedTabDTO{}, err
		}
	} else {
		infos, err := s.sessions.ListSessionsFor(ctx)
		if err != nil {
			return attachedTabDTO{}, err
		}
		sessionIDs = make([]string, 0, len(infos))
		for _, info := range infos {
			if !s.durableHostKind(info.Kind) || info.Status != session.StatusConnected {
				continue
			}
			sessionIDs = append(sessionIDs, info.ID)
		}
	}

	if len(sessionIDs) == 0 {
		return attachedTabDTO{}, session.ErrTabNotFound
	}
	bridged, err := s.bridge.bridge(call.Channel.ID)
	if err != nil {
		return attachedTabDTO{}, err
	}
	info, err := s.sessions.RecoverRemoteDurableInSessions(context.WithoutCancel(ctx), tabID, session.OpenTabOptions{
		SessionID: sessionID, ClientID: call.ClientID, ChannelID: call.Channel.ID, Cols: 80, Rows: 24,
	}, sessionIDs)
	if err != nil {
		if bridged {
			s.bridge.Unbridge(call.Channel.ID)
		}
		return attachedTabDTO{}, err
	}
	return attachedSessionTab(info), nil
}

// durableHostKind reports a session kind whose durable terminals this app can
// list and recover: remote helpers, and the local helper when it runs one.
func (s *terminalCommandService) durableHostKind(kind string) bool {
	return kind == session.KindSSH || kind == session.KindDevice || kind == session.KindLocal && s.localHelper != nil
}

func (s *terminalCommandService) connectLocal(ctx context.Context) (*session.Session, error) {
	row, err := s.database.AssetEnsureBuiltinLocal(ctx)
	if err != nil {
		return nil, err
	}
	asset, err := productionSessionAsset(row, false)
	if err != nil {
		return nil, err
	}
	return s.sessions.ConnectFor(ctx, asset)
}

func (s *terminalCommandService) authorizeDockerTab(ctx context.Context, tabID string) error {
	sessionID, err := s.docker.StreamSession(tabID)
	if err != nil {
		return err
	}
	_, err = s.sessions.SessionFor(ctx, sessionID)
	return err
}

// Account admission adds a user ID to the request context. An identity-less IPC
// request is an already-admitted desktop, open, or gateway request.
func productionSessionRequestContext(ctx context.Context) context.Context {
	if _, ok := ipc.UserIDFromContext(ctx); ok {
		return ctx
	}
	return session.WithOpenAccess(ctx)
}

func productionSessionAsset(row store.AssetRow, acceptHostKey bool, trustedOwner ...string) (session.Asset, error) {
	options, err := productionAssetOptionMap(row)
	if err != nil {
		return session.Asset{}, ipc.BadParam(fmt.Errorf("资产选项不是有效的 JSON: %w", err))
	}
	if row.Kind == session.KindSSH || row.Kind == session.KindDocker {
		delete(options, "userId")
		if len(trustedOwner) > 0 && trustedOwner[0] != "" {
			options["userId"] = trustedOwner[0]
		}
	}
	if acceptHostKey {
		options["autoAcceptUnknownHost"] = true
	}
	encoding, _ := options["encoding"].(string)
	startupCommand, _ := options["startupCommand"].(string)
	terminalMode, err := productionTerminalMode(options)
	if err != nil {
		return session.Asset{}, err
	}
	var environment map[string]string
	if value, ok := options["env"]; ok {
		raw, _ := json.Marshal(value)
		if err := json.Unmarshal(raw, &environment); err != nil {
			return session.Asset{}, ipc.BadParam(fmt.Errorf("资产环境变量无效: %w", err))
		}
	}
	return session.Asset{
		ID: row.ID, Name: row.Name, Kind: row.Kind, Encoding: encoding, StartupCommand: startupCommand,
		TerminalMode: terminalMode, Env: environment, Options: options, StoreBacked: true,
		ConfigFingerprint: productionAssetConfigFingerprint(row),
	}, nil
}

func productionAssetConfigFingerprint(row store.AssetRow) string {
	raw, err := json.Marshal(struct {
		Kind        string  `json:"kind"`
		Host        *string `json:"host"`
		Port        *int32  `json:"port"`
		Username    *string `json:"username"`
		AuthKind    *string `json:"authKind"`
		KeyPath     *string `json:"keyPath"`
		CredID      *string `json:"credId"`
		OptionsJSON string  `json:"optionsJson"`
	}{
		Kind: row.Kind, Host: row.Host, Port: row.Port, Username: row.Username,
		AuthKind: row.AuthKind, KeyPath: row.KeyPath, CredID: row.CredID, OptionsJSON: row.OptionsJSON,
	})
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func productionTerminalMode(options map[string]any) (string, error) {
	configured, ok := options["terminalMode"]
	if !ok {
		return session.TerminalModeAuto, nil
	}
	mode, ok := configured.(string)
	if !ok || mode != session.TerminalModeAuto && mode != "durable" && mode != session.TerminalModeRegular {
		return "", ipc.BadParam(errors.New("终端模式无效"))
	}
	return mode, nil
}

func newProductionTerminalModeResolver(database *store.Store) func(context.Context, string) (string, error) {
	return func(ctx context.Context, assetID string) (string, error) {
		row, err := database.AssetGet(ctx, assetID)
		if err != nil {
			var appErr *ipc.Error
			if errors.As(err, &appErr) && appErr.Code == ipc.CodeNotFound {
				return "", nil
			}
			return "", err
		}
		options, err := productionAssetOptionMap(row)
		if err != nil {
			return "", ipc.BadParam(err)
		}
		mode, err := productionTerminalMode(options)
		if err != nil || mode != session.TerminalModeAuto {
			return mode, err
		}
		preferences := account.NewPreferences(database)
		defaults, err := preferences.Defaults(ctx)
		if err != nil {
			return "", err
		}
		if userID, ok := ipc.UserIDFromContext(ctx); ok {
			overrides, err := preferences.UserOverrides(ctx, userID)
			if err != nil {
				return "", err
			}
			defaults = account.MergePreferences(defaults, overrides)
		}
		var preferred string
		if raw, ok := defaults["terminal.defaultMode"]; ok {
			if err := json.Unmarshal(raw, &preferred); err != nil || preferred != "durable" && preferred != session.TerminalModeRegular {
				return "", ipc.BadParam(errors.New("默认终端模式无效"))
			}
		}
		if preferred == session.TerminalModeRegular {
			return preferred, nil
		}
		return session.TerminalModeAuto, nil
	}
}

// newProductionDurableHistoryLimit resolves the recorded output limit for new
// durable tabs from the terminal.durableHistoryMiB preference, the owner's
// override first. An unset or unreadable preference keeps the helper default.
func newProductionDurableHistoryLimit(database *store.Store) func(context.Context) uint64 {
	return func(ctx context.Context) uint64 {
		preferences := account.NewPreferences(database)
		values, err := preferences.Defaults(ctx)
		if err != nil {
			return 0
		}
		if userID, ok := ipc.UserIDFromContext(ctx); ok {
			overrides, err := preferences.UserOverrides(ctx, userID)
			if err != nil {
				return 0
			}
			values = account.MergePreferences(values, overrides)
		}
		raw, ok := values["terminal.durableHistoryMiB"]
		if !ok {
			return 0
		}
		var mib uint64
		if err := json.Unmarshal(raw, &mib); err != nil || mib == 0 || mib > 1024 {
			return 0
		}
		return mib << 20
	}
}

func (s *terminalCommandService) refreshStaleConfigs(ctx context.Context, infos []session.SessionInfo) {
	if s.database == nil {
		return
	}
	for index := range infos {
		info := &infos[index]
		if info.StaleConfig || info.AssetConfigFingerprint == "" {
			continue
		}
		row, err := s.database.AssetGetLive(ctx, info.AssetID)
		if err != nil || productionAssetConfigFingerprint(row) == info.AssetConfigFingerprint {
			continue
		}
		if err := s.sessions.MarkSessionConfigStaleFor(ctx, info.ID); err == nil {
			info.StaleConfig = true
		}
	}
}

func productionSessionInfo(info session.SessionInfo) sessionInfoDTO {
	var assetID *string
	if info.AssetID != "" {
		assetID = &info.AssetID
	}
	return sessionInfoDTO{
		ID: info.ID, AssetID: assetID, Name: info.Name, Kind: info.Kind, Status: string(info.Status),
		Tabs: info.Tabs, CreatedAt: info.CreatedAt.UnixMilli(), Error: info.Error, ErrorCode: info.ErrorCode,
		Attempt: info.Attempt, NextRetryAt: info.NextRetryAt, Liveness: string(info.Liveness), StaleConfig: info.StaleConfig,
	}
}

func attachedSessionTab(info session.TabInfo) attachedTabDTO {
	return attachedTabDTO{
		TabID: info.ID, SessionID: info.SessionID, Cols: info.Cols, Rows: info.Rows,
		SizeOwner: nullableProductionString(info.SizeOwner), Subscribers: info.Subscribers, Viewers: info.Viewers, Exited: info.Exited,
		Retainable: info.Retainable, Durable: info.Durable, Ephemeral: info.Ephemeral, Encoding: info.Encoding, Title: info.Title, TitleCustom: info.TitleCustom, TitleChangedAt: info.TitleChangedAt, Shell: string(info.Shell), FallbackNotice: info.FallbackNotice,
		AttachmentID: info.AttachmentID,
	}
}

func nullableProductionString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func productionBytes(values []int) ([]byte, error) {
	result := make([]byte, len(values))
	for index, value := range values {
		if value < 0 || value > 255 {
			return nil, ipc.BadParam(fmt.Errorf("序数 %d 的终端字节超出 0..255 范围", index))
		}
		result[index] = byte(value)
	}
	return result, nil
}

func gridRequestClient(environmentClient, requestClient string) string {
	if requestClient != "" {
		return requestClient
	}
	return environmentClient
}

func validTabID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for index := 0; index < len(id); index++ {
		char := id[index]
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func (s *terminalCommandService) closeDockerSession(ctx context.Context, sessionID string) {
	if err := s.docker.CloseSession(sessionID); err != nil {
		reportAppError(ctx, s.events, "docker", "Docker 容器会话关闭失败，请检查 Docker 服务状态后重试", err)
	}
}

func terminalIPCError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, supervisor.ErrHelperReplaceFailed) {
		// A failed replacement also wraps durable.ErrUnavailable; its own
		// code and stage detail must win.
		return session.IPCError(err)
	}
	var connectErr *ssh.ConnectError
	if errors.As(err, &connectErr) {
		return sshConnectIPCError(connectErr)
	}
	if errors.Is(err, docker.ErrStreamClosed) {
		return ipc.WrapError(ipc.CodeDisconnected, err.Error(), err)
	}
	if errors.Is(err, docker.ErrNotInstalled) {
		return ipc.WrapError(ipc.Code("docker_not_installed"), "这台主机没有安装 Docker", err)
	}
	if errors.Is(err, docker.ErrAccessDenied) {
		return ipc.WrapError(ipc.Code("docker_forbidden"), "当前登录用户没有权限访问 Docker，可将该用户加入 docker 用户组后重新连接", err)
	}
	if errors.Is(err, docker.ErrDaemonUnavailable) {
		return ipc.WrapError(ipc.Code("docker_unavailable"), "Docker 服务未运行或无法连接，请在主机上启动 Docker 后重试", err)
	}
	if errors.Is(err, durable.ErrSeenUnsupported) {
		return ipc.WrapError(ipc.CodeUnsupported, err.Error(), err).WithDetail(map[string]any{"reason": "seen_unsupported"})
	}
	if errors.Is(err, docker.ErrUnsupported) || errors.Is(err, docker.ErrNoBackend) || errors.Is(err, durable.ErrUnavailable) {
		return ipc.WrapError(ipc.CodeUnsupported, err.Error(), err)
	}
	if errors.Is(err, durable.ErrNotFound) {
		return ipc.WrapError(ipc.CodeNotFound, err.Error(), err)
	}
	var conflict *base.DurableMetadataConflictError
	if errors.As(err, &conflict) {
		return ipc.WrapError(ipc.Code("metadata_conflict"), err.Error(), err).WithDetail(map[string]any{
			"title": conflict.CurrentTitle, "metadataRevision": conflict.CurrentMetadataRevision,
		})
	}
	if errors.Is(err, durable.ErrIdentity) {
		return ipc.WrapError(ipc.Code("identity_changed"), err.Error(), err)
	}
	if errors.Is(err, durable.ErrNotOwned) {
		return ipc.WrapError(ipc.CodeForbidden, err.Error(), err)
	}
	if errors.Is(err, durable.ErrExited) || errors.Is(err, durable.ErrClosed) {
		return ipc.WrapError(ipc.CodeDisconnected, err.Error(), err)
	}
	if errors.Is(err, durable.ErrInvalidInput) || errors.Is(err, durable.ErrAlreadyExists) {
		return ipc.BadParam(err)
	}
	return session.IPCError(err)
}
