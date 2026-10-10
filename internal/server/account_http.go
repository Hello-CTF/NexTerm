package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	core "github.com/Hello-CTF/NexTerm/internal/app"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	syncservice "github.com/Hello-CTF/NexTerm/internal/sync"
	"github.com/Hello-CTF/NexTerm/internal/vault"
)

type accountUserView struct {
	ID                 string        `json:"id"`
	Username           string        `json:"username"`
	DisplayName        string        `json:"display_name"`
	Role               account.Role  `json:"role"`
	State              account.State `json:"state"`
	MustChangePassword bool          `json:"must_change_password"`
	MFAEnabled         bool          `json:"mfa_enabled"`
	CreatedAt          int64         `json:"created_at"`
	UpdatedAt          int64         `json:"updated_at"`
	LastLoginAt        int64         `json:"last_login_at"`
}

func newAccountUserView(user *account.User) accountUserView {
	return accountUserView{
		ID: user.ID, Username: user.Username, DisplayName: user.DisplayName,
		Role: user.Role, State: user.State, MustChangePassword: user.MustChangePassword,
		CreatedAt: user.CreatedAt, UpdatedAt: user.UpdatedAt, LastLoginAt: user.LastLoginAt,
	}
}

type dekEnvelopeRequest struct {
	DEKEnvelope      []byte `json:"dek_envelope"`
	KDFSalt          []byte `json:"kdf_salt"`
	KDFParams        string `json:"kdf_params"`
	RecoveryEnvelope []byte `json:"recovery_envelope"`
	RecoveryHash     string `json:"recovery_hash"`
}

func (request *dekEnvelopeRequest) toEnvelopes() *vault.UserDEKEnvelopes {
	return &vault.UserDEKEnvelopes{
		DEKEnvelope:      request.DEKEnvelope,
		KDFSalt:          request.KDFSalt,
		KDFParams:        request.KDFParams,
		RecoveryEnvelope: request.RecoveryEnvelope,
		RecoveryHash:     request.RecoveryHash,
	}
}

// validate 在任何账号写入之前调用, 避免信封缺失导致半初始化状态。
func (request *dekEnvelopeRequest) validate() error {
	if len(request.DEKEnvelope) == 0 || len(request.KDFSalt) == 0 || request.KDFParams == "" ||
		len(request.RecoveryEnvelope) == 0 || request.RecoveryHash == "" {
		return ipc.NewError(ipc.CodeBadParam, "参数错误: 账号加密密钥字段不完整")
	}
	return nil
}

type accountDeviceView struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	CreatedAt  int64  `json:"created_at"`
	LastSeenAt int64  `json:"last_seen_at"`
	RevokedAt  int64  `json:"revoked_at"`
}

func writeAccountJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeAccountError(w http.ResponseWriter, status int, err *ipc.Error) {
	writeAccountJSON(w, status, ipc.Failure(err))
}

func writeAccountFailure(w http.ResponseWriter, err error) {
	normalized := ipc.NormalizeError(err)
	writeAccountError(w, accountErrorStatus(normalized), normalized)
}

func accountErrorStatus(err *ipc.Error) int {
	switch err.Code {
	case ipc.CodeBadParam:
		return http.StatusBadRequest
	case ipc.CodeForbidden, ipc.CodeMFAEnrollmentRequired:
		return http.StatusForbidden
	case ipc.CodeNotFound:
		return http.StatusNotFound
	default:
		return http.StatusInternalServerError
	}
}

func decodeAccountJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxAccountBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return ipc.BadParam(fmt.Errorf("请求体不是合法 JSON: %w", err))
	}
	return nil
}

