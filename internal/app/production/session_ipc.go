package production

import (
	"context"
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
	desktop      bool

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
	ID        string   `json:"id"`
	AssetID   *string  `json:"assetId"`
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	Status    string   `json:"status"`
	Tabs      []string `json:"tabs"`
	CreatedAt int64    `json:"createdAt"`
}

type openTerminalRequest struct {
	SessionID string `json:"sessionId"`
	Cols      uint32 `json:"cols"`
	Rows      uint32 `json:"rows"`
	Durable   *bool  `json:"durable,omitempty"`
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
	TabID       string  `json:"tabId"`
	SessionID   string  `json:"sessionId"`
	Cols        uint32  `json:"cols"`
	Rows        uint32  `json:"rows"`
	Controller  *string `json:"controller"`
	Subscribers int     `json:"subscribers"`
	Viewers     int     `json:"viewers"`
	Exited      bool    `json:"exited"`
	Retainable  bool    `json:"retainable"`
	Encoding    string  `json:"encoding,omitempty"`
	Title       string  `json:"title,omitempty"`
	Shell       string  `json:"shell"`
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
	TabID    string `json:"tabId"`
	Data     []int  `json:"data"`
	ClientID string `json:"clientId"`
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
	MaxBytes  int    `json:"maxBytes"`
	Visible   bool   `json:"visible"`
	Encoding  string `json:"encoding"`
	Path      string `json:"path"`
	Line      string `json:"line"`
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
				row, err := s.database.AssetGet(ctx, input.AssetID)
				if err != nil {
					return sessionInfoDTO{}, err
				}
				asset, err := productionSessionAsset(row, input.AcceptHostKey)
				if err != nil {
					return sessionInfoDTO{}, err
				}
				connected, err := s.sessions.ConnectFor(ctx, asset)
				if err != nil {
					return sessionInfoDTO{}, terminalIPCError(err)
				}
				return productionSessionInfo(connected.Info()), nil
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
				if err := s.sessions.StartReconnectFor(ctx, input.SessionID); err != nil {
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
			return ipc.Register(dispatcher, "session_open_line_tab", func(ctx context.Context, call *ipc.Call, input openTerminalRequest) (string, error) {
				ctx = productionSessionRequestContext(ctx)
				if err := s.bridge.Bridge(call.Channel.ID); err != nil {
					return "", terminalIPCError(err)
				}
				info, err := s.sessions.OpenTabFor(context.WithoutCancel(ctx), session.OpenTabOptions{
					SessionID: input.SessionID, ClientID: call.ClientID, ChannelID: call.Channel.ID,
					Cols: input.Cols, Rows: input.Rows, Ephemeral: true,
				})
				if err != nil {
					s.bridge.Unbridge(call.Channel.ID)
				}
				return info.ID, terminalIPCError(err)
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
			return ipc.Register(dispatcher, "session_cwd", func(ctx context.Context, _ *ipc.Call, input sessionIDRequest) (*string, error) {
				ctx = productionSessionRequestContext(ctx)
				transport, err := s.sessions.TransportFor(ctx, input.SessionID)
				if err != nil {
					return nil, terminalIPCError(err)
				}
				cwd, ok := transport.(interface{ CWD() string })
				if !ok {
					return nil, nil
				}
				value := cwd.CWD()
				return &value, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "terminal_attach", func(ctx context.Context, call *ipc.Call, input openTerminalRequest) (attachedTabDTO, error) {
				ctx = productionSessionRequestContext(ctx)
				connected, err := s.sessions.SessionFor(ctx, input.SessionID)
				if err != nil {
					return attachedTabDTO{}, terminalIPCError(err)
				}
				kind := connected.Asset().Kind
				ephemeral := kind == session.KindLocal
				retainable := ephemeral
				var durable *session.DurableTabOptions
				if kind == session.KindDevice || kind == session.KindSSH && (input.Durable == nil || *input.Durable) {
					durable = &session.DurableTabOptions{}
				}
				if err := s.bridge.Bridge(call.Channel.ID); err != nil {
					return attachedTabDTO{}, terminalIPCError(err)
				}
				info, err := s.sessions.OpenTabFor(context.WithoutCancel(ctx), session.OpenTabOptions{
					SessionID: input.SessionID, ClientID: call.ClientID, ChannelID: call.Channel.ID, Cols: input.Cols, Rows: input.Rows,
					Ephemeral: ephemeral, Retainable: retainable, Durable: durable,
				})
				if err != nil {
					s.bridge.Unbridge(call.Channel.ID)
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
					if err := s.bridge.Bridge(call.Channel.ID); err != nil {
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
			return ipc.RegisterNested(dispatcher, "terminal_write", func(ctx context.Context, call *ipc.Call, input terminalWriteRequest) (any, error) {
				ctx = productionSessionRequestContext(ctx)
				data, err := productionBytes(input.Data)
				if err != nil {
					return nil, err
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
					var lastOutputMSAgo int64
					if snapshot, snapshotErr := s.sessions.TerminalSnapshotFor(ctx, info.ID); snapshotErr == nil {
						lastOutputMSAgo = int64(snapshot.LastOutputMsAgo)
					}
					result = append(result, liveTabDTO{
						TabID: info.ID, SessionID: info.SessionID, SessionName: sessionInfo.Name, SessionKind: sessionInfo.Kind,
						Cols: info.Cols, Rows: info.Rows, Controller: nullableProductionString(info.Controller),
						Subscribers: info.Subscribers, Viewers: info.Viewers, Exited: info.Exited, LastOutputMSAgo: lastOutputMSAgo,
						Title: info.Title,
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
				var err error
				if input.Mode == "detach" {
					err = s.sessions.BackgroundTab(ctx, input.TabID, call.ClientID)
				} else {
					err = s.sessions.CloseTabFor(ctx, input.TabID)
				}
				if errors.Is(err, session.ErrTabNotFound) && input.SessionID != "" {
					if _, err = s.sessions.SessionFor(ctx, input.SessionID); err == nil {
						_, err = s.sessions.RecoverRemoteDurable(ctx, input.TabID, session.OpenTabOptions{
							SessionID: input.SessionID, ClientID: call.ClientID, Detached: true, Cols: 80, Rows: 24,
						})
						if err == nil {
							if input.Mode == "detach" {
								err = s.sessions.BackgroundTab(ctx, input.TabID, call.ClientID)
							} else {
								err = s.sessions.CloseTabFor(ctx, input.TabID)
							}
						}
					}
				}
				if errors.Is(err, session.ErrTabNotFound) && s.docker != nil {
					if authErr := s.authorizeDockerTab(ctx, input.TabID); authErr != nil {
						err = authErr
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
	}
	for _, register := range registrations {
		if err := register(); err != nil {
			return err
		}
	}
	if err := s.registerSSHDiagnostics(dispatcher); err != nil {
		return err
	}
	if err := registerFSCommands(dispatcher, s.sessions, s.stagedBlobs); err != nil {
		return err
	}
	if !s.desktop {
		return nil
	}
	return registerLocalFolderCommands(dispatcher)
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

	var searchErrs []error
	for _, currentSessionID := range sessionIDs {
		if err := s.bridge.Bridge(call.Channel.ID); err != nil {
			return attachedTabDTO{}, err
		}
		info, err := s.sessions.RecoverRemoteDurable(context.WithoutCancel(ctx), tabID, session.OpenTabOptions{
			SessionID: currentSessionID, ClientID: call.ClientID, ChannelID: call.Channel.ID, Cols: 80, Rows: 24,
		})
		if err == nil {
			return attachedSessionTab(info), nil
		}
		s.bridge.Unbridge(call.Channel.ID)
		if errors.Is(err, session.ErrForbidden) {
			return attachedTabDTO{}, err
		}
		if !errors.Is(err, session.ErrTabNotFound) {
			searchErrs = append(searchErrs, err)
		}
	}
	if len(searchErrs) > 0 {
		return attachedTabDTO{}, errors.Join(searchErrs...)
	}
	return attachedTabDTO{}, session.ErrTabNotFound
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

func productionSessionAsset(row store.AssetRow, acceptHostKey bool) (session.Asset, error) {
	options, err := productionAssetOptionMap(row)
	if err != nil {
		return session.Asset{}, ipc.BadParam(fmt.Errorf("资产选项不是有效的 JSON: %w", err))
	}
	if acceptHostKey {
		options["autoAcceptUnknownHost"] = true
	}
	encoding, _ := options["encoding"].(string)
	startupCommand, _ := options["startupCommand"].(string)
	var environment map[string]string
	if value, ok := options["env"]; ok {
		raw, _ := json.Marshal(value)
		if err := json.Unmarshal(raw, &environment); err != nil {
			return session.Asset{}, ipc.BadParam(fmt.Errorf("资产环境变量无效: %w", err))
		}
	}
	return session.Asset{ID: row.ID, Name: row.Name, Kind: row.Kind, Encoding: encoding, StartupCommand: startupCommand, Env: environment, Options: options}, nil
}

func productionSessionInfo(info session.SessionInfo) sessionInfoDTO {
	var assetID *string
	if info.AssetID != "" {
		assetID = &info.AssetID
	}
	return sessionInfoDTO{
		ID: info.ID, AssetID: assetID, Name: info.Name, Kind: info.Kind, Status: string(info.Status),
		Tabs: info.Tabs, CreatedAt: info.CreatedAt.UnixMilli(),
	}
}

func attachedSessionTab(info session.TabInfo) attachedTabDTO {
	return attachedTabDTO{
		TabID: info.ID, SessionID: info.SessionID, Cols: info.Cols, Rows: info.Rows,
		Controller: nullableProductionString(info.Controller), Subscribers: info.Subscribers, Viewers: info.Viewers, Exited: info.Exited,
		Retainable: info.Retainable, Encoding: info.Encoding, Title: info.Title, Shell: string(info.Shell),
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
