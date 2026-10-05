package production

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/docker"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/session"
)

type dockerLogsRequest struct {
	SessionID   string `json:"sessionId"`
	ContainerID string `json:"containerId"`
	Tail        int    `json:"tail"`
}

type dockerExecRequest struct {
	SessionID   string `json:"sessionId"`
	ContainerID string `json:"containerId"`
	Cmd         string `json:"cmd"`
	Cols        uint   `json:"cols"`
	Rows        uint   `json:"rows"`
	ClientID    string `json:"clientId"`
}

type dockerActionRequest struct {
	SessionID   string `json:"sessionId"`
	ContainerID string `json:"containerId"`
	Action      string `json:"action"`
	NewName     string `json:"newName"`
}

type dockerImageRequest struct {
	SessionID string `json:"sessionId"`
	Image     string `json:"image"`
	Force     bool   `json:"force"`
}

type dockerContainerRequest struct {
	SessionID   string `json:"sessionId"`
	ContainerID string `json:"containerId"`
	Path        string `json:"path"`
}

func (s *terminalCommandService) registerDocker(dispatcher *ipc.Dispatcher) error {
	registrations := []func() error{
		func() error {
			return ipc.Register(dispatcher, "docker_overview", func(ctx context.Context, _ *ipc.Call, input sessionIDRequest) (any, error) {
				value, err := s.docker.Overview(ctx, input.SessionID)
				return value, terminalIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "docker_ps", func(ctx context.Context, _ *ipc.Call, input sessionIDRequest) (any, error) {
				value, err := s.docker.PS(ctx, input.SessionID)
				return value, terminalIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "docker_logs_attach", func(ctx context.Context, call *ipc.Call, input dockerLogsRequest) (string, error) {
				stream, sink, err := s.openDockerSink(ctx, call)
				if err != nil {
					return "", err
				}
				id, err := s.docker.AttachLogs(ctx, docker.LogsAttachRequest{
					SessionID: input.SessionID, Container: input.ContainerID, Tail: &input.Tail, Sink: sink,
				})
				if err != nil {
					_ = stream.Close()
					return "", terminalIPCError(err)
				}
				s.watchDockerStream(id, stream)
				return id, nil
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "docker_exec_attach", func(ctx context.Context, call *ipc.Call, input dockerExecRequest) (string, error) {
				stream, sink, err := s.openDockerSink(ctx, call)
				if err != nil {
					return "", err
				}
				id, err := s.docker.AttachExec(ctx, docker.ExecAttachRequest{
					SessionID: input.SessionID, Container: input.ContainerID, CustomCmd: input.Cmd,
					Width: input.Cols, Height: input.Rows, Sink: sink,
				})
				if err != nil {
					_ = stream.Close()
					return "", terminalIPCError(err)
				}
				clientID := gridRequestClient(call.ClientID, input.ClientID)
				s.mu.Lock()
				s.dockerTabs[id] = &dockerTabInfo{
					sessionID: input.SessionID, cols: input.Cols, rows: input.Rows, controller: clientID,
					channels: map[string]string{call.Channel.ID: clientID},
				}
				s.mu.Unlock()
				s.watchDockerStream(id, stream)
				return id, nil
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "docker_action", func(ctx context.Context, _ *ipc.Call, input dockerActionRequest) (any, error) {
				assetID := ""
				if connected, err := s.sessions.Session(input.SessionID); err == nil {
					assetID = connected.Asset().ID
				}
				return nil, terminalIPCError(s.docker.Action(ctx, docker.ActionRequest{
					SessionID: input.SessionID, AssetID: assetID, Source: "user",
					Options: docker.ActionOptions{Container: input.ContainerID, Action: docker.Action(input.Action), NewName: input.NewName},
				}))
			})
		},
		func() error {
			return ipc.Register(dispatcher, "docker_images", func(ctx context.Context, _ *ipc.Call, input sessionIDRequest) (any, error) {
				value, err := s.docker.Images(ctx, input.SessionID)
				return value, terminalIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "docker_image_pull", func(ctx context.Context, _ *ipc.Call, input dockerImageRequest) (string, error) {
				if strings.TrimSpace(input.Image) == "" {
					return "", ipc.BadParam(fmt.Errorf("镜像引用不能为空"))
				}
				value, err := s.docker.ImagePull(ctx, docker.ImagePullRequest{SessionID: input.SessionID, Reference: input.Image})
				return value, terminalIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "docker_image_remove", func(ctx context.Context, _ *ipc.Call, input dockerImageRequest) (any, error) {
				if strings.TrimSpace(input.Image) == "" {
					return nil, ipc.BadParam(fmt.Errorf("镜像引用不能为空"))
				}
				return nil, terminalIPCError(s.docker.ImageRemove(ctx, input.SessionID, input.Image, input.Force))
			})
		},
		func() error {
			return ipc.Register(dispatcher, "docker_inspect", func(ctx context.Context, _ *ipc.Call, input dockerContainerRequest) (json.RawMessage, error) {
				value, err := s.docker.Inspect(ctx, input.SessionID, input.ContainerID)
				return value, terminalIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "docker_stats", func(ctx context.Context, _ *ipc.Call, input sessionIDRequest) (string, error) {
				value, err := s.docker.Stats(ctx, input.SessionID)
				return value, terminalIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "docker_container_list_dir", func(ctx context.Context, _ *ipc.Call, input dockerContainerRequest) ([]string, error) {
				value, err := s.docker.ContainerListDir(ctx, input.SessionID, input.ContainerID, input.Path)
				return value, terminalIPCError(err)
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

func (s *terminalCommandService) openDockerSink(ctx context.Context, call *ipc.Call) (ipc.BinaryStream, docker.FrameSink, error) {
	stream, err := call.Streams.OpenBinary(ctx, call.Channel)
	if err != nil {
		return nil, nil, terminalIPCError(err)
	}
	return stream, docker.FrameSinkFunc(func(ctx context.Context, frame []byte) error {
		return stream.SendBinary(ctx, frame)
	}), nil
}

func (s *terminalCommandService) watchDockerStream(id string, stream ipc.BinaryStream) {
	s.mu.Lock()
	if previous := s.sinks[id]; previous != nil && previous != stream {
		_ = previous.Close()
	}
	s.sinks[id] = stream
	s.mu.Unlock()
	done, err := s.docker.StreamDone(id)
	if err != nil {
		return
	}
	go func() {
		<-done
		_ = stream.Close()
		s.mu.Lock()
		if s.sinks[id] == stream {
			delete(s.sinks, id)
		}
		s.mu.Unlock()
	}()
}

func (s *terminalCommandService) attachDocker(ctx context.Context, call *ipc.Call, id string) (attachedTabDTO, error) {
	stream, sink, err := s.openDockerSink(ctx, call)
	if err != nil {
		return attachedTabDTO{}, err
	}
	if err := s.docker.ResumeExec(id, sink); err != nil {
		_ = stream.Close()
		return attachedTabDTO{}, terminalIPCError(err)
	}
	s.watchDockerStream(id, stream)
	sessionID, err := s.docker.StreamSession(id)
	if err != nil {
		return attachedTabDTO{}, terminalIPCError(err)
	}
	s.mu.Lock()
	meta := s.dockerTabs[id]
	if meta == nil {
		meta = &dockerTabInfo{sessionID: sessionID, cols: 80, rows: 24, controller: call.ClientID, channels: make(map[string]string)}
		s.dockerTabs[id] = meta
	}
	meta.channels = map[string]string{call.Channel.ID: call.ClientID}
	info := attachedTabDTO{
		TabID: id, SessionID: meta.sessionID, Cols: uint32(meta.cols), Rows: uint32(meta.rows),
		Controller: nullableProductionString(meta.controller), Subscribers: 1, Viewers: 1,
	}
	s.mu.Unlock()
	if status, completed, err := s.docker.StreamStatus(id); err == nil {
		info.Exited = completed || status.Err != nil
	}
	return info, nil
}

func (s *terminalCommandService) detachDocker(id, _ string) error {
	s.mu.Lock()
	stream := s.sinks[id]
	delete(s.sinks, id)
	delete(s.dockerTabs, id)
	s.mu.Unlock()
	if stream != nil {
		_ = stream.Close()
	}
	return s.docker.DetachStream(id)
}

func (s *terminalCommandService) closeDocker(id string) error {
	s.mu.Lock()
	stream := s.sinks[id]
	delete(s.sinks, id)
	delete(s.dockerTabs, id)
	s.mu.Unlock()
	if stream != nil {
		_ = stream.Close()
	}
	return s.docker.CloseStream(id)
}

func (s *terminalCommandService) claimDocker(id, clientID string) (*string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	meta := s.dockerTabs[id]
	if meta == nil {
		return nil, session.ErrTabNotFound
	}
	previous := meta.controller
	meta.controller = clientID
	return nullableProductionString(previous), nil
}

func (s *terminalCommandService) releaseDocker(id, clientID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	meta := s.dockerTabs[id]
	if meta == nil {
		return false, session.ErrTabNotFound
	}
	if meta.controller != clientID {
		return false, nil
	}
	meta.controller = ""
	return true, nil
}

func (s *terminalCommandService) dockerController(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if meta := s.dockerTabs[id]; meta != nil {
		return meta.controller
	}
	return ""
}

func (s *terminalCommandService) writeDocker(ctx context.Context, id, clientID string, data []byte) error {
	if controller := s.dockerController(id); controller != "" && controller != clientID {
		return session.ErrNotController
	}
	return s.docker.WriteExec(ctx, id, data)
}

func (s *terminalCommandService) resizeDocker(ctx context.Context, id, clientID string, cols, rows uint) error {
	if controller := s.dockerController(id); controller != "" && controller != clientID {
		return session.ErrNotController
	}
	return s.docker.ResizeExec(ctx, id, cols, rows)
}
