package production

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/docker"
	"github.com/ProbiusOfficial/NexTerm/internal/durable"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/session"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

type terminalCommandService struct {
	database    *store.Store
	sessions    *session.Manager
	docker      *docker.Service
	durable     *durable.Backend
	durableErr  error
	bridge      *terminalBridge
	grid        *ipc.Dispatcher
	smokeAttach bool

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
}

type attachTerminalRequest struct {
	TabID       string `json:"tabId"`
	ReplayBytes int    `json:"replayBytes"`
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
}

type terminalWriteRequest struct {
	TabID    string `json:"tabId"`
	Data     []int  `json:"data"`
	ClientID string `json:"clientId"`
}

type terminalIDRequest struct {
	TabID     string `json:"tabId"`
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
}

func newTerminalCommandService(database *store.Store, sessions *session.Manager, dockerService *docker.Service, durableBackend *durable.Backend, durableErr error, bridge *terminalBridge, smokeAttach bool) *terminalCommandService {
	return &terminalCommandService{
		database: database, sessions: sessions, docker: dockerService, durable: durableBackend, durableErr: durableErr, bridge: bridge,
		dockerTabs: make(map[string]*dockerTabInfo), sinks: make(map[string]ipc.BinaryStream), smokeAttach: smokeAttach,
	}
}

