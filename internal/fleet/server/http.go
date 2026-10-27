package fleetserver

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/version"
)

const (
	sessionCookieName = "nexterm_session"
	csrfHeaderName    = "X-NexTerm-CSRF"
	csrfPurpose       = "nexterm-csrf-v1"
	maxFleetBody      = 64 << 10
)

type identityContextKey struct{}

func withIdentity(r *http.Request, identity *account.Identity) *http.Request {
	ctx := ipc.WithUserID(r.Context(), identity.UserID)
	ctx = ipc.WithRole(ctx, string(identity.Role))
	return r.WithContext(context.WithValue(ctx, identityContextKey{}, identity))
}

func identityFrom(r *http.Request) *account.Identity {
	identity, _ := r.Context().Value(identityContextKey{}).(*account.Identity)
	return identity
}

// fleetCSRFToken follows the same stateless derivation rule as the account session: HMAC(sessionID); it expires in sync with the session.
func fleetCSRFToken(sessionID string) string {
	mac := hmac.New(sha256.New, []byte(sessionID))
	mac.Write([]byte(csrfPurpose))
	return hex.EncodeToString(mac.Sum(nil))
}

func fleetCSRFSafeEqual(sessionID, presented string) bool {
	expected, err := hex.DecodeString(fleetCSRFToken(sessionID))
	if err != nil {
		return false
	}
	provided, err := hex.DecodeString(presented)
	if err != nil || len(provided) != len(expected) {
		return false
	}
	return hmac.Equal(expected, provided)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func writeFleetJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeFleetFailure(w http.ResponseWriter, err error) {
	normalized := ipc.NormalizeError(err)
	status := http.StatusInternalServerError
	switch normalized.Code {
	case ipc.CodeBadParam:
		status = http.StatusBadRequest
	case ipc.CodeForbidden:
		status = http.StatusForbidden
	case ipc.CodeNotFound:
		status = http.StatusNotFound
	case ipc.CodeDisconnected:
		status = http.StatusServiceUnavailable
	}
	writeFleetJSON(w, status, ipc.Failure(normalized))
}

func decodeFleetJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxFleetBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return ipc.BadParam(fmt.Errorf("请求体不是合法 JSON: %w", err))
	}
	return nil
}

// fleetRoutePatterns are all route patterns claimed by the fleet handlers (device management + sharing);
// the Handler's internal mux and mounting into the shared mux share the same table, so the two paths never drift apart.
var fleetRoutePatterns = []string{
	"POST /device/enroll",
	"POST /device/enroll-codes",
	"GET /fleet/devices",
	"POST /fleet/devices/{id}/revoke",
	"POST /fleet/devices/{id}/autostart",
	"POST /fleet/devices/{id}/terminal-access",
	"GET /fleet/devices/{id}/metrics",
	"GET /fleet/devices/{id}/bridge",
	"GET /fleet/base-urls",
	"PUT /fleet/base-urls",
	"POST /agent/sync",
	"POST /agent/current-url",
	"GET /ws/device",
	"POST /share/host-shares",
	"GET /share/host-shares",
	"POST /share/host-shares/{id}/revoke",
	"GET /share/public/{token}",
	"GET /share/devices/{id}/terminal",
}

// Handler returns the standalone HTTP entry for fleet device management; under --auth=off all routes get 403.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	s.mountRoutes(mux)
	return s.fleetGuard(mux)
}

// Mount registers all fleet routes into the shared mux; every pattern points at the same guarded entry,
// and the guard's --auth=off hard boundary plus device credential auth are unaffected by how mounting is done.
func (s *Service) Mount(mux *http.ServeMux) {
	handler := s.Handler()
	for _, pattern := range fleetRoutePatterns {
		mux.Handle(pattern, handler)
	}
}

