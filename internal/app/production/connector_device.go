package production

import (
	"context"
	"errors"
	"fmt"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	sshdaemon "github.com/Hello-CTF/NexTerm/internal/ssh"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/device"
)

// DeviceConnectorConfig 是设备会话的拨号面, 由装配层按宿主注入: Dial 打开一条
// 到设备 supervisor helper 的桥 (桌面=经已登录服务端账号的 WS 桥, 服务端=
// fleet registry 直拨); Digest 解析设备 supervisor 状态摘要 (服务端读控制
// 通道 hello, 桌面查设备清单); Authorize 仅服务端注入, 每次 (重)连执行 owner/
// 吊销/终端开关校验并写 terminal_open 审计, 桌面为 nil (每条桥在服务端 HTTP
// 边已逐次授权审计)。重连在后台 ctx 运行, userID 取不到, 由 IPC 层在建立会话
// 时把认证用户写进资产 options 供回退。
type DeviceConnectorConfig struct {
	Dial      device.DialFunc
	Digest    func(ctx context.Context, deviceID string) (string, error)
	Authorize func(ctx context.Context, userID, deviceID string) error
}

// SetDeviceConnector 注入设备会话拨号面; 未注入时设备会话连接报不支持。
func (s *ProductionServices) SetDeviceConnector(config *DeviceConnectorConfig) {
	if s.sshConnector != nil {
		s.sshConnector.device.Store(config)
	}
}

func (c *productionConnector) connectDevice(ctx context.Context, options productionAssetOptions, generation uint64) (base.Transport, error) {
	config := c.device.Load()
	if config == nil || config.Dial == nil || config.Digest == nil {
		return nil, fmt.Errorf("%w: 设备终端通道未配置", session.ErrUnsupported)
	}
	if options.DeviceID == "" {
		return nil, ipc.BadParam(errors.New("设备会话缺少 deviceId"))
	}
	if config.Authorize != nil {
		userID, _ := ipc.UserIDFromContext(ctx)
		if userID == "" {
			userID = options.UserID
		}
		if err := config.Authorize(ctx, userID, options.DeviceID); err != nil {
			return nil, err
		}
	}
	digest, err := config.Digest(ctx, options.DeviceID)
	if err != nil {
		return nil, err
	}
	return device.New(options.DeviceID, digest, config.Dial, generation), nil
}

// productionDurableResolver 按传输分流 durable 提供方: 设备传输直连设备
// supervisor helper, SSH 传输走目标机 supervisor daemon。
type productionDurableResolver struct {
	ssh *sshdaemon.Resolver
}

func (r productionDurableResolver) ResolveDurable(ctx context.Context, transport base.Transport) (base.DurableProvider, error) {
	if deviceTransport, ok := transport.(*device.Transport); ok {
		return deviceTransport.Provider(), nil
	}
	if r.ssh != nil {
		return r.ssh.ResolveDurable(ctx, transport)
	}
	return nil, fmt.Errorf("%w: remote durable tabs need an SSH transport", base.ErrUnsupported)
}