func (s *terminalCommandService) registerSession(dispatcher *ipc.Dispatcher) error {
	s.grid = ipc.NewDispatcher()
	if err := s.sessions.RegisterGridCommands(s.grid); err != nil {
		return err
	}
	registrations := []func() error{
		func() error {
			return ipc.RegisterNested(dispatcher, "session_connect", func(ctx context.Context, _ *ipc.Call, input sessionConnectRequest) (sessionInfoDTO, error) {
				row, err := s.database.AssetGet(ctx, input.AssetID)
				if err != nil {
					return sessionInfoDTO{}, err
				}
				asset, err := productionSessionAsset(row, input.AcceptHostKey)
				if err != nil {
					return sessionInfoDTO{}, err
				}
				connected, err := s.sessions.Connect(ctx, asset)
				if err != nil {
					return sessionInfoDTO{}, terminalIPCError(err)
				}
				return productionSessionInfo(connected.Info()), nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "session_connect_local", func(ctx context.Context, _ *ipc.Call, _ struct{}) (sessionInfoDTO, error) {
				connected, err := s.connectLocal(ctx)
				if err != nil {
					return sessionInfoDTO{}, terminalIPCError(err)
				}
				return productionSessionInfo(connected.Info()), nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "session_disconnect", func(_ context.Context, _ *ipc.Call, input sessionIDRequest) (any, error) {
				if s.docker != nil {
					_ = s.docker.CloseSession(input.SessionID)
				}
				return nil, terminalIPCError(s.sessions.Disconnect(input.SessionID))
			})
		},
		func() error {
			return ipc.Register(dispatcher, "session_reconnect", func(_ context.Context, _ *ipc.Call, input sessionIDRequest) (bool, error) {
				started := s.sessions.StartReconnect(input.SessionID)
				if started && s.docker != nil {
					_ = s.docker.CloseSession(input.SessionID)
				}
				return started, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "session_list", func(_ context.Context, _ *ipc.Call, _ struct{}) ([]sessionInfoDTO, error) {
				infos := s.sessions.ListSessions()
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
				if err := s.bridge.Bridge(call.Channel.ID); err != nil {
					return "", terminalIPCError(err)
				}
				info, err := s.sessions.OpenTab(context.WithoutCancel(ctx), session.OpenTabOptions{
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
				_, err := s.sessions.ExecLine(ctx, input.TabID, call.ClientID, input.Line)
				return nil, terminalIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "session_cwd", func(ctx context.Context, _ *ipc.Call, input sessionIDRequest) (*string, error) {
				transport, err := s.sessions.Transport(ctx, input.SessionID)
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
			return ipc.Register(dispatcher, "terminal_attach", func(ctx context.Context, call *ipc.Call, input openTerminalRequest) (string, error) {
				durableOptions, err := s.createDurableOptions(input.SessionID)
				if err != nil {
					return "", terminalIPCError(err)
				}
				if err := s.bridge.Bridge(call.Channel.ID); err != nil {
					return "", terminalIPCError(err)
				}
				info, err := s.sessions.OpenTab(context.WithoutCancel(ctx), session.OpenTabOptions{
					SessionID: input.SessionID, ClientID: call.ClientID, ChannelID: call.Channel.ID, Cols: input.Cols, Rows: input.Rows,
					Durable: durableOptions,
				})
				if err != nil {
					s.bridge.Unbridge(call.Channel.ID)
				}
				return info.ID, terminalIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "terminal_attach_tab", func(ctx context.Context, call *ipc.Call, input attachTerminalRequest) (attachedTabDTO, error) {
				if _, err := s.sessions.Tab(input.TabID); err == nil {
					if err := s.bridge.Bridge(call.Channel.ID); err != nil {
						return attachedTabDTO{}, terminalIPCError(err)
					}
				}
				info, err := s.sessions.AttachTab(ctx, input.TabID, session.AttachOptions{
					ClientID: call.ClientID, ChannelID: call.Channel.ID, ReplayBytes: input.ReplayBytes,
				})
				if err == nil {
					return attachedSessionTab(info), nil
				}
				if errors.Is(err, session.ErrTabNotFound) && s.durableErr != nil {
					if s.docker != nil {
						if _, _, statusErr := s.docker.StreamStatus(input.TabID); statusErr == nil {
							return s.attachDocker(ctx, call, input.TabID)
						}
					}
					return attachedTabDTO{}, terminalIPCError(s.durableErr)
				}
				if errors.Is(err, session.ErrTabNotFound) && s.durable != nil {
					recovered, recoveryErr := s.recoverDurable(ctx, call, input.TabID)
					if recoveryErr == nil {
						return recovered, nil
					}
					s.bridge.Unbridge(call.Channel.ID)
					if !errors.Is(recoveryErr, durable.ErrNotFound) {
						return attachedTabDTO{}, terminalIPCError(recoveryErr)
					}
					if s.docker != nil {
						if _, _, statusErr := s.docker.StreamStatus(input.TabID); statusErr == nil {
							return s.attachDocker(ctx, call, input.TabID)
						}
						return attachedTabDTO{}, terminalIPCError(recoveryErr)
					}
					return attachedTabDTO{}, terminalIPCError(recoveryErr)
				}
				if !errors.Is(err, session.ErrTabNotFound) || s.docker == nil {
					return attachedTabDTO{}, terminalIPCError(err)
				}
				return s.attachDocker(ctx, call, input.TabID)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "terminal_write", func(ctx context.Context, call *ipc.Call, input terminalWriteRequest) (any, error) {
				data, err := productionBytes(input.Data)
				if err != nil {
					return nil, err
				}
				client := gridRequestClient(call.ClientID, input.ClientID)
				err = s.sessions.Write(ctx, input.TabID, client, data)
				if errors.Is(err, session.ErrTabNotFound) && s.docker != nil {
					err = s.writeDocker(ctx, input.TabID, client, data)
				}
				return nil, terminalIPCError(err)
			})
		},
		func() error {
			return dispatcher.RegisterRaw(session.CommandTerminalResize, s.dispatchGrid)
		},
		func() error {
			return dispatcher.RegisterRaw(session.CommandTerminalResizeFlush, s.dispatchGrid)
		},
		func() error {
			return ipc.Register(dispatcher, "terminal_detach", func(_ context.Context, _ *ipc.Call, input terminalIDRequest) (any, error) {
				var err error
				if input.ChannelID != "" {
					err = s.sessions.DetachChannel(input.ChannelID)
				} else {
					err = s.sessions.DetachAll(input.TabID)
				}
				if errors.Is(err, session.ErrTabNotFound) && s.docker != nil {
					err = s.detachDocker(input.TabID, input.ChannelID)
				}
				return nil, terminalIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "terminal_claim", func(_ context.Context, call *ipc.Call, input terminalIDRequest) (*string, error) {
				previous, err := s.sessions.Claim(input.TabID, call.ClientID)
				if errors.Is(err, session.ErrTabNotFound) && s.docker != nil {
					return s.claimDocker(input.TabID, call.ClientID)
				}
				return nullableProductionString(previous), terminalIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "terminal_release", func(_ context.Context, call *ipc.Call, input terminalIDRequest) (bool, error) {
				released, err := s.sessions.Release(input.TabID, call.ClientID)
				if errors.Is(err, session.ErrTabNotFound) && s.docker != nil {
					return s.releaseDocker(input.TabID, call.ClientID)
				}
				return released, terminalIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "terminal_list", func(_ context.Context, _ *ipc.Call, _ struct{}) ([]liveTabDTO, error) {
				infos := s.sessions.ListTabs()
				result := make([]liveTabDTO, 0, len(infos))
				for _, info := range infos {
					connected, err := s.sessions.Session(info.SessionID)
					if err != nil {
						continue
					}
					sessionInfo := connected.Info()
					var lastOutputMSAgo int64
					if snapshot, snapshotErr := s.sessions.TerminalSnapshot(info.ID); snapshotErr == nil {
						lastOutputMSAgo = int64(snapshot.LastOutputMsAgo)
					}
					result = append(result, liveTabDTO{
						TabID: info.ID, SessionID: info.SessionID, SessionName: sessionInfo.Name, SessionKind: sessionInfo.Kind,
						Cols: info.Cols, Rows: info.Rows, Controller: nullableProductionString(info.Controller),
						Subscribers: info.Subscribers, Viewers: info.Viewers, Exited: info.Exited, LastOutputMSAgo: lastOutputMSAgo,
					})
				}
				return result, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "terminal_screen_text", func(_ context.Context, _ *ipc.Call, input terminalIDRequest) (string, error) {
				value, err := s.sessions.ScreenText(input.TabID)
				return value, terminalIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "terminal_snapshot", func(_ context.Context, _ *ipc.Call, input terminalIDRequest) (any, error) {
				value, err := s.sessions.TerminalSnapshot(input.TabID)
				return value, terminalIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "terminal_tail", func(_ context.Context, _ *ipc.Call, input terminalIDRequest) ([]string, error) {
				value, err := s.sessions.TailLines(input.TabID, input.N)
				return value, terminalIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "terminal_set_visible", func(_ context.Context, _ *ipc.Call, input terminalIDRequest) (any, error) {
				return nil, terminalIPCError(s.sessions.SetVisible(input.TabID, input.Visible))
			})
		},
		func() error {
			return ipc.Register(dispatcher, "terminal_dump", func(_ context.Context, _ *ipc.Call, input terminalIDRequest) (string, error) {
				value, err := s.sessions.RawDump(input.TabID, input.MaxBytes)
				return string(value), terminalIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "terminal_switch_encoding", func(_ context.Context, _ *ipc.Call, input terminalIDRequest) (any, error) {
				return nil, terminalIPCError(s.sessions.SwitchEncoding(input.TabID, input.Encoding))
			})
		},
		func() error {
			return ipc.Register(dispatcher, "terminal_record_start", func(_ context.Context, _ *ipc.Call, input terminalIDRequest) (any, error) {
				return nil, terminalIPCError(s.sessions.StartRecording(input.TabID, input.Path))
			})
		},
		func() error {
			return ipc.Register(dispatcher, "terminal_record_stop", func(_ context.Context, _ *ipc.Call, input terminalIDRequest) (uint64, error) {
				value, err := s.sessions.StopRecording(input.TabID)
				return value, terminalIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "terminal_export_log", func(_ context.Context, _ *ipc.Call, input terminalIDRequest) (uint64, error) {
				value, err := s.sessions.ExportLog(input.TabID, input.Path, input.MaxBytes)
				return value, terminalIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "terminal_close_tab", func(_ context.Context, call *ipc.Call, input terminalIDRequest) (any, error) {
				var err error
				if input.Mode == "detach" {
					err = s.sessions.DetachClient(input.TabID, call.ClientID)
				} else {
					err = s.sessions.CloseTab(input.TabID)
				}
				if errors.Is(err, session.ErrTabNotFound) && s.docker != nil {
					if input.Mode == "detach" {
						err = s.detachDocker(input.TabID, call.Channel.ID)
					} else {
						err = s.closeDocker(input.TabID)
					}
				}
				return nil, terminalIPCError(err)
			})
		},
	}
	if s.smokeAttach {
		registrations = append(registrations, func() error {
			return ipc.Register(dispatcher, "terminal_attach_smoke", func(ctx context.Context, call *ipc.Call, input openTerminalRequest) (string, error) {
				if err := s.bridge.Bridge(call.Channel.ID); err != nil {
					return "", terminalIPCError(err)
				}
				info, err := s.sessions.OpenTab(context.WithoutCancel(ctx), session.OpenTabOptions{
					SessionID: input.SessionID, ClientID: call.ClientID, ChannelID: call.Channel.ID, Cols: input.Cols, Rows: input.Rows,
				})
				if err != nil {
					s.bridge.Unbridge(call.Channel.ID)
				}
				return info.ID, terminalIPCError(err)
			})
		})
	}
	for _, register := range registrations {
		if err := register(); err != nil {
			return err
		}
	}
	return registerFSCommands(dispatcher, s.sessions)
}

func (s *terminalCommandService) createDurableOptions(sessionID string) (*session.DurableTabOptions, error) {
	connected, err := s.sessions.Session(sessionID)
	if err != nil {
		return nil, err
	}
	if connected.Asset().Kind != session.KindLocal {
		return nil, nil
	}
	if s.durableErr != nil {
		return nil, s.durableErr
	}
	if s.durable == nil {
		return nil, nil
	}
	return &session.DurableTabOptions{}, nil
}

func (s *terminalCommandService) recoverDurable(ctx context.Context, call *ipc.Call, tabID string) (attachedTabDTO, error) {
	var connected *session.Session
	for _, info := range s.sessions.ListSessions() {
		if info.Kind == session.KindLocal && info.Status == session.StatusConnected {
			current, err := s.sessions.Session(info.ID)
			if err == nil {
				connected = current
				break
			}
		}
	}
	var err error
	if connected == nil {
		connected, err = s.connectLocal(ctx)
		if err != nil {
			return attachedTabDTO{}, err
		}
	}
	if err := s.bridge.Bridge(call.Channel.ID); err != nil {
		return attachedTabDTO{}, err
	}
	info, err := s.sessions.OpenTab(context.WithoutCancel(ctx), session.OpenTabOptions{
		TabID: tabID, SessionID: connected.ID, ClientID: call.ClientID, ChannelID: call.Channel.ID,
		Cols: 80, Rows: 24, Durable: &session.DurableTabOptions{Recover: true},
	})
	if err != nil {
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
	return s.sessions.Connect(ctx, asset)
}

func (s *terminalCommandService) dispatchGrid(ctx context.Context, call *ipc.Call) (any, error) {
	response := s.grid.Dispatch(ctx, ipc.Request{
		Command: call.Command, Args: call.Args, Channel: call.Channel, ClientID: call.ClientID,
	}, ipc.Environment{ClientID: call.ClientID, Events: call.Events, Streams: call.Streams})
	if response.OK {
		return response.Data, nil
	}
	if call.Command == session.CommandTerminalResize && response.Error != nil && response.Error.Code == ipc.CodeNotFound && s.docker != nil {
		var input struct {
			TabID    string `json:"tabId"`
			ClientID string `json:"clientId"`
			Cols     uint   `json:"cols"`
			Rows     uint   `json:"rows"`
		}
		if err := json.Unmarshal(call.Args, &input); err != nil {
			return nil, ipc.BadParam(err)
		}
		client := gridRequestClient(call.ClientID, input.ClientID)
		return nil, terminalIPCError(s.resizeDocker(ctx, input.TabID, client, input.Cols, input.Rows))
	}
	return nil, response.Error
}

func productionSessionAsset(row store.AssetRow, acceptHostKey bool) (session.Asset, error) {
	options := make(map[string]any)
	if err := json.Unmarshal([]byte(store.ParseJSONOr(row.OptionsJSON)), &options); err != nil {
		return session.Asset{}, ipc.BadParam(err)
	}
	if row.Host != nil {
		options["host"] = *row.Host
	}
	if row.Port != nil {
		options["port"] = *row.Port
	}
	if row.Username != nil {
		options["username"] = *row.Username
	}
	if row.AuthKind != nil {
		options["authKind"] = *row.AuthKind
	}
	if row.KeyPath != nil {
		options["keyPath"] = *row.KeyPath
	}
	if row.CredID != nil {
		options["credId"] = *row.CredID
	}
	if acceptHostKey {
		options["autoAcceptUnknownHost"] = true
	}
	encoding, _ := options["encoding"].(string)
	return session.Asset{ID: row.ID, Name: row.Name, Kind: row.Kind, Encoding: encoding, Options: options}, nil
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
			return nil, ipc.BadParam(fmt.Errorf("terminal byte at index %d is outside 0..255", index))
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

func terminalIPCError(err error) error {
	if err == nil {
		return nil
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
	if errors.Is(err, durable.ErrNotOwned) || errors.Is(err, durable.ErrIdentity) {
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