func (s *Service) mountRoutes(mux *http.ServeMux) {
	session := func(pattern string, handler http.HandlerFunc) {
		mux.Handle(pattern, s.requireFleetSession(handler))
	}
	sessionCSRF := func(pattern string, handler http.HandlerFunc) {
		mux.Handle(pattern, s.requireFleetSession(s.requireFleetCSRF(handler)))
	}
	adminCSRF := func(pattern string, handler http.HandlerFunc) {
		mux.Handle(pattern, s.requireFleetSession(s.requireFleetCSRF(s.requireFleetSuperadmin(handler))))
	}

	mux.HandleFunc("POST /device/enroll", s.serveDeviceEnroll)
	sessionCSRF("POST /device/enroll-codes", s.serveEnrollCodeIssue)

	session("GET /fleet/devices", s.serveDeviceList)
	sessionCSRF("POST /fleet/devices/{id}/revoke", s.serveDeviceRevoke)
	sessionCSRF("POST /fleet/devices/{id}/autostart", s.serveDeviceAutostart)
	sessionCSRF("POST /fleet/devices/{id}/terminal-access", s.serveDeviceTerminalAccess)
	session("GET /fleet/devices/{id}/metrics", s.serveDeviceMetrics)
	session("GET /fleet/devices/{id}/bridge", s.serveDeviceBridgeRelay)
	session("GET /fleet/sessions", s.serveSessionList)

	session("GET /fleet/base-urls", s.serveBaseURLsGet)
	adminCSRF("PUT /fleet/base-urls", s.serveBaseURLsPut)

	// Agent contract routes: device credential (device_id+secret) auth is written inside the handlers,
	// accepting neither session nor gateway, so no user-state middleware is wrapped around them.
	mux.HandleFunc("POST /agent/sync", s.serveAgentSync)
	mux.HandleFunc("POST /agent/current-url", s.serveAgentCurrentURL)
	mux.HandleFunc("GET /ws/device", s.serveDeviceWS)

	// Share management routes: account session required, writes additionally require CSRF.
	sessionCSRF("POST /share/host-shares", s.serveHostShareCreate)
	session("GET /share/host-shares", s.serveHostShareList)
	sessionCSRF("POST /share/host-shares/{id}/revoke", s.serveHostShareRevoke)

	// Share data plane: registered share terminals require an account session; like the other session routes,
	// the GET upgrade requires no CSRF. The legacy public-link route was removed; it answers 410 for every
	// request so an old URL can neither fall through to the SPA shell nor reach any data path.
	mux.HandleFunc("GET /share/public/{token}", serveSharePublicGone)
	session("GET /share/devices/{id}/terminal", s.serveShareTerminalOpen)
}

// fleetGuard enforces the --auth=off hard boundary (fleet depends on attributable identity; everything is closed when off),
// and parses the session cookie into an identity; anonymous requests still enter the public routes as usual.
func (s *Service) fleetGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if s.authOff {
			writeFleetJSON(w, http.StatusForbidden, ipc.Failure(ipc.NewError(ipc.CodeForbidden, "当前为共享工作区模式（未启用账号登录），设备管理不可用")))
			return
		}
		if cookie, err := r.Cookie(sessionCookieName); err == nil && cookie.Value != "" {
			if identity, err := s.accounts.ValidateSession(r.Context(), cookie.Value); err == nil {
				r = withIdentity(r, identity)
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Service) requireFleetSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity := identityFrom(r)
		if identity == nil {
			writeFleetJSON(w, http.StatusUnauthorized, ipc.Failure(ipc.NewError(ipc.CodeForbidden, "会话无效或缺失，请重新登录")))
			return
		}
		if identity.State == account.StateResetRequired {
			writeFleetJSON(w, http.StatusForbidden, ipc.Failure(ipc.NewError(ipc.CodeForbidden, "必须先完成密码重置")))
			return
		}
		locked, err := s.mfaEnrollLocked(r)
		if err != nil {
			writeFleetFailure(w, err)
			return
		}
		if locked {
			writeFleetJSON(w, http.StatusForbidden, ipc.Failure(ipc.NewError(ipc.CodeMFAEnrollmentRequired, "管理员已要求启用两步验证：完成 TOTP 绑定前，该账号只能使用绑定相关功能")))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// mfaEnrollAllowedPaths stays consistent with the main service's account_session.go:
// under the mfa_required policy, unenrolled sessions may only access the routes needed for enrollment (the enrollment surface is the main service's /auth/totp*).
var mfaEnrollAllowedPaths = map[string]bool{
	"/auth/me":           true,
	"/auth/totp":         true,
	"/auth/totp/setup":   true,
	"/auth/totp/confirm": true,
	"/auth/logout":       true,
	"/auth/logout-all":   true,
}

func (s *Service) mfaEnrollLocked(r *http.Request) (bool, error) {
	identity := identityFrom(r)
	if identity == nil || identity.MFAEnabled || identity.State == account.StateResetRequired || mfaEnrollAllowedPaths[r.URL.Path] {
		return false, nil
	}
	required, err := s.accounts.MFARequired(r.Context())
	if err != nil {
		return false, err
	}
	return required, nil
}

func (s *Service) requireFleetCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity := identityFrom(r)
		if identity == nil || !fleetCSRFSafeEqual(identity.SessionID, r.Header.Get(csrfHeaderName)) {
			writeFleetJSON(w, http.StatusForbidden, ipc.Failure(ipc.NewError(ipc.CodeForbidden, "CSRF 校验失败：页面已过期，请刷新后重试")))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Service) requireFleetSuperadmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity := identityFrom(r)
		if identity == nil || identity.Role != account.RoleSuperadmin {
			writeFleetJSON(w, http.StatusForbidden, ipc.Failure(ipc.NewError(ipc.CodeForbidden, "需要超级管理员权限")))
			return
		}
		next.ServeHTTP(w, r)
	})
}

