package fleetserver

import (
	"context"
	"net"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// AuthorizeDeviceTerminal 校验 userID 对 deviceID 的终端访问 (设备 owner、未吊销、
// 终端开关打开) 并写 terminal_open 审计, 规则与桥端点一致。设备会话
// (session_connect_device) 的每次连接与重连尝试各调用一次。
func (s *Service) AuthorizeDeviceTerminal(ctx context.Context, userID, deviceID string) error {
	if userID == "" {
		return ipc.NewError(ipc.CodeForbidden, "会话无效或缺失，请重新登录")
	}
	if _, err := s.authorize(ctx, &account.Identity{UserID: userID}, deviceID, "terminal_open", auditKindDeviceTerminal); err != nil {
		return err
	}
	desired, err := s.AgentDesiredConfig(ctx, deviceID)
	if err != nil {
		return err
	}
	if !desired.TerminalEnabled {
		return ipc.NewError(ipc.CodeForbidden, "设备已关闭终端访问")
	}
	return nil
}

// RequestDeviceBridge 为已授权的设备会话拨一条到设备 supervisor helper 的桥;
// 授权在会话连接时已判定, 桥本身不再重复审计 (与 registry.RequestBridge 同语义)。
func (s *Service) RequestDeviceBridge(ctx context.Context, deviceID string) (net.Conn, error) {
	return s.registry.RequestBridge(ctx, deviceID)
}

// DeviceStateDigest 返回设备控制通道 hello 上报的 supervisor 状态摘要;
// 设备离线或未上报时 ok=false。
func (s *Service) DeviceStateDigest(deviceID string) (string, bool) {
	return s.registry.StateDigest(deviceID)
}
