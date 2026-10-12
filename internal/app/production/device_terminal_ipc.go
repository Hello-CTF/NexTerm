package production

import (
	"context"
	"strconv"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
)

const (
	CommandDeviceTerminalOpen        = "device_terminal_open"
	CommandDeviceTerminalResume      = "device_terminal_resume"
	CommandDeviceTerminalBegin       = "device_terminal_begin"
	CommandDeviceTerminalInput       = "device_terminal_input"
	CommandDeviceTerminalResize      = "device_terminal_resize"
	CommandDeviceTerminalKill        = "device_terminal_kill"
	CommandDeviceTerminalKillSession = "device_terminal_kill_session"
	CommandDeviceTerminalClose       = "device_terminal_close"
)

type deviceTerminalOpenRequest struct {
	DeviceID    string `json:"deviceId"`
	StateDigest string `json:"stateDigest"`
	Cols        uint32 `json:"cols"`
	Rows        uint32 `json:"rows"`
}

// deviceTerminalIdentityDTO 的 createdAtUnixNano 以字符串携带: JS number 装不下
// 纳秒 (2^53 < 2^63), 前端 SupervisorSessionIdentity 用 BigInt, JSON 序列化走字符串。
type deviceTerminalIdentityDTO struct {
	CreatedAtUnixNano string `json:"createdAtUnixNano"`
	Incarnation       string `json:"incarnation"`
}

type deviceTerminalResumeRequest struct {
	DeviceID    string                    `json:"deviceId"`
	StateDigest string                    `json:"stateDigest"`
	SessionID   string                    `json:"sessionId"`
	Identity    deviceTerminalIdentityDTO `json:"identity"`
}

type deviceTerminalChannelRequest struct {
	ChannelID string `json:"channelId"`
}

type deviceTerminalInputRequest struct {
	ChannelID string `json:"channelId"`
	Data      []byte `json:"data"`
}

type deviceTerminalResizeRequest struct {
	ChannelID string `json:"channelId"`
	Cols      uint32 `json:"cols"`
	Rows      uint32 `json:"rows"`
}

type deviceTerminalKillSessionRequest struct {
	DeviceID    string                     `json:"deviceId"`
	StateDigest string                     `json:"stateDigest"`
	SessionID   string                     `json:"sessionId"`
	Identity    *deviceTerminalIdentityDTO `json:"identity,omitempty"`
}

type deviceTerminalOpenResponse struct {
	ChannelID string                    `json:"channelId"`
	Info      supervisor.Info           `json:"info"`
	Identity  deviceTerminalIdentityDTO `json:"identity"`
}

type deviceTerminalInputResponse struct {
	Written int `json:"written"`
}

func identityFromDTO(dto deviceTerminalIdentityDTO) (supervisor.Identity, error) {
	nano, err := strconv.ParseInt(dto.CreatedAtUnixNano, 10, 64)
	if err != nil {
		return supervisor.Identity{}, ipc.NewError(ipc.CodeBadParam, "identity.createdAtUnixNano 不合法")
	}
	return supervisor.Identity{CreatedAt: time.Unix(0, nano).UTC(), Incarnation: dto.Incarnation}, nil
}

func identityToDTO(identity supervisor.Identity) deviceTerminalIdentityDTO {
	return deviceTerminalIdentityDTO{
		CreatedAtUnixNano: strconv.FormatInt(identity.CreatedAt.UnixNano(), 10),
		Incarnation:       identity.Incarnation,
	}
}

func openResponseFrom(result *deviceTerminalOpenResult) deviceTerminalOpenResponse {
	return deviceTerminalOpenResponse{
		ChannelID: result.channelID,
		Info:      result.info,
		Identity:  identityToDTO(result.identity),
	}
}

