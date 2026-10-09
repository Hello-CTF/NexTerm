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

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
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

// fleetRoutePatterns 是 fleet 处理器认领的全部路由模式 (设备管理 + 分享);
// Handler 内部 mux 与挂载到共享 mux 时共用同一张表, 保证两条路径不会漂移。
var fleetRoutePatterns = []string{
	"POST /device/enroll",
	"POST /device/enroll-codes",
	"GET /fleet/devices",
	"POST /fleet/devices/{id}/revoke",
	"POST /fleet/devices/{id}/autostart",
	"GET /fleet/devices/{id}/metrics",
	"GET /fleet/devices/{id}/bridge",
	"GET /fleet/base-urls",
	"PUT /fleet/base-urls",
	"POST /agent/sync",
	"POST /agent/current-url",
	"GET /ws/device",
	"POST /share/links",
	"GET /share/links",
	"POST /share/links/{id}/revoke",
	"POST /share/device-links",
	"GET /share/device-links",
	"POST /share/device-links/{id}/revoke",
	"POST /share/host-shares",
	"GET /share/host-shares",
	"POST /share/host-shares/{id}/revoke",
	"GET /share/public/{token}",
	"GET /share/public/device/{token}",
	"GET /share/devices/{id}/terminal",
}

// Handler 返回 fleet 设备管理的独立 HTTP 入口; --auth=off 下所有路由一律 403。
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	s.mountRoutes(mux)
	return s.fleetGuard(mux)
}

// Mount 把 fleet 全部路由注册进共享 mux; 每个模式都指向同一个带 guard 的
// 入口, guard 的 --auth=off 硬边界与设备凭证鉴权不受挂载方式影响。
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
	session("GET /fleet/devices/{id}/metrics", s.serveDeviceMetrics)
	session("GET /fleet/devices/{id}/bridge", s.serveDeviceBridgeRelay)

	session("GET /fleet/base-urls", s.serveBaseURLsGet)
	adminCSRF("PUT /fleet/base-urls", s.serveBaseURLsPut)

	// agent 合同路由: 设备凭证 (device_id+secret) 鉴权写在 handler 内,
	// 不接受会话/gateway/静态令牌, 因此不套任何用户态中间件。
	mux.HandleFunc("POST /agent/sync", s.serveAgentSync)
	mux.HandleFunc("POST /agent/current-url", s.serveAgentCurrentURL)
	mux.HandleFunc("GET /ws/device", s.serveDeviceWS)

	// 分享管理路由: 账号会话把关, 写操作另需 CSRF。
	sessionCSRF("POST /share/links", s.serveShareLinkCreate)
	session("GET /share/links", s.serveShareLinkList)
	sessionCSRF("POST /share/links/{id}/revoke", s.serveShareLinkRevoke)
	sessionCSRF("POST /share/device-links", s.serveDeviceShareLinkCreate)
	session("GET /share/device-links", s.serveDeviceShareLinkList)
	sessionCSRF("POST /share/device-links/{id}/revoke", s.serveDeviceShareLinkRevoke)
	sessionCSRF("POST /share/host-shares", s.serveHostShareCreate)
	session("GET /share/host-shares", s.serveHostShareList)
	sessionCSRF("POST /share/host-shares/{id}/revoke", s.serveHostShareRevoke)

	// 分享数据面: 公开链接以 token 为凭据 (匿名可达, 等同 /device/enroll),
	// 注册分享终端要求账号会话; 二者都是 GET 升级, 与其他 session 路由一样
	// 不要求 CSRF。公开链接的普通 GET (浏览器导航) 在 handler 内分流给静态
	// SPA 入口, 只有 WS upgrade 进入 token 鉴权。
	mux.HandleFunc("GET /share/public/{token}", s.serveSharePublicTerminal)
	mux.HandleFunc("GET /share/public/device/{token}", s.serveSharePublicDeviceTerminal)
	session("GET /share/devices/{id}/terminal", s.serveShareTerminalOpen)
}

// fleetGuard 落实 --auth=off 硬边界 (fleet 依赖可归因身份, off 下一律关闭),
// 并把会话 cookie 解析为身份; 匿名请求照常进入公开路由。
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
		if s.mfaEnrollLocked(r) {
			writeFleetJSON(w, http.StatusForbidden, ipc.Failure(ipc.NewError(ipc.CodeMFAEnrollmentRequired, "管理员已要求启用两步验证: 完成 TOTP 绑定前,该账号只能使用绑定相关功能")))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// mfaEnrollAllowedPaths 与主服务 account_session.go 保持一致:
// mfa_required 策略下未绑定会话只能访问绑定所需路由(绑定面在主服务 /auth/totp*)。
var mfaEnrollAllowedPaths = map[string]bool{
	"/auth/me":           true,
	"/auth/totp":         true,
	"/auth/totp/setup":   true,
	"/auth/totp/confirm": true,
	"/auth/logout":       true,
	"/auth/logout-all":   true,
}

func (s *Service) mfaEnrollLocked(r *http.Request) bool {
	identity := identityFrom(r)
	if identity == nil || identity.MFAEnabled || identity.State == account.StateResetRequired || mfaEnrollAllowedPaths[r.URL.Path] {
		return false
	}
	required, err := s.accounts.MFARequired(r.Context())
	return err == nil && required
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

// agentView.StateDigest 是设备端 supervisor 状态摘要 (控制通道 hello 上报,
// 不含凭据材料), 仅供已授权浏览器在 /fleet/devices/{id}/bridge 上完成 supervisor
// hello; 设备离线或未上报时为空。
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
