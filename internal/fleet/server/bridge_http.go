package fleetserver

import (
	"net/http"

	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/wscompress"
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
	// listen (one control bridge per remote listener) and accept (one bridge per inbound connection)
	// serve remote port forwards, so they audit under the same forward_open family as forward.
	case "forward", "listen", "accept":
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
	// Pre-check purpose support before upgrading: a rejected purpose must reach the caller as a readable fleet
	// error, not as a bare WS close after Accept (the desktop only surfaces close reasons). Online state is
	// checked separately from capability reporting.
	if !s.registry.AgentOnline(deviceID) {
		writeFleetJSON(w, http.StatusServiceUnavailable, ipc.Failure(ipc.NewError(ipc.CodeDisconnected, errDeviceOffline.Error())))
		return
	}
	capabilities, _ := s.registry.Capabilities(deviceID)
	if !servesPurpose(capabilities, purpose) {
		writeFleetJSON(w, http.StatusForbidden, ipc.Failure(ipc.NewError(ipc.CodeForbidden, purposeUnsupportedMessage(purpose))))
		return
	}
	user, err := websocket.Accept(w, r, wscompress.Accept(wscompress.ForBridge(purpose), nil))
	if err != nil {
		return
	}
	// deviceproto frames can reach 256KiB+overhead on the wire; the default 32KiB read limit would tear the bridge.
	user.SetReadLimit(1 << 20)
	bridge, err := s.registry.RequestBridge(r.Context(), deviceID, purpose)
	if err != nil {
		_ = user.Close(websocket.StatusPolicyViolation, "device bridge unavailable")
		return
	}
	pipeBridge(user, bridge.(*wsConn))
}