type enrollCodeIssueRequest struct {
	TTLMS  int64  `json:"ttl_ms"`
	UserID string `json:"user_id"`
}

type enrollCodeView struct {
	Code      string `json:"code"`
	ExpiresAt int64  `json:"expires_at"`
}

func (s *Service) serveEnrollCodeIssue(w http.ResponseWriter, r *http.Request) {
	var request enrollCodeIssueRequest
	if err := decodeFleetJSON(w, r, &request); err != nil {
		writeFleetFailure(w, err)
		return
	}
	identity := identityFrom(r)
	code, expiresAt, err := s.IssueEnrollCode(r.Context(), identity, request.UserID, time.Duration(request.TTLMS)*time.Millisecond)
	if err != nil {
		writeFleetFailure(w, err)
		return
	}
	writeFleetJSON(w, http.StatusOK, enrollCodeView{Code: code, ExpiresAt: expiresAt})
}

type deviceEnrollRequest struct {
	Code       string `json:"code"`
	Name       string `json:"name"`
	Platform   string `json:"platform"`
	AppVersion string `json:"app_version"`
	Kind       string `json:"kind,omitempty"`
}

type deviceEnrollView struct {
	DeviceID          string         `json:"device_id"`
	Secret            string         `json:"secret"`
	BaseURLs          []BaseURLEntry `json:"base_urls"`
	MetricsIntervalMS int64          `json:"metrics_interval_ms"`
	DesiredAutostart  bool           `json:"desired_autostart"`
	TerminalEnabled   bool           `json:"terminal_enabled"`
	Kind              string         `json:"kind,omitempty"`
}

func (s *Service) serveDeviceEnroll(w http.ResponseWriter, r *http.Request) {
	var request deviceEnrollRequest
	if err := decodeFleetJSON(w, r, &request); err != nil {
		writeFleetFailure(w, err)
		return
	}
	key := "fleet-enroll|" + clientIP(r)
	if !s.enrollGate.Allow(key) {
		writeFleetJSON(w, http.StatusTooManyRequests, ipc.Failure(ipc.NewError(ipc.CodeForbidden, "尝试过于频繁，请稍后再试")))
		return
	}
	result, err := s.ConsumeEnrollCodeKind(r.Context(), request.Code, request.Name, request.Platform, request.AppVersion, request.Kind)
	if err != nil {
		s.enrollGate.RecordFailure(key)
		writeFleetFailure(w, err)
		return
	}
	s.enrollGate.RecordSuccess(key)
	writeFleetJSON(w, http.StatusOK, deviceEnrollView{
		DeviceID:          result.DeviceID,
		Secret:            result.Secret,
		BaseURLs:          result.BaseURLs,
		MetricsIntervalMS: result.MetricsIntervalMS,
		DesiredAutostart:  result.DesiredAutostart,
		TerminalEnabled:   result.TerminalEnabled,
		Kind:              result.Kind,
	})
}

// agentView.StateDigest is the device-side supervisor state digest (reported by the control channel hello,
// containing no credential material); it only lets an authorized browser complete the supervisor
// hello on /fleet/devices/{id}/bridge, and is empty when the device is offline or has not reported one.
type agentView struct {
	Platform         string       `json:"platform"`
	AppVersion       string       `json:"app_version"`
	DesiredAutostart bool         `json:"desired_autostart"`
	TerminalEnabled  bool         `json:"terminal_enabled"`
	CurrentURL       string       `json:"current_url"`
	ServiceState     ServiceState `json:"service_state"`
	LastSeenAt       int64        `json:"last_seen_at"`
	StateDigest      string       `json:"state_digest,omitempty"`
}

// deviceListView is the GET /fleet/devices body. ServerVersion is additive: older clients ignore it.
type deviceListView struct {
	Devices       []deviceView `json:"devices"`
	ServerVersion string       `json:"server_version"`
}

type deviceView struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Kind       string     `json:"kind"`
	CreatedAt  int64      `json:"created_at"`
	LastSeenAt int64      `json:"last_seen_at"`
	RevokedAt  int64      `json:"revoked_at"`
	Owner      *Owner     `json:"owner,omitempty"`
	Agent      *agentView `json:"agent,omitempty"`
}