func (s *Server) mountAccountRoutes(mux *http.ServeMux) {
	if s.accounts == nil {
		return
	}
	public := func(pattern string, handler http.HandlerFunc) {
		mux.Handle(pattern, s.accountGuard(handler))
	}
	session := func(pattern string, handler http.HandlerFunc) {
		mux.Handle(pattern, s.accountGuard(s.requireAccountSession(handler)))
	}
	sessionCSRF := func(pattern string, handler http.HandlerFunc) {
		mux.Handle(pattern, s.accountGuard(s.requireAccountSession(s.requireAccountCSRF(handler))))
	}
	admin := func(pattern string, handler http.HandlerFunc) {
		mux.Handle(pattern, s.accountGuard(s.requireAccountSession(s.requireSuperadmin(handler))))
	}
	adminCSRF := func(pattern string, handler http.HandlerFunc) {
		mux.Handle(pattern, s.accountGuard(s.requireAccountSession(s.requireAccountCSRF(s.requireSuperadmin(handler)))))
	}

	public("GET /auth/status", s.serveAccountStatus)
	public("POST /auth/init", s.serveAccountInit)
	public("POST /auth/platform-session", s.servePlatformSession)
	public("POST /auth/login", s.serveAccountLogin)
	public("POST /auth/totp/login", s.serveAccountTOTPLogin)
	public("POST /auth/register", s.serveAccountRegister)
	public("POST /auth/recovery/reset", s.serveAccountRecoveryReset)
	public("POST /auth/devices/enroll", s.serveAccountDeviceEnroll)

	session("GET /auth/me", s.serveAccountMe)
	session("GET /auth/dek", s.serveAccountDEKGet)
	session("GET /auth/devices", s.serveAccountDeviceList)
	session("GET /auth/totp", s.serveAccountTOTPStatus)
	sessionCSRF("POST /auth/logout", s.serveAccountLogout)
	sessionCSRF("POST /auth/logout-all", s.serveAccountLogoutAll)
	sessionCSRF("POST /auth/password", s.serveAccountPasswordChange)
	sessionCSRF("POST /auth/dek", s.serveAccountDEKUpload)
	sessionCSRF("POST /auth/devices/enroll-code", s.serveAccountEnrollCode)
	sessionCSRF("POST /auth/totp/setup", s.serveAccountTOTPSetup)
	sessionCSRF("POST /auth/totp/confirm", s.serveAccountTOTPConfirm)
	sessionCSRF("DELETE /auth/totp", s.serveAccountTOTPDisable)
	sessionCSRF("DELETE /auth/devices/{id}", s.serveAccountDeviceRevoke)

	admin("GET /admin/users", s.serveAdminUserList)
	admin("GET /admin/settings", s.serveAdminSettingsGet)
	adminCSRF("POST /admin/users", s.serveAdminUserCreate)
	adminCSRF("POST /admin/users/{id}/disable", s.serveAdminUserDisable)
	adminCSRF("POST /admin/users/{id}/reset", s.serveAdminUserReset)
	adminCSRF("PUT /admin/settings", s.serveAdminSettingsPut)

	if !s.options.SyncOnly && s.previews != nil && s.spectator != nil {
		session("GET /share/previews", s.servePreviewLinkList)
		sessionCSRF("POST /share/previews", s.servePreviewLinkCreate)
		sessionCSRF("POST /share/previews/{id}/revoke", s.servePreviewLinkRevoke)
	}

	s.mountPreferenceRoutes(mux)
}

type accountStatusView struct {
	Initialized      bool   `json:"initialized"`
	RegistrationOpen bool   `json:"registration_open"`
	Auth             string `json:"auth"`
}

// effectiveAuthMode 上报有效访问控制模式而非配置值: loopback 模式监听非回环地址时
// 等同 on(与 authRequired 的推导和 README 口径一致), 浏览器端据此决定是否允许匿名。
func (s *Server) effectiveAuthMode() string {
	if s.options.Auth == AuthLoopback && !core.LoopbackListen(s.options.Listen) {
		return AuthOn
	}
	return s.options.Auth
}

func (s *Server) serveAccountStatus(w http.ResponseWriter, r *http.Request) {
	users, err := s.accounts.CountUsers(r.Context())
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
	registration, err := s.accounts.RegistrationEnabled(r.Context())
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
	writeAccountJSON(w, http.StatusOK, accountStatusView{Initialized: users > 0, RegistrationOpen: registration, Auth: s.effectiveAuthMode()})
}

type accountInitRequest struct {
	Code     string `json:"code"`
	Username string `json:"username"`
	Password string `json:"password"`
	dekEnvelopeRequest
}

type accountSessionView struct {
	User      accountUserView `json:"user"`
	CSRFToken string          `json:"csrf_token"`
	// MFARequired 随会话下发 mfa_required 策略, 前端据此把未绑定会话引入强制绑定门。
	MFARequired bool `json:"mfa_required"`
}

