package fleetserver

import (
	"context"
	"errors"
	"net/http"

	"github.com/Hello-CTF/NexTerm/internal/deviceproto"
	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/rstream"
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
	// file-mux multiplexes file operations over one bridge, so it audits as the file channel it replaces.
	case deviceproto.PurposeFile, deviceproto.PurposeFileMux:
		action = "file_open"
	// listen (one control bridge per remote listener) and accept (one bridge per inbound connection)
	// serve remote port forwards, so they audit under the same forward_open family as forward.
	case "forward", "listen", "accept":
		action = "forward_open"
	// p2p-signal carries direct-connection signaling; the relay passes it through like any other bridge.
	case agent.PurposeP2PSignal:
		action = "p2p_signal_open"
	default:
		writeFleetJSON(w, http.StatusBadRequest, ipc.Failure(ipc.NewError(ipc.CodeBadParam, "未知的桥接用途")))
		return
	}
	// A stream bridge carries an rstream the agent unwraps; the client asks for it and the agent must advertise
	// it. The audit records the inner purpose's action either way, marking stream bridges and resumes.
	query := r.URL.Query()
	resume := query.Get(rstream.RelayResumeParam) == "1"
	capabilities, _ := s.registry.Capabilities(deviceID)
	stream := (resume || query.Get(rstream.RelayStreamParam) == "1") && hasCapability(capabilities, rstream.Capability)
	if _, err := s.authorize(r.Context(), identity, deviceID, action, auditKindDeviceTerminal, func(payload *auditPayload) {
		payload.Stream, payload.Resume = stream, resume
	}); err != nil {
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
	capabilities, _ = s.registry.Capabilities(deviceID)
	if !servesPurpose(capabilities, purpose) {
		writeFleetJSON(w, http.StatusForbidden, ipc.Failure(ipc.NewError(ipc.CodeForbidden, purposeUnsupportedMessage(purpose))))
		return
	}
	// The agent may have reconnected as another build since the audit; a RESUME needs one that still holds streams.
	stream = stream && hasCapability(capabilities, rstream.Capability)
	if resume && !stream {
		writeFleetJSON(w, http.StatusConflict, ipc.Failure(ipc.NewError(ipc.CodeUnsupported, "设备代理不支持续传通道")))
		return
	}
	// The relay is registered under its login session before the session is rechecked, so a revocation that lands
	// after the cookie was validated either fails this check or reaches the registered relay.
	var token string
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		token = cookie.Value
	}
	ctx, release := s.bridges.track(r.Context(), identity, token)
	defer release()
	if s.bridges.sessionInvalid(r.Context(), identity, token) || errors.Is(context.Cause(ctx), errBridgeSessionRevoked) {
		release()
		writeFleetJSON(w, http.StatusUnauthorized, ipc.Failure(ipc.NewError(ipc.CodeForbidden, bridgeSessionReloginMessage)))
		return
	}
	if stream {
		w.Header().Set(rstream.RelayStreamHeader, "1")
	}
	user, err := websocket.Accept(w, r, wscompress.Accept(wscompress.ForBridge(purpose), nil))
	if err != nil {
		return
	}
	// deviceproto frames can reach 256KiB+overhead on the wire; the default 32KiB read limit would tear the bridge.
	user.SetReadLimit(1 << 20)
	bridge, err := s.registry.RequestStreamBridge(ctx, deviceID, purpose, stream)
	if err != nil {
		if errors.Is(context.Cause(ctx), errBridgeSessionRevoked) {
			_ = user.Close(websocket.StatusPolicyViolation, bridgeSessionReloginMessage)
			return
		}
		_ = user.Close(websocket.StatusPolicyViolation, "device bridge unavailable")
		return
	}
	relayed := bridge.(*wsConn)
	if stream {
		// A stream bridge carries rstream frames; the agent adds the session variables itself (the capability
		// implies it), so no create-frame rewrite may touch the bytes.
		relayed.createEnv = nil
	}
	pipeBridge(ctx, user, relayed)
}
