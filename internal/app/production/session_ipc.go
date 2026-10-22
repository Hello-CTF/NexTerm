package production

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/docker"
	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
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

	mu         sync.Mutex
	dockerTabs map[string]*dockerTabInfo
	sinks      map[string]ipc.BinaryStream
}

type dockerTabInfo struct {
	sessionID  string
	cols       uint
	rows       uint
	controller string
	channels   map[string]string
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
	StaleConfig bool       `json:"staleConfig"`
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
	Controller     *string `json:"controller"`
	Subscribers    int     `json:"subscribers"`
	Viewers        int     `json:"viewers"`
	Exited         bool    `json:"exited"`
	Retainable     bool    `json:"retainable"`
	Ephemeral      bool    `json:"ephemeral"`
	Encoding       string  `json:"encoding,omitempty"`
	Title          string  `json:"title,omitempty"`
	Shell          string  `json:"shell"`
	FallbackNotice string  `json:"fallbackNotice,omitempty"`
	AttachmentID   uint64  `json:"attachmentId,omitempty"`
}

type renameTerminalRequest struct {
	TabID string `json:"tabId"`
	Title string `json:"title"`
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
	Controller      *string `json:"controller"`
	Subscribers     int     `json:"subscribers"`
	Viewers         int     `json:"viewers"`
	Exited          bool    `json:"exited"`
	LastOutputAt    int64   `json:"lastOutputAt"`
	LastOutputMSAgo int64   `json:"lastOutputMsAgo"`
	Title           string  `json:"title,omitempty"`
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
				if useDurable {
					durable = &session.DurableTabOptions{}
					fallback = kind == session.KindSSH && input.Durable == nil
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
							if (info.Kind == session.KindSSH || info.Kind == session.KindDevice) && info.Status == session.StatusConnected {
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
			// RPC. The caller must own the tab and be its controller.
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
			return ipc.Register(dispatcher, "terminal_claim", func(ctx context.Context, call *ipc.Call, input terminalIDRequest) (*string, error) {
				ctx = productionSessionRequestContext(ctx)
				previous, err := s.sessions.ClaimFor(ctx, input.TabID, call.ClientID)
				if errors.Is(err, session.ErrTabNotFound) && s.docker != nil {
					if authErr := s.authorizeDockerTab(ctx, input.TabID); authErr != nil {
						return nil, terminalIPCError(authErr)
					}
					return s.claimDocker(input.TabID, call.ClientID)
				}
				return nullableProductionString(previous), terminalIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "terminal_release", func(ctx context.Context, call *ipc.Call, input terminalIDRequest) (bool, error) {
				ctx = productionSessionRequestContext(ctx)
				released, err := s.sessions.ReleaseFor(ctx, input.TabID, call.ClientID)
				if errors.Is(err, session.ErrTabNotFound) && s.docker != nil {
					if authErr := s.authorizeDockerTab(ctx, input.TabID); authErr != nil {
						return false, terminalIPCError(authErr)
					}
					return s.releaseDocker(input.TabID, call.ClientID)
				}
				return released, terminalIPCError(err)
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
						Cols: info.Cols, Rows: info.Rows, Controller: nullableProductionString(info.Controller),
						Subscribers: info.Subscribers, Viewers: info.Viewers, Exited: info.Exited,
						LastOutputAt: info.LastOutputAt, LastOutputMSAgo: info.LastOutputMSAgo, Title: info.Title,
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
				info, err := s.sessions.RenameTabFor(ctx, input.TabID, input.Title)
				if err != nil {
					return attachedTabDTO{}, terminalIPCError(err)
				}
				return attachedSessionTab(info), nil
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
						return helperOperationDTO{}, ipc.BadParam(errors.New("助手操作缺少 operationId"))
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
				case "status", "update", "update_interrupt":
					if s.helpers == nil {
						return helperOperationDTO{}, terminalIPCError(session.ErrUnsupported)
					}
					result, err := s.helpers.ensure(ctx, input.SessionID, action)
					if err != nil {
						if result.Update != nil && result.Update.RolledBack {
							return result, ipc.NormalizeError(err).WithDetail(result)
						}
						return helperOperationDTO{}, terminalIPCError(err)
					}
					result.Accepted = action != "status"
					return result, nil
				default:
					return helperOperationDTO{}, ipc.BadParam(errors.New("未知的助手操作"))
				}
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
	if kind := connected.Asset().Kind; kind == session.KindSSH || kind == session.KindDevice {
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
			if (info.Kind != session.KindSSH && info.Kind != session.KindDevice) || info.Status != session.StatusConnected {
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
	if !ok || mode != session.TerminalModeAuto && mode != session.TerminalModeRegular {
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
		return productionTerminalMode(options)
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
		Attempt: info.Attempt, NextRetryAt: info.NextRetryAt, StaleConfig: info.StaleConfig,
	}
}

func attachedSessionTab(info session.TabInfo) attachedTabDTO {
	return attachedTabDTO{
		TabID: info.ID, SessionID: info.SessionID, Cols: info.Cols, Rows: info.Rows,
		Controller: nullableProductionString(info.Controller), Subscribers: info.Subscribers, Viewers: info.Viewers, Exited: info.Exited,
		Retainable: info.Retainable, Ephemeral: info.Ephemeral, Encoding: info.Encoding, Title: info.Title, Shell: string(info.Shell), FallbackNotice: info.FallbackNotice,
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
	var connectErr *ssh.ConnectError
	if errors.As(err, &connectErr) {
		return sshConnectIPCError(connectErr)
	}
	if errors.Is(err, docker.ErrStreamClosed) {
		return ipc.WrapError(ipc.CodeDisconnected, err.Error(), err)
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