func (s *Server) newAccountSessionView(r *http.Request, user *account.User, sessionID string) (accountSessionView, error) {
	view := newAccountUserView(user)
	enabled, err := s.accounts.TOTPEnabled(r.Context(), user.ID)
	if err != nil {
		return accountSessionView{}, err
	}
	required, err := s.accounts.MFARequired(r.Context())
	if err != nil {
		return accountSessionView{}, err
	}
	view.MFAEnabled = enabled
	return accountSessionView{User: view, CSRFToken: accountCSRFToken(sessionID), MFARequired: required}, nil
}

func (s *Server) writeAccountSession(w http.ResponseWriter, r *http.Request, user *account.User, deviceID string) {
	token, session, err := s.accounts.IssueSession(r.Context(), user.ID, deviceID)
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
	view, err := s.newAccountSessionView(r, user, session.ID)
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
	setSessionCookie(w, r, token)
	writeAccountJSON(w, http.StatusOK, view)
}

func (s *Server) serveAccountInit(w http.ResponseWriter, r *http.Request) {
	// 平台托管模式下没有码可消费: 账号由首个经网关到达的请求自动建立。
	if s.options.Auth == AuthPlatform {
		writeAccountError(w, http.StatusForbidden, ipc.NewError(ipc.CodeForbidden, "当前为平台托管模式（--auth=platform）：账号随平台登录自动建立，不需要初始化码"))
		return
	}
	var request accountInitRequest
	if err := decodeAccountJSON(w, r, &request); err != nil {
		writeAccountFailure(w, err)
		return
	}
	if err := request.dekEnvelopeRequest.validate(); err != nil {
		writeAccountFailure(w, err)
		return
	}
	key := ipThrottleKey(r, "init")
	if !s.accountThrottle.init.Allow(key) {
		writeAccountError(w, http.StatusTooManyRequests, ipc.NewError(ipc.CodeForbidden, "尝试过于频繁，请稍后再试"))
		return
	}
	user, err := s.accounts.InitSuperadminWithEnvelopes(r.Context(), request.Code, request.Username, request.Password, request.toEnvelopes())
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
	s.accountThrottle.init.RecordSuccess(key)
	s.writeAccountSession(w, r, user, "")
}

// servePlatformSession 把「已经过前置网关认证」的请求升级为一个真实账号会话。
//
// 平台模式的信任根是网关注入的请求头（NEXTERM_GATEWAY_AUTH）：能走到这里就说明平台
// 已经完成了登录认证。但那个头只说「过了门」、不说「是谁」，所以这里维持一个保留的
// 单所有者账号 —— 首个这样的请求创建它并签发会话，之后每次打开顺手续用。用户既不抄
// 初始化码，也不需要口令。
func (s *Server) servePlatformSession(w http.ResponseWriter, r *http.Request) {
	if s.options.Auth != AuthPlatform {
		writeAccountError(w, http.StatusForbidden, ipc.NewError(ipc.CodeForbidden, "当前部署不是平台托管模式"))
		return
	}
	if !syncservice.GatewayAuthorized(r, s.gatewayAuthKey) {
		writeAccountError(w, http.StatusForbidden, ipc.NewError(ipc.CodeForbidden, "缺少网关凭据：请从平台入口访问"))
		return
	}
	// 已有有效会话就续用, 避免每次刷新都新开一行会话。
	if token, ok := sessionCookie(r); ok {
		if identity, err := s.accounts.ValidateSession(r.Context(), token); err == nil {
			if user, err := s.accounts.GetUser(r.Context(), identity.UserID); err == nil {
				view, err := s.newAccountSessionView(r, user, identity.SessionID)
				if err != nil {
					writeAccountFailure(w, err)
					return
				}
				writeAccountJSON(w, http.StatusOK, view)
				return
			}
		}
	}
	owner, err := s.accounts.EnsurePlatformOwner(r.Context())
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
	s.writeAccountSession(w, r, owner, "")
}

type accountLoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	DeviceID string `json:"device_id"`
}

