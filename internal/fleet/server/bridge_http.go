package fleetserver

import (
	"net/http"

	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/coder/websocket"
)

// serveDeviceBridgeRelay handles GET /fleet/devices/{id}/bridge: after owner
// session authentication, the server asks the agent over the control channel to dial a bridge outbound, then connects this user-side
// websocket and the bridge byte stream bidirectionally. Supervisor protocol frames pass through as-is (binary frames, byte-transparent);
// session semantics (create/attach/resume) are arbitrated by the supervisor helper on the other end.
// GET is a safe method and, like the other session routes, requires no CSRF; cross-origin WS is blocked by
// websocket.Accept's default Origin check together with same-site cookies.
func (s *Service) serveDeviceBridgeRelay(w http.ResponseWriter, r *http.Request) {
	identity := identityFrom(r)
	deviceID := r.PathValue("id")
	purpose := r.URL.Query().Get("purpose")
	action := "terminal_open"
	switch purpose {
	case "", agent.PurposeTerminal:
		purpose = agent.PurposeTerminal
	case "file":
		action = "file_open"
	case "forward":
		action = "forward_open"
	default:
		writeFleetJSON(w, http.StatusBadRequest, ipc.Failure(ipc.NewError(ipc.CodeBadParam, "未知的桥接用途")))
		return
	}
	if _, err := s.authorize(r.Context(), identity, deviceID, action, auditKindDeviceTerminal); err != nil {
		writeFleetFailure(w, err)
		return
	}
	desired, err := s.AgentDesiredConfig(r.Context(), deviceID)
	if err != nil {
		writeFleetFailure(w, err)
		return
	}
	if !desired.TerminalEnabled {
		writeFleetJSON(w, http.StatusForbidden, ipc.Failure(ipc.NewError(ipc.CodeForbidden, "设备已关闭远程访问")))
		return
	}
	user, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionContextTakeover})
	if err != nil {
		return
	}
	bridge, err := s.registry.RequestBridge(r.Context(), deviceID, purpose)
	if err != nil {
		_ = user.Close(websocket.StatusPolicyViolation, "device bridge unavailable")
		return
	}
	pipeBridge(user, bridge.(*wsConn))
}