func registerDeviceTerminalCommands(dispatcher *ipc.Dispatcher, manager *deviceTerminalManager) error {
	badEmpty := func(field string) error {
		return ipc.NewError(ipc.CodeBadParam, field+" 不能为空")
	}
	registrations := []func() error{
		func() error {
			return ipc.RegisterNested(dispatcher, CommandDeviceTerminalOpen, func(ctx context.Context, _ *ipc.Call, input deviceTerminalOpenRequest) (*deviceTerminalOpenResponse, error) {
				if input.DeviceID == "" {
					return nil, badEmpty("deviceId")
				}
				if input.StateDigest == "" {
					return nil, badEmpty("stateDigest")
				}
				result, err := manager.open(ctx, input.DeviceID, input.StateDigest, input.Cols, input.Rows)
				if err != nil {
					return nil, err
				}
				response := openResponseFrom(result)
				return &response, nil
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, CommandDeviceTerminalResume, func(ctx context.Context, _ *ipc.Call, input deviceTerminalResumeRequest) (*deviceTerminalOpenResponse, error) {
				if input.DeviceID == "" {
					return nil, badEmpty("deviceId")
				}
				if input.StateDigest == "" {
					return nil, badEmpty("stateDigest")
				}
				if input.SessionID == "" {
					return nil, badEmpty("sessionId")
				}
				identity, err := identityFromDTO(input.Identity)
				if err != nil {
					return nil, err
				}
				result, err := manager.resume(ctx, input.DeviceID, input.StateDigest, input.SessionID, identity)
				if err != nil {
					return nil, err
				}
				response := openResponseFrom(result)
				return &response, nil
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, CommandDeviceTerminalBegin, func(_ context.Context, _ *ipc.Call, input deviceTerminalChannelRequest) (struct{}, error) {
				if input.ChannelID == "" {
					return struct{}{}, badEmpty("channelId")
				}
				return struct{}{}, manager.begin(input.ChannelID)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, CommandDeviceTerminalInput, func(_ context.Context, _ *ipc.Call, input deviceTerminalInputRequest) (*deviceTerminalInputResponse, error) {
				if input.ChannelID == "" {
					return nil, badEmpty("channelId")
				}
				written, err := manager.input(input.ChannelID, input.Data)
				if err != nil {
					return nil, err
				}
				return &deviceTerminalInputResponse{Written: written}, nil
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, CommandDeviceTerminalResize, func(ctx context.Context, _ *ipc.Call, input deviceTerminalResizeRequest) (struct{}, error) {
				if input.ChannelID == "" {
					return struct{}{}, badEmpty("channelId")
				}
				return struct{}{}, manager.resize(ctx, input.ChannelID, input.Cols, input.Rows)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, CommandDeviceTerminalKill, func(ctx context.Context, _ *ipc.Call, input deviceTerminalChannelRequest) (struct{}, error) {
				if input.ChannelID == "" {
					return struct{}{}, badEmpty("channelId")
				}
				return struct{}{}, manager.kill(ctx, input.ChannelID)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, CommandDeviceTerminalKillSession, func(ctx context.Context, _ *ipc.Call, input deviceTerminalKillSessionRequest) (struct{}, error) {
				if input.DeviceID == "" {
					return struct{}{}, badEmpty("deviceId")
				}
				if input.StateDigest == "" {
					return struct{}{}, badEmpty("stateDigest")
				}
				if input.SessionID == "" {
					return struct{}{}, badEmpty("sessionId")
				}
				var identity *supervisor.Identity
				if input.Identity != nil {
					parsed, err := identityFromDTO(*input.Identity)
					if err != nil {
						return struct{}{}, err
					}
					identity = &parsed
				}
				return struct{}{}, manager.killSession(ctx, input.DeviceID, input.StateDigest, input.SessionID, identity)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, CommandDeviceTerminalClose, func(_ context.Context, _ *ipc.Call, input deviceTerminalChannelRequest) (struct{}, error) {
				if input.ChannelID == "" {
					return struct{}{}, badEmpty("channelId")
				}
				return struct{}{}, manager.close(input.ChannelID)
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