func (s *Server) serveAccountLogin(w http.ResponseWriter, r *http.Request) {
	var request accountLoginRequest
	if err := decodeAccountJSON(w, r, &request); err != nil {
		writeAccountFailure(w, err)
		return
	}
	key := loginThrottleKey(r, request.Username)
	if !s.accountThrottle.login.Allow(key) {
		writeAccountError(w, http.StatusTooManyRequests, ipc.NewError(ipc.CodeForbidden, "登录尝试过于频繁，请稍后再试"))
		return
	}
	user, err := s.accounts.Authenticate(r.Context(), request.Username, request.Password)
	if err != nil {
		s.accountThrottle.login.RecordFailure(key)
		writeAccountFailure(w, err)
		return
	}
	s.accountThrottle.login.RecordSuccess(key)
	mfaEnabled, err := s.accounts.TOTPEnabled(r.Context(), user.ID)
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
	if mfaEnabled {
		ticket, expiresAt := s.mfaTickets.issue(user.ID, request.DeviceID)
		if ticket == "" {
			writeAccountError(w, http.StatusInternalServerError, ipc.NewError(ipc.CodeInternal, "登录票据签发失败"))
			return
		}
		writeAccountJSON(w, http.StatusOK, accountMFABeginView{MFARequired: true, Ticket: ticket, ExpiresAt: expiresAt.UnixMilli()})
		return
	}
	s.writeAccountSession(w, r, user, request.DeviceID)
}

type accountRegisterRequest struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
	dekEnvelopeRequest
}

func (s *Server) serveAccountRegister(w http.ResponseWriter, r *http.Request) {
	var request accountRegisterRequest
	if err := decodeAccountJSON(w, r, &request); err != nil {
		writeAccountFailure(w, err)
		return
	}
	if err := request.dekEnvelopeRequest.validate(); err != nil {
		writeAccountFailure(w, err)
		return
	}
	key := ipThrottleKey(r, "register")
	if !s.accountThrottle.register.Allow(key) {
		writeAccountError(w, http.StatusTooManyRequests, ipc.NewError(ipc.CodeForbidden, "尝试过于频繁，请稍后再试"))
		return
	}
	registration, err := s.accounts.RegistrationEnabled(r.Context())
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
	if !registration {
		writeAccountError(w, http.StatusForbidden, ipc.NewError(ipc.CodeForbidden, "注册已关闭，请联系管理员添加账号"))
		return
	}
	user, err := s.accounts.CreateUserWithEnvelopes(r.Context(), request.Username, request.DisplayName, request.Password, request.toEnvelopes())
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
	s.accountThrottle.register.RecordSuccess(key)
	s.writeAccountSession(w, r, user, "")
}

type accountRecoveryResetRequest struct {
	Username    string `json:"username"`
	RecoveryKey string `json:"recovery_key"`
	NewPassword string `json:"new_password"`
	dekEnvelopeRequest
}

