package fleetserver

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/account"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

const (
	sessionCookieName = "nexterm_session"
	csrfHeaderName    = "X-NexTerm-CSRF"
	csrfPurpose       = "nexterm-csrf-v1"
	maxFleetBody      = 64 << 10
)

type identityContextKey struct{}

func withIdentity(r *http.Request, identity *account.Identity) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), identityContextKey{}, identity))
}

func identityFrom(r *http.Request) *account.Identity {
	identity, _ := r.Context().Value(identityContextKey{}).(*account.Identity)
	return identity
}

// fleetCSRFToken 与账号会话同一套无状态派生规则: HMAC(sessionID), 会话失效即同步失效。
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

// Handler 返回 fleet 设备管理的独立 HTTP 入口; --auth=off 下所有路由一律 403。
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
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
	session("GET /fleet/devices/{id}/metrics", s.serveDeviceMetrics)

	session("GET /fleet/base-urls", s.serveBaseURLsGet)
	adminCSRF("PUT /fleet/base-urls", s.serveBaseURLsPut)

	return s.fleetGuard(mux)
}

// fleetGuard 落实 --auth=off 硬边界 (fleet 依赖可归因身份, off 下一律关闭),
// 并把会话 cookie 解析为身份; 匿名请求照常进入公开路由。
func (s *Service) fleetGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if s.authOff {
			writeFleetJSON(w, http.StatusForbidden, ipc.Failure(ipc.NewError(ipc.CodeForbidden, "--auth=off 下设备管理已关闭")))
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
			writeFleetJSON(w, http.StatusUnauthorized, ipc.Failure(ipc.NewError(ipc.CodeForbidden, "会话无效或缺失")))
			return
		}
		if identity.State == account.StateResetRequired {
			writeFleetJSON(w, http.StatusForbidden, ipc.Failure(ipc.NewError(ipc.CodeForbidden, "必须先完成密码重置")))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Service) requireFleetCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity := identityFrom(r)
		if identity == nil || !fleetCSRFSafeEqual(identity.SessionID, r.Header.Get(csrfHeaderName)) {
			writeFleetJSON(w, http.StatusForbidden, ipc.Failure(ipc.NewError(ipc.CodeForbidden, "CSRF 校验失败")))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Service) requireFleetSuperadmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity := identityFrom(r)
		if identity == nil || identity.Role != account.RoleSuperadmin {
			writeFleetJSON(w, http.StatusForbidden, ipc.Failure(ipc.NewError(ipc.CodeForbidden, "需要超管权限")))
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
}

type deviceEnrollView struct {
	DeviceID          string         `json:"device_id"`
	Secret            string         `json:"secret"`
	BaseURLs          []BaseURLEntry `json:"base_urls"`
	MetricsIntervalMS int64          `json:"metrics_interval_ms"`
	DesiredAutostart  bool           `json:"desired_autostart"`
	TerminalEnabled   bool           `json:"terminal_enabled"`
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
	result, err := s.ConsumeEnrollCode(r.Context(), request.Code, request.Name, request.Platform, request.AppVersion)
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
	})
}

type agentView struct {
	Platform         string       `json:"platform"`
	AppVersion       string       `json:"app_version"`
	DesiredAutostart bool         `json:"desired_autostart"`
	TerminalEnabled  bool         `json:"terminal_enabled"`
	CurrentURL       string       `json:"current_url"`
	ServiceState     ServiceState `json:"service_state"`
	LastSeenAt       int64        `json:"last_seen_at"`
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
		views = append(views, newDeviceView(device))
	}
	writeFleetJSON(w, http.StatusOK, map[string][]deviceView{"devices": views})
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

func (s *Service) serveDeviceMetrics(w http.ResponseWriter, r *http.Request) {
	var since int64
	if raw := r.URL.Query().Get("since_ms"); raw != "" {
		if _, err := fmt.Sscanf(raw, "%d", &since); err != nil {
			writeFleetFailure(w, ipc.BadParam(fmt.Errorf("since_ms 必须是整数")))
			return
		}
	}
	samples, err := s.DeviceMetrics(r.Context(), identityFrom(r), r.PathValue("id"), since)
	if err != nil {
		writeFleetFailure(w, err)
		return
	}
	writeFleetJSON(w, http.StatusOK, map[string][]*MetricsSample{"samples": samples})
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
