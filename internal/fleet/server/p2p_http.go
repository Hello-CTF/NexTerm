package fleetserver

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/p2p/grant"
)

var errP2PSessionRevoked = errors.New("login session revoked")

// mountP2PRoutes registers the direct-connection routes; their patterns are listed in fleetRoutePatterns.
func (s *Service) mountP2PRoutes(mux routeRegistrar) {
	mux.Handle("GET /fleet/p2p/config", s.requireFleetSession(http.HandlerFunc(s.serveP2PConfig)))
	mux.Handle("POST /fleet/devices/{id}/p2p/grant", s.requireFleetSession(s.requireFleetCSRF(http.HandlerFunc(s.serveP2PGrant))))
	mux.Handle("POST /fleet/devices/{id}/p2p/key/confirm", s.requireFleetSession(s.requireFleetCSRF(http.HandlerFunc(s.serveP2PKeyConfirm))))
	// Agent routes authenticate the device credential inside the handler, like /agent/sync.
	mux.HandleFunc("GET /agent/p2p/config", s.serveAgentP2PConfig)
	mux.HandleFunc("POST /agent/p2p/key", s.serveAgentP2PKey)
	mux.HandleFunc("POST /agent/p2p/audit", s.serveAgentP2PAudit)
}

func (s *Service) serveP2PConfig(w http.ResponseWriter, r *http.Request) {
	config, err := s.p2pConfig(r)
	if err != nil {
		writeFleetFailure(w, err)
		return
	}
	writeFleetJSON(w, http.StatusOK, config)
}

type p2pGrantRequest struct {
	ClientPub string `json:"client_pub"`
}

func (s *Service) serveP2PGrant(w http.ResponseWriter, r *http.Request) {
	var request p2pGrantRequest
	if err := decodeFleetJSON(w, r, &request); err != nil {
		writeFleetFailure(w, err)
		return
	}
	clientPub, err := grant.ParseKey(request.ClientPub)
	if err != nil {
		writeFleetFailure(w, ipc.BadParam(errors.New("client_pub 必须是 base64url 编码的 Ed25519 公钥")))
		return
	}
	var token string
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		token = cookie.Value
	}
	view, err := s.IssueP2PGrant(r.Context(), identityFrom(r), r.PathValue("id"), clientPub, token)
	if errors.Is(err, errP2PSessionRevoked) {
		writeFleetJSON(w, http.StatusUnauthorized, ipc.Failure(ipc.NewError(ipc.CodeForbidden, bridgeSessionReloginMessage)))
		return
	}
	if err != nil {
		writeFleetFailure(w, err)
		return
	}
	writeFleetJSON(w, http.StatusOK, view)
}

type p2pKeyConfirmRequest struct {
	PublicKey string `json:"public_key"`
}

func (s *Service) serveP2PKeyConfirm(w http.ResponseWriter, r *http.Request) {
	var request p2pKeyConfirmRequest
	if err := decodeFleetJSON(w, r, &request); err != nil {
		writeFleetFailure(w, err)
		return
	}
	public, err := grant.ParseKey(request.PublicKey)
	if err != nil {
		writeFleetFailure(w, ipc.BadParam(errors.New("public_key 必须是 base64url 编码的 Ed25519 公钥")))
		return
	}
	if err := s.ConfirmP2PDeviceKey(r.Context(), identityFrom(r), r.PathValue("id"), public); err != nil {
		writeFleetFailure(w, err)
		return
	}
	writeFleetJSON(w, http.StatusOK, map[string]any{"ok": true, "state": agent.P2PKeyStateBound, "public_key": public.String()})
}

// serveAgentP2PConfig handles GET /agent/p2p/config. A GET carries no body, so the device id comes in
// X-NexTerm-Device-ID and the secret as an Authorization bearer token.
func (s *Service) serveAgentP2PConfig(w http.ResponseWriter, r *http.Request) {
	deviceID := strings.TrimSpace(r.Header.Get(agent.HeaderDeviceID))
	secret, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if deviceID == "" || !ok || secret == "" {
		writeFleetFailure(w, ipc.NewError(ipc.CodeForbidden, "设备凭据无效"))
		return
	}
	if _, err := s.AuthenticateAgent(r.Context(), deviceID, secret); err != nil {
		writeFleetFailure(w, err)
		return
	}
	config, err := s.p2pConfig(r)
	if err != nil {
		writeFleetFailure(w, err)
		return
	}
	key, err := s.deviceP2PKey(r.Context(), deviceID)
	if err != nil {
		writeFleetFailure(w, err)
		return
	}
	config.KeyState = newP2PKeyView(key).State
	writeFleetJSON(w, http.StatusOK, config)
}

func (s *Service) serveAgentP2PKey(w http.ResponseWriter, r *http.Request) {
	var request agent.P2PKeyRequest
	if err := decodeFleetJSON(w, r, &request); err != nil {
		writeFleetFailure(w, err)
		return
	}
	if _, err := s.AuthenticateAgent(r.Context(), request.DeviceID, request.Secret); err != nil {
		writeFleetFailure(w, err)
		return
	}
	public, err := grant.ParseKey(request.PublicKey)
	if err != nil {
		writeFleetFailure(w, ipc.BadParam(errors.New("public_key 必须是 base64url 编码的 Ed25519 公钥")))
		return
	}
	response, err := s.RegisterP2PDeviceKey(r.Context(), request.DeviceID, public)
	if err != nil {
		writeFleetFailure(w, err)
		return
	}
	writeFleetJSON(w, http.StatusOK, response)
}

func (s *Service) serveAgentP2PAudit(w http.ResponseWriter, r *http.Request) {
	var request agent.P2PAuditRequest
	if err := decodeFleetJSON(w, r, &request); err != nil {
		writeFleetFailure(w, err)
		return
	}
	ownerID, err := s.AuthenticateAgent(r.Context(), request.DeviceID, request.Secret)
	if err != nil {
		writeFleetFailure(w, err)
		return
	}
	if !s.p2p.audits.allow(request.DeviceID, s.now()) {
		writeFleetJSON(w, http.StatusTooManyRequests, ipc.Failure(ipc.NewError(ipc.CodeForbidden, "上报过于频繁，请稍后再试")))
		return
	}
	response, err := s.RecordP2PAudit(r.Context(), request.DeviceID, ownerID, request)
	if err != nil {
		writeFleetFailure(w, err)
		return
	}
	writeFleetJSON(w, http.StatusOK, response)
}