func (s *Server) serveAccountRecoveryReset(w http.ResponseWriter, r *http.Request) {
	var request accountRecoveryResetRequest
	if err := decodeAccountJSON(w, r, &request); err != nil {
		writeAccountFailure(w, err)
		return
	}
	key := recoveryThrottleKey(r, request.Username)
	if !s.accountThrottle.recovery.Allow(key) {
		writeAccountError(w, http.StatusTooManyRequests, ipc.NewError(ipc.CodeForbidden, "尝试过于频繁，请稍后再试"))
		return
	}
	err := s.accounts.ResetPasswordWithRecovery(r.Context(), request.Username, request.RecoveryKey, request.NewPassword, request.toEnvelopes())
	if err != nil {
		s.accountThrottle.recovery.RecordFailure(key)
		writeAccountFailure(w, err)
		return
	}
	s.accountThrottle.recovery.RecordSuccess(key)
	writeAccountJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type accountEnrollRequest struct {
	Code string `json:"code"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

func (s *Server) serveAccountDeviceEnroll(w http.ResponseWriter, r *http.Request) {
	var request accountEnrollRequest
	if err := decodeAccountJSON(w, r, &request); err != nil {
		writeAccountFailure(w, err)
		return
	}
	key := ipThrottleKey(r, "enroll")
	if !s.accountThrottle.enroll.Allow(key) {
		writeAccountError(w, http.StatusTooManyRequests, ipc.NewError(ipc.CodeForbidden, "尝试过于频繁，请稍后再试"))
		return
	}
	userID, err := s.accounts.ConsumeEnrollCode(r.Context(), request.Code)
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
	device, err := s.accounts.RegisterDevice(r.Context(), userID, request.Name, request.Kind)
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
	s.accountThrottle.enroll.RecordSuccess(key)
	writeAccountJSON(w, http.StatusOK, map[string]accountDeviceView{"device": newAccountDeviceView(device)})
}

func newAccountDeviceView(device *account.Device) accountDeviceView {
	return accountDeviceView{
		ID: device.ID, Name: device.Name, Kind: device.Kind,
		CreatedAt: device.CreatedAt, LastSeenAt: device.LastSeenAt, RevokedAt: device.RevokedAt,
	}
}

func (s *Server) serveAccountMe(w http.ResponseWriter, r *http.Request) {
	identity := accountIdentityFrom(r.Context())
	user, err := s.accounts.GetUser(r.Context(), identity.UserID)
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
	view, err := s.newAccountSessionView(r, user, identity.SessionID)
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
	writeAccountJSON(w, http.StatusOK, view)
}

func (s *Server) serveAccountLogout(w http.ResponseWriter, r *http.Request) {
	identity := accountIdentityFrom(r.Context())
	if err := s.accounts.RevokeSession(r.Context(), identity.SessionID); err != nil {
		writeAccountFailure(w, err)
		return
	}
	clearSessionCookie(w, r)
	writeAccountJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) serveAccountLogoutAll(w http.ResponseWriter, r *http.Request) {
	identity := accountIdentityFrom(r.Context())
	revoked, err := s.accounts.RevokeUserSessions(r.Context(), identity.UserID, "")
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
	clearSessionCookie(w, r)
	writeAccountJSON(w, http.StatusOK, map[string]int64{"revoked": revoked})
}

type accountPasswordChangeRequest struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
	dekEnvelopeRequest
}

func (s *Server) serveAccountPasswordChange(w http.ResponseWriter, r *http.Request) {
	identity := accountIdentityFrom(r.Context())
	var request accountPasswordChangeRequest
	if err := decodeAccountJSON(w, r, &request); err != nil {
		writeAccountFailure(w, err)
		return
	}
	err := s.accounts.ChangePassword(r.Context(), identity.UserID, request.OldPassword, request.NewPassword, request.toEnvelopes(), identity.SessionID)
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
	writeAccountJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type accountDEKView struct {
	DEKEnvelope      []byte `json:"dek_envelope"`
	KDFSalt          []byte `json:"kdf_salt"`
	KDFParams        string `json:"kdf_params"`
	RecoveryEnvelope []byte `json:"recovery_envelope"`
	RecoveryHash     string `json:"recovery_hash"`
}

// serveAccountDEKGet 只返回加密信封; 明文 DEK 永不出服务端(服务端本身不持有)。
func (s *Server) serveAccountDEKGet(w http.ResponseWriter, r *http.Request) {
	identity := accountIdentityFrom(r.Context())
	envelopes, err := s.accounts.GetUserDEKEnvelopes(r.Context(), identity.UserID)
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
	writeAccountJSON(w, http.StatusOK, accountDEKView{
		DEKEnvelope:      envelopes.DEKEnvelope,
		KDFSalt:          envelopes.KDFSalt,
		KDFParams:        envelopes.KDFParams,
		RecoveryEnvelope: envelopes.RecoveryEnvelope,
		RecoveryHash:     envelopes.RecoveryHash,
	})
}

// serveAccountDEKUpload 供管理员创建的用户在首次登录后上传初始信封; 已存在时拒绝覆盖。
func (s *Server) serveAccountDEKUpload(w http.ResponseWriter, r *http.Request) {
	identity := accountIdentityFrom(r.Context())
	var request dekEnvelopeRequest
	if err := decodeAccountJSON(w, r, &request); err != nil {
		writeAccountFailure(w, err)
		return
	}
	err := s.accounts.InsertUserDEKEnvelopes(r.Context(), identity.UserID, request.toEnvelopes())
	if err != nil {
		if errors.Is(err, account.ErrDEKEnvelopesExist) {
			writeAccountError(w, http.StatusConflict, ipc.NewError(ipc.CodeBadParam, "账号加密密钥已存在，无需重复上传"))
			return
		}
		writeAccountFailure(w, err)
		return
	}
	writeAccountJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) serveAccountDeviceList(w http.ResponseWriter, r *http.Request) {
	identity := accountIdentityFrom(r.Context())
	devices, err := s.accounts.ListDevices(r.Context(), identity.UserID)
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
	views := make([]accountDeviceView, 0, len(devices))
	for _, device := range devices {
		views = append(views, newAccountDeviceView(device))
	}
	writeAccountJSON(w, http.StatusOK, map[string][]accountDeviceView{"devices": views})
}

type accountEnrollCodeView struct {
	Code      string `json:"code"`
	ExpiresAt int64  `json:"expires_at"`
}

func (s *Server) serveAccountEnrollCode(w http.ResponseWriter, r *http.Request) {
	identity := accountIdentityFrom(r.Context())
	code, err := s.accounts.IssueEnrollCode(r.Context(), identity.UserID)
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
	writeAccountJSON(w, http.StatusOK, accountEnrollCodeView{Code: code, ExpiresAt: time.Now().Add(account.EnrollCodeTTL).UnixMilli()})
}

func (s *Server) serveAccountDeviceRevoke(w http.ResponseWriter, r *http.Request) {
	identity := accountIdentityFrom(r.Context())
	if err := s.accounts.RevokeDevice(r.Context(), identity.UserID, r.PathValue("id")); err != nil {
		writeAccountFailure(w, err)
		return
	}
	writeAccountJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) serveAdminUserList(w http.ResponseWriter, r *http.Request) {
	users, err := s.accounts.ListUsers(r.Context())
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
	mfaEnabled, err := s.accounts.TOTPEnabledMap(r.Context())
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
	views := make([]accountUserView, 0, len(users))
	for _, user := range users {
		view := newAccountUserView(user)
		view.MFAEnabled = mfaEnabled[user.ID]
		views = append(views, view)
	}
	writeAccountJSON(w, http.StatusOK, map[string][]accountUserView{"users": views})
}

type accountAdminCreateRequest struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
}

func (s *Server) serveAdminUserCreate(w http.ResponseWriter, r *http.Request) {
	var request accountAdminCreateRequest
	if err := decodeAccountJSON(w, r, &request); err != nil {
		writeAccountFailure(w, err)
		return
	}
	user, err := s.accounts.CreateUser(r.Context(), request.Username, request.DisplayName, request.Password)
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
	writeAccountJSON(w, http.StatusOK, map[string]accountUserView{"user": newAccountUserView(user)})
}

func (s *Server) serveAdminUserDisable(w http.ResponseWriter, r *http.Request) {
	identity := accountIdentityFrom(r.Context())
	target := r.PathValue("id")
	if target == identity.UserID {
		writeAccountError(w, http.StatusBadRequest, ipc.NewError(ipc.CodeBadParam, "不能禁用当前登录账号"))
		return
	}
	if err := s.accounts.SetUserDisabled(r.Context(), target, true); err != nil {
		writeAccountFailure(w, err)
		return
	}
	writeAccountJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) serveAdminUserReset(w http.ResponseWriter, r *http.Request) {
	if err := s.accounts.AdminResetUser(r.Context(), r.PathValue("id")); err != nil {
		writeAccountFailure(w, err)
		return
	}
	writeAccountJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type accountAdminSettingsView struct {
	RegistrationOpen bool `json:"registration_open"`
	MFARequired      bool `json:"mfa_required"`
}

// accountAdminSettingsPutRequest 用指针区分「未携带」(保持不变)与「空串」(清除)。
type accountAdminSettingsPutRequest struct {
	RegistrationOpen bool  `json:"registration_open"`
	MFARequired      *bool `json:"mfa_required"`
}

func (s *Server) serveAdminSettingsGet(w http.ResponseWriter, r *http.Request) {
	registration, err := s.accounts.RegistrationEnabled(r.Context())
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
	mfaRequired, err := s.accounts.MFARequired(r.Context())
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
	writeAccountJSON(w, http.StatusOK, accountAdminSettingsView{RegistrationOpen: registration, MFARequired: mfaRequired})
}

func (s *Server) serveAdminSettingsPut(w http.ResponseWriter, r *http.Request) {
	var request accountAdminSettingsPutRequest
	if err := decodeAccountJSON(w, r, &request); err != nil {
		writeAccountFailure(w, err)
		return
	}
	if request.MFARequired != nil {
		if err := s.accounts.SetMFARequired(r.Context(), *request.MFARequired); err != nil {
			writeAccountFailure(w, err)
			return
		}
	}
	if err := s.accounts.SetRegistrationEnabled(r.Context(), request.RegistrationOpen); err != nil {
		writeAccountFailure(w, err)
		return
	}
	s.serveAdminSettingsGet(w, r)
}
