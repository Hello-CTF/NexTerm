package fleetserver

import (
	"net/http"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/coder/websocket"
)

// serveDeviceBridgeRelay 处理 GET /fleet/devices/{id}/bridge: owner 或 superadmin
// 经会话认证后, 服务端经控制通道请 agent 出站拨一条桥接, 再把这条用户侧
// websocket 与桥接字节流双向接通。supervisor 协议帧原样穿过 (二进制帧, 字节透明),
// 会话语义 (create/attach/resume) 由对端 supervisor helper 裁决。
// GET 是安全方法, 与其他 session 路由一样不要求 CSRF; 跨源 WS 由
// websocket.Accept 的默认 Origin 校验与同站 cookie 共同拦截。
func (s *Service) serveDeviceBridgeRelay(w http.ResponseWriter, r *http.Request) {
	identity := identityFrom(r)
	deviceID := r.PathValue("id")
	if _, err := s.authorize(r.Context(), identity, deviceID, "terminal_open", auditKindDeviceTerminal); err != nil {
		writeFleetFailure(w, err)
		return
	}
	desired, err := s.AgentDesiredConfig(r.Context(), deviceID)
	if err != nil {
		writeFleetFailure(w, err)
		return
	}
	if !desired.TerminalEnabled {
		writeFleetJSON(w, http.StatusForbidden, ipc.Failure(ipc.NewError(ipc.CodeForbidden, "设备已关闭终端访问")))
		return
	}
	user, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	bridge, err := s.registry.RequestBridge(r.Context(), deviceID)
	if err != nil {
		_ = user.Close(websocket.StatusPolicyViolation, "device bridge unavailable")
		return
	}
	pipeBridge(user, bridge.(*wsConn))
}