func newDeviceView(device *Device) deviceView {
	view := deviceView{
		ID: device.ID, Name: device.Name, Kind: device.Kind,
		CreatedAt: device.CreatedAt, LastSeenAt: device.LastSeenAt, RevokedAt: device.RevokedAt,
		Owner: device.Owner,
	}
	if device.Agent != nil {
		view.Agent = &agentView{
			Platform: device.Agent.Platform, AppVersion: device.Agent.AppVersion,
			DesiredAutostart: device.Agent.DesiredAutostart, TerminalEnabled: device.Agent.TerminalEnabled,
			CurrentURL: device.Agent.CurrentURL, ServiceState: device.Agent.ServiceState, LastSeenAt: device.Agent.LastSeenAt,
		}
	}
	return view
}

func (s *Service) serveDeviceList(w http.ResponseWriter, r *http.Request) {
	devices, err := s.ListDevices(r.Context(), identityFrom(r))
	if err != nil {
		writeFleetFailure(w, err)
		return
	}
	views := make([]deviceView, 0, len(devices))
	for _, device := range devices {
		view := newDeviceView(device)
		if view.Agent != nil {
			if digest, ok := s.registry.StateDigest(device.ID); ok {
				view.Agent.StateDigest = digest
			}
		}
		views = append(views, view)
	}
	writeFleetJSON(w, http.StatusOK, deviceListView{Devices: views, ServerVersion: version.Version})
}

func (s *Service) serveDeviceRevoke(w http.ResponseWriter, r *http.Request) {
	if err := s.RevokeDevice(r.Context(), identityFrom(r), r.PathValue("id")); err != nil {
		writeFleetFailure(w, err)
		return
	}
	writeFleetJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type autostartRequest struct {
	Desired bool `json:"desired"`
}

func (s *Service) serveDeviceAutostart(w http.ResponseWriter, r *http.Request) {
	var request autostartRequest
	if err := decodeFleetJSON(w, r, &request); err != nil {
		writeFleetFailure(w, err)
		return
	}
	if err := s.SetDesiredAutostart(r.Context(), identityFrom(r), r.PathValue("id"), request.Desired); err != nil {
		writeFleetFailure(w, err)
		return
	}
	writeFleetJSON(w, http.StatusOK, map[string]bool{"ok": true, "desired_autostart": request.Desired})
}

type terminalAccessRequest struct {
	// A pointer so a missing field is rejected instead of silently meaning "off".
	Enabled *bool `json:"enabled"`
}

func (s *Service) serveDeviceTerminalAccess(w http.ResponseWriter, r *http.Request) {
	var request terminalAccessRequest
	if err := decodeFleetJSON(w, r, &request); err != nil {
		writeFleetFailure(w, err)
		return
	}
	if request.Enabled == nil {
		writeFleetFailure(w, ipc.BadParam(errors.New("缺少 enabled 字段")))
		return
	}
	if err := s.SetTerminalEnabled(r.Context(), identityFrom(r), r.PathValue("id"), *request.Enabled); err != nil {
		writeFleetFailure(w, err)
		return
	}
	writeFleetJSON(w, http.StatusOK, map[string]bool{"ok": true, "terminal_enabled": *request.Enabled})
}

func (s *Service) serveDeviceMetrics(w http.ResponseWriter, r *http.Request) {
	var since int64
	if raw := r.URL.Query().Get("since_ms"); raw != "" {
		if _, err := fmt.Sscanf(raw, "%d", &since); err != nil {
			writeFleetFailure(w, ipc.BadParam(fmt.Errorf("指标起始时间（毫秒时间戳）必须是整数")))
			return
		}
	}
	result, err := s.DeviceMetrics(r.Context(), identityFrom(r), r.PathValue("id"), since)
	if err != nil {
		writeFleetFailure(w, err)
		return
	}
	writeFleetJSON(w, http.StatusOK, result)
}

func (s *Service) serveBaseURLsGet(w http.ResponseWriter, r *http.Request) {
	entries, err := s.BaseURLs(r.Context())
	if err != nil {
		writeFleetFailure(w, err)
		return
	}
	writeFleetJSON(w, http.StatusOK, map[string][]BaseURLEntry{"base_urls": entries})
}

type baseURLsPutRequest struct {
	BaseURLs []BaseURLEntry `json:"base_urls"`
}

func (s *Service) serveBaseURLsPut(w http.ResponseWriter, r *http.Request) {
	var request baseURLsPutRequest
	if err := decodeFleetJSON(w, r, &request); err != nil {
		writeFleetFailure(w, err)
		return
	}
	entries, err := s.SetBaseURLs(r.Context(), request.BaseURLs)
	if err != nil {
		writeFleetFailure(w, err)
		return
	}
	writeFleetJSON(w, http.StatusOK, map[string][]BaseURLEntry{"base_urls": entries})
}
