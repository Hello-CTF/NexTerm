package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

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

// validate is called before any account write, avoiding a half-initialized state from missing envelopes.
func (request *dekEnvelopeRequest) validate() error {
	if len(request.DEKEnvelope) == 0 || len(request.KDFSalt) == 0 || request.KDFParams == "" ||
		len(request.RecoveryEnvelope) == 0 || request.RecoveryHash == "" {
		return ipc.NewError(ipc.CodeBadParam, "参数错误: 数据密钥封装字段不完整")
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

type accountSessionInfoView struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Origin     string `json:"origin"`
	DeviceID   string `json:"device_id,omitempty"`
	DeviceName string `json:"device_name,omitempty"`
	CreatedAt  int64  `json:"created_at"`
	LastSeenAt int64  `json:"last_seen_at"`
	ExpiresAt  int64  `json:"expires_at"`
	Current    bool   `json:"current"`
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

type adminRoute struct {
	name         string
	pattern      string
	csrfRequired bool
	handler      http.HandlerFunc
}

func (s *Server) adminRoutes() []adminRoute {
	return []adminRoute{
		{name: "list_users", pattern: "GET /admin/users", handler: s.serveAdminUserList},
		{name: "get_settings", pattern: "GET /admin/settings", handler: s.serveAdminSettingsGet},
		{name: "create_user", pattern: "POST /admin/users", csrfRequired: true, handler: s.serveAdminUserCreate},
		{name: "disable_user", pattern: "POST /admin/users/{id}/disable", csrfRequired: true, handler: s.serveAdminUserDisable},
		{name: "reset_user", pattern: "POST /admin/users/{id}/reset", csrfRequired: true, handler: s.serveAdminUserReset},
		{name: "put_settings", pattern: "PUT /admin/settings", csrfRequired: true, handler: s.serveAdminSettingsPut},
		{name: "get_preferences", pattern: "GET /admin/preferences", handler: s.serveAdminPreferencesGet},
		{name: "put_preferences", pattern: "PUT /admin/preferences", csrfRequired: true, handler: s.serveAdminPreferencesPut},
	}
}

func (s *Server) mountAdminRoutes(mux *http.ServeMux) {
	for _, route := range s.adminRoutes() {
		inner := route.handler
		if route.csrfRequired {
			inner = s.auditAdminRoute(route.name, inner)
		}
		handler := s.requireSuperadmin(inner)
		if route.csrfRequired {
			handler = s.requireAccountCSRF(handler)
		}
		mux.Handle(route.pattern, s.accountGuard(s.requireAccountSession(handler)))
	}
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

	public("GET /auth/status", s.serveAccountStatus)
	public("POST /auth/init", s.serveAccountInit)
	public("POST /auth/platform-session", s.servePlatformSession)
	public("POST /auth/login", s.serveAccountLogin)
	public("POST /auth/totp/login", s.serveAccountTOTPLogin)
	public("POST /auth/register", s.serveAccountRegister)
	public("POST /auth/recovery/reset", s.serveAccountRecoveryReset)

	session("GET /auth/me", s.serveAccountMe)
	session("GET /auth/dek", s.serveAccountDEKGet)
	session("GET /auth/sessions", s.serveAccountSessionList)
	session("GET /auth/devices", s.serveAccountDeviceList)
	session("GET /auth/totp", s.serveAccountTOTPStatus)
	sessionCSRF("POST /auth/logout", s.serveAccountLogout)
	sessionCSRF("POST /auth/logout-all", s.serveAccountLogoutAll)
	sessionCSRF("DELETE /auth/sessions/{id}", s.serveAccountSessionRevoke)
	sessionCSRF("POST /auth/password", s.serveAccountPasswordChange)
	sessionCSRF("POST /auth/dek", s.serveAccountDEKUpload)
	sessionCSRF("POST /auth/totp/setup", s.serveAccountTOTPSetup)
	sessionCSRF("POST /auth/totp/confirm", s.serveAccountTOTPConfirm)
	sessionCSRF("DELETE /auth/totp", s.serveAccountTOTPDisable)
	sessionCSRF("DELETE /auth/devices/{id}", s.serveAccountDeviceRevoke)

	s.mountAdminRoutes(mux)

	if !s.options.SyncOnly && s.previews != nil && s.spectator != nil {
		session("GET /share/previews", s.servePreviewLinkList)
		sessionCSRF("POST /share/previews", s.servePreviewLinkCreate)
		sessionCSRF("POST /share/previews/{id}/revoke", s.servePreviewLinkRevoke)
		if s.remoteSessions != nil {
			sessionCSRF("POST /share/remote-sessions", s.serveRemoteSessionCreate)
			sessionCSRF("GET /share/remote-sessions/{id}/feed", s.serveRemoteSessionFeed)
			sessionCSRF("DELETE /share/remote-sessions/{id}", s.serveRemoteSessionDelete)
		}
	}

	s.mountPreferenceRoutes(mux)
}

type accountStatusView struct {
	Initialized      bool   `json:"initialized"`
	RegistrationOpen bool   `json:"registration_open"`
	Auth             string `json:"auth"`
}

// effectiveAuthMode reports the effective access-control mode, not the configured value: loopback mode listening on a
// non-loopback address equals on (consistent with authRequired derivation and the README), and the browser side
// uses it to decide whether anonymous is allowed.
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
	// SessionTag identifies the login session without revealing its ID, so a
	// client can tell a refreshed CSRF token for the same session from a new
	// session.
	SessionTag string `json:"session_tag"`
	// MFARequired delivers the mfa_required policy with the session, letting the frontend gate unbound sessions into forced binding.
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
	csrfToken, err := s.accounts.CSRFToken(sessionID)
	if err != nil {
		return accountSessionView{}, err
	}
	return accountSessionView{User: view, CSRFToken: csrfToken, SessionTag: accountSessionTag(sessionID), MFARequired: required}, nil
}

// accountSessionTag is a one-way tag of the session ID. Unlike the CSRF token
// it does not depend on a server key, so it stays the same when that key
// changes.
func accountSessionTag(sessionID string) string {
	digest := sha256.Sum256([]byte("nexterm-session-tag\x00" + sessionID))
	return hex.EncodeToString(digest[:16])
}

// accountLoginGate, when non-nil, runs after a successful Authenticate and
// before the epoch capture, outside the revocation lock. Tests use it to
// interpose a password change mid-login.
var accountLoginGate func(ctx context.Context, user *account.User)

// SetAccountLoginGateForTest installs accountLoginGate and returns a restore
// function. It exists for tests only.
func SetAccountLoginGateForTest(gate func(ctx context.Context, user *account.User)) (restore func()) {
	previous := accountLoginGate
	accountLoginGate = gate
	return func() { accountLoginGate = previous }
}

// writeAccountSession issues the session under the caller-held revocation lock
// (the account async-boundary mutex). The epoch re-check fails the issuance
// closed when a revocation, password change, reset, or disable committed after
// the caller captured the generation at the authentication boundary. It
// returns the issued session ID, or "" when no session was issued.
func (s *Server) writeAccountSession(w http.ResponseWriter, r *http.Request, user *account.User, deviceID string, epoch uint64) string {
	if !s.accounts.CheckEpoch(user.ID, epoch) {
		writeAccountError(w, http.StatusForbidden, ipc.NewError(ipc.CodeForbidden, "账号状态已变更，请重新登录"))
		return ""
	}
	// Stamp the login first so a store error leaves no orphaned session behind.
	if err := s.accounts.RecordLogin(r.Context(), user); err != nil {
		writeAccountFailure(w, err)
		return ""
	}
	token, session, err := s.accounts.IssueSessionWithMetadata(r.Context(), user.ID, deviceID, account.SessionMetadata{
		ClientName: sessionClientName(r), Origin: clientIP(r),
	})
	if err != nil {
		writeAccountFailure(w, err)
		return ""
	}
	s.setLoginDeviceCookie(w, r, user)
	view, err := s.newAccountSessionView(r, user, session.ID)
	if err != nil {
		writeAccountFailure(w, err)
		return ""
	}
	s.setSessionCookie(w, r, token)
	writeAccountJSON(w, http.StatusOK, view)
	return session.ID
}

func (s *Server) serveAccountInit(w http.ResponseWriter, r *http.Request) {
	// In platform-managed mode there is no code to consume: the account is created automatically by the first request arriving through the gateway.
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
	s.accountRevocationMu.Lock()
	defer s.accountRevocationMu.Unlock()
	s.writeAccountSession(w, r, user, "", s.accounts.Epoch(user.ID))
}

// servePlatformSession upgrades a request that "already passed the front gateway's auth" into a real account session.
//
// The trust root of platform mode is the gateway-injected request header (NEXTERM_GATEWAY_AUTH): reaching here means the platform
// has already completed login authentication. But that header only says "passed the gate", not "who it is", so a reserved
// single-owner account is kept here - the first such request creates it and issues a session, and subsequent opens reuse it.
// The user neither copies an init code nor needs a password.
func (s *Server) servePlatformSession(w http.ResponseWriter, r *http.Request) {
	if s.options.Auth != AuthPlatform {
		writeAccountError(w, http.StatusForbidden, ipc.NewError(ipc.CodeForbidden, "当前部署不是平台托管模式"))
		return
	}
	if !syncservice.GatewayAuthorized(r, s.gatewayAuthKey) {
		writeAccountError(w, http.StatusForbidden, ipc.NewError(ipc.CodeForbidden, "缺少网关凭据：请从平台入口访问"))
		return
	}
	// An existing valid session is reused, avoiding a new session row on every refresh.
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
	s.accountRevocationMu.Lock()
	defer s.accountRevocationMu.Unlock()
	s.writeAccountSession(w, r, owner, "", s.accounts.Epoch(owner.ID))
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
	ipKey := ipThrottleKey(r, "login-ip")
	userKey := loginUserThrottleKey(request.Username)
	// Every bucket is checked before any spends an attempt, so a request one
	// bucket refuses costs the others nothing. A client holding a login device
	// token for this account is exempt from the per-username backoff, which
	// only exists to slow guessing spread over many addresses, and from the
	// shared overflow guards of full tables; its own guards still apply.
	var admitted bool
	if s.loginDeviceKnown(r, request.Username) {
		admitted = s.accountThrottle.loginIP.PeekOwn(ipKey) && s.accountThrottle.login.PeekOwn(key) &&
			s.accountThrottle.loginIP.AllowOwn(ipKey) && s.accountThrottle.login.AllowOwn(key)
	} else {
		admitted = s.accountThrottle.loginIP.Peek(ipKey) && s.accountThrottle.login.Peek(key) && s.accountThrottle.loginUser.Peek(userKey) &&
			s.accountThrottle.loginIP.Allow(ipKey) && s.accountThrottle.login.Allow(key)
	}
	if !admitted {
		s.metrics.login(loginStepPassword, loginThrottled)
		writeAccountError(w, http.StatusTooManyRequests, ipc.NewError(ipc.CodeForbidden, "登录尝试过于频繁，请稍后再试"))
		return
	}
	// The password hash is verified outside the revocation lock so slow key
	// derivation does not serialize every login with revocations. The lock is
	// taken afterwards and the account is re-read: a password change, reset or
	// disable that landed after verification fails the login closed, and one
	// that lands after the epoch capture fails the issuance epoch check.
	authenticated, err := s.accounts.Authenticate(r.Context(), request.Username, request.Password)
	if err != nil {
		s.accountThrottle.login.RecordFailure(key)
		s.accountThrottle.loginIP.RecordFailure(ipKey)
		s.accountThrottle.loginUser.RecordFailure(userKey)
		s.metrics.login(loginStepPassword, passwordLoginResult(err))
		writeAccountFailure(w, err)
		s.auditPasswordLoginFailure(r, request.Username, err)
		return
	}
	if accountLoginGate != nil {
		accountLoginGate(r.Context(), authenticated)
	}
	epoch := s.accounts.Epoch(authenticated.ID)
	audits := s.newAuditQueue(r)
	defer audits.flush()
	s.accountRevocationMu.Lock()
	defer s.accountRevocationMu.Unlock()
	user, err := s.accounts.ConfirmAuthenticated(r.Context(), authenticated)
	if err != nil {
		s.metrics.login(loginStepPassword, loginError)
		writeAccountFailure(w, err)
		return
	}
	mfaEnabled, err := s.accounts.TOTPEnabled(r.Context(), user.ID)
	if err != nil {
		s.metrics.login(loginStepPassword, loginError)
		writeAccountFailure(w, err)
		return
	}
	if mfaEnabled {
		// The password-step throttle stays armed until the second factor passes.
		ticket, expiresAt := s.mfaTickets.issue(user.ID, request.DeviceID, key, epoch)
		if ticket == "" {
			writeAccountError(w, http.StatusInternalServerError, ipc.NewError(ipc.CodeInternal, "登录票据签发失败"))
			return
		}
		s.metrics.login(loginStepPassword, loginMFARequired)
		writeAccountJSON(w, http.StatusOK, accountMFABeginView{MFARequired: true, Ticket: ticket, ExpiresAt: expiresAt.UnixMilli()})
		return
	}
	// The per-username streak is left alone: one success must not let an
	// attacker who shares the account's name restart guessing.
	s.accountThrottle.login.RecordSuccess(key)
	s.metrics.login(loginStepPassword, loginSuccess)
	audits.loginSuccess(user.ID, s.writeAccountSession(w, r, user, request.DeviceID, epoch), "password")
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
	s.accountRevocationMu.Lock()
	defer s.accountRevocationMu.Unlock()
	s.writeAccountSession(w, r, user, "", s.accounts.Epoch(user.ID))
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
	// Serialize with logins and envelope uploads: the recovery reset revokes
	// sessions and bumps the account epoch as one atomic generation change.
	audits := s.newAuditQueue(r)
	defer audits.flush()
	s.accountRevocationMu.Lock()
	defer s.accountRevocationMu.Unlock()
	err := s.accounts.ResetPasswordWithRecovery(r.Context(), request.Username, request.RecoveryKey, request.NewPassword, request.toEnvelopes())
	if err != nil {
		s.accountThrottle.recovery.RecordFailure(key)
		writeAccountFailure(w, err)
		return
	}
	s.accountThrottle.recovery.RecordSuccess(key)
	if user, lookupErr := s.accounts.GetUserByUsername(r.Context(), request.Username); lookupErr == nil && user != nil {
		s.unlockMFA(r, user.ID, "recovery_reset", "")
		audits.self(user.ID, "password_recovery_reset", nil)
		s.closeUserConnections(user.ID, "")
	}
	writeAccountJSON(w, http.StatusOK, map[string]bool{"ok": true})
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
	err := func() error {
		s.accountRevocationMu.Lock()
		defer s.accountRevocationMu.Unlock()
		commit, rollback, prepareErr := s.prepareAccountCleanup(r.Context(), identity.UserID, identity.SessionID)
		if prepareErr != nil {
			// The snapshot read failed: abort without revoking so the user can
			// retry the whole logout with the same event id once it recovers.
			rollback()
			return prepareErr
		}
		if err := s.accounts.RevokeSession(r.Context(), identity.SessionID); err != nil {
			rollback()
			return err
		}
		commit(identity.SessionID)
		return nil
	}()
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
	s.auditSelf(r, identity.UserID, "logout", map[string]any{"session_id": identity.SessionID})
	s.closeAccountConnections(identity.SessionID)
	s.clearSessionCookie(w, r)
	writeAccountJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) serveAccountLogoutAll(w http.ResponseWriter, r *http.Request) {
	identity := accountIdentityFrom(r.Context())
	sessionIDs, err := func() ([]string, error) {
		s.accountRevocationMu.Lock()
		defer s.accountRevocationMu.Unlock()
		var commit func(...string)
		rollback := func() {}
		// The prepare failure aborts the revocation transaction itself: no
		// session is revoked, and the whole logout-all retries with the same
		// event id once the snapshot read recovers.
		sessionIDs, err := s.revokeUserSessions(r.Context(), identity.UserID, "", func() error {
			var prepareErr error
			// The prefixed key is normalized back to this session ID by the
			// cleanup generation-bound read; keep the two in sync.
			commit, rollback, prepareErr = s.prepareAccountCleanup(r.Context(), identity.UserID, "logout-all:"+identity.SessionID)
			return prepareErr
		})
		if err != nil {
			rollback()
			return nil, err
		}
		if commit != nil {
			commit(sessionIDs...)
		}
		return sessionIDs, nil
	}()
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
	s.auditSelf(r, identity.UserID, "logout_all", map[string]any{"session_id": identity.SessionID, "revoked": len(sessionIDs)})
	for _, sessionID := range sessionIDs {
		s.closeAccountConnections(sessionID)
	}
	s.clearSessionCookie(w, r)
	writeAccountJSON(w, http.StatusOK, map[string]int64{"revoked": int64(len(sessionIDs))})
}

func (s *Server) serveAccountSessionList(w http.ResponseWriter, r *http.Request) {
	identity := accountIdentityFrom(r.Context())
	sessions, err := s.accounts.ListUserSessions(r.Context(), identity.UserID)
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
	views := make([]accountSessionInfoView, 0, len(sessions))
	for _, session := range sessions {
		name := session.ClientName
		if name == "" {
			name = session.DeviceName
		}
		if name == "" {
			name = "未识别会话"
		}
		origin := session.Origin
		if origin == "" {
			origin = "未知来源"
		}
		views = append(views, accountSessionInfoView{
			ID: session.ID, Name: name, Origin: origin,
			DeviceID: session.DeviceID, DeviceName: session.DeviceName,
			CreatedAt: session.CreatedAt, LastSeenAt: session.LastSeenAt, ExpiresAt: session.ExpiresAt,
			Current: session.ID == identity.SessionID,
		})
	}
	writeAccountJSON(w, http.StatusOK, map[string][]accountSessionInfoView{"sessions": views})
}

func (s *Server) serveAccountSessionRevoke(w http.ResponseWriter, r *http.Request) {
	identity := accountIdentityFrom(r.Context())
	sessionID := r.PathValue("id")
	err := func() error {
		s.accountRevocationMu.Lock()
		defer s.accountRevocationMu.Unlock()
		commit, rollback, prepareErr := s.prepareAccountCleanup(r.Context(), identity.UserID, sessionID)
		if prepareErr != nil {
			// The snapshot read failed: abort without revoking so the revoke
			// retries with the same event id once the read recovers.
			rollback()
			return prepareErr
		}
		if err := s.accounts.RevokeUserSession(r.Context(), identity.UserID, sessionID); err != nil {
			rollback()
			return err
		}
		commit(sessionID)
		return nil
	}()
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
	s.auditSelf(r, identity.UserID, "session_revoke", map[string]any{"session_id": sessionID, "current": sessionID == identity.SessionID})
	s.closeAccountConnections(sessionID)
	if sessionID == identity.SessionID {
		s.clearSessionCookie(w, r)
	}
	writeAccountJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) cleanupAccount(ctx context.Context, ownerID, cleanupID string, sessionIDs ...string) {
	s.accountRevocationMu.Lock()
	defer s.accountRevocationMu.Unlock()
	s.cleanupAccountLocked(ctx, ownerID, cleanupID, sessionIDs...)
}

func (s *Server) cleanupAccountLocked(ctx context.Context, ownerID, cleanupID string, sessionIDs ...string) {
	if s.accountCleanup == nil || ownerID == "" || cleanupID == "" {
		return
	}
	// The deferred (natural-expiry) path records a snapshot read failure as an
	// error instead of treating the cleanup as done; the event stays absent so
	// the next revalidation of the same session retries it.
	if err := s.accountCleanup(context.WithoutCancel(ctx), ownerID, cleanupID, sessionIDs...); err != nil {
		s.logger.Error("account cleanup failed; same-event retry remains available", "owner", ownerID, "event", cleanupID, "error", err)
	}
}

func (s *Server) prepareAccountCleanup(ctx context.Context, ownerID, cleanupID string) (func(...string), func(), error) {
	if s.accountCleanupPrepare == nil {
		return func(...string) {}, func() {}, nil
	}
	return s.accountCleanupPrepare(context.WithoutCancel(ctx), ownerID, cleanupID)
}

// closeUserConnections ends the live connections of a user's login sessions other than exceptSession, for
// revocations that do not enumerate session ids. Only services that implement CloseAccountUser take part.
func (s *Server) closeUserConnections(userID, exceptSession string) {
	s.sockets.closeUser(userID, exceptSession)
	if closer, ok := s.accountConnections.(interface{ CloseAccountUser(string, string) }); ok {
		closer.CloseAccountUser(userID, exceptSession)
	}
}

// closeDeviceConnections ends the live connections of a user's login sessions bound to a revoked login device.
// Only services that implement CloseAccountDevice take part; the others rely on their periodic session recheck.
func (s *Server) closeDeviceConnections(userID, deviceID string) {
	s.sockets.closeDevice(userID, deviceID)
	if closer, ok := s.accountConnections.(interface{ CloseAccountDevice(string, string) }); ok {
		closer.CloseAccountDevice(userID, deviceID)
	}
}

func (s *Server) closeAccountConnections(sessionID string) {
	s.sockets.closeSession(sessionID)
	if s.accountConnections == nil {
		return
	}
	if err := s.accountConnections.CloseAccountSession(sessionID); err != nil {
		s.logger.Warn("close revoked account database connections", "error", err)
	}
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
	// Serialize with logins and envelope uploads: the password change revokes
	// sessions and bumps the account epoch as one atomic generation change.
	audits := s.newAuditQueue(r)
	defer audits.flush()
	s.accountRevocationMu.Lock()
	defer s.accountRevocationMu.Unlock()
	err := s.accounts.ChangePassword(r.Context(), identity.UserID, request.OldPassword, request.NewPassword, request.toEnvelopes(), identity.SessionID)
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
	s.unlockMFA(r, identity.UserID, "password_change", "")
	audits.self(identity.UserID, "password_change", map[string]any{"session_id": identity.SessionID})
	s.closeUserConnections(identity.UserID, identity.SessionID)
	writeAccountJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type accountDEKView struct {
	DEKEnvelope      []byte `json:"dek_envelope"`
	KDFSalt          []byte `json:"kdf_salt"`
	KDFParams        string `json:"kdf_params"`
	RecoveryEnvelope []byte `json:"recovery_envelope"`
	RecoveryHash     string `json:"recovery_hash"`
}

// serveAccountDEKGet returns only encrypted envelopes; the plaintext DEK never leaves the server (the server does not hold it).
// The epoch re-check fails closed when a revocation or reset committed while the request was in flight.
func (s *Server) serveAccountDEKGet(w http.ResponseWriter, r *http.Request) {
	identity := accountIdentityFrom(r.Context())
	epoch := s.accounts.Epoch(identity.UserID)
	envelopes, err := s.accounts.GetUserDEKEnvelopes(r.Context(), identity.UserID)
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
	s.accountRevocationMu.Lock()
	defer s.accountRevocationMu.Unlock()
	if !s.accounts.CheckEpoch(identity.UserID, epoch) {
		writeAccountError(w, http.StatusForbidden, ipc.NewError(ipc.CodeForbidden, sessionReloginMessage))
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

// serveAccountDEKUpload lets users created by an admin upload their initial envelope after first login; overwriting an existing one is refused.
// The epoch re-check under the revocation lock stops an in-flight upload from resurrecting
// envelope material after a concurrent reset or revocation deleted it.
func (s *Server) serveAccountDEKUpload(w http.ResponseWriter, r *http.Request) {
	identity := accountIdentityFrom(r.Context())
	epoch := s.accounts.Epoch(identity.UserID)
	var request dekEnvelopeRequest
	if err := decodeAccountJSON(w, r, &request); err != nil {
		writeAccountFailure(w, err)
		return
	}
	s.accountRevocationMu.Lock()
	defer s.accountRevocationMu.Unlock()
	if !s.accounts.CheckEpoch(identity.UserID, epoch) {
		writeAccountError(w, http.StatusForbidden, ipc.NewError(ipc.CodeForbidden, sessionReloginMessage))
		return
	}
	err := s.accounts.InsertUserDEKEnvelopes(r.Context(), identity.UserID, request.toEnvelopes())
	if err != nil {
		if errors.Is(err, account.ErrDEKEnvelopesExist) {
			writeAccountError(w, http.StatusConflict, ipc.NewError(ipc.CodeBadParam, "数据密钥已存在，无需重复上传"))
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

func (s *Server) serveAccountDeviceRevoke(w http.ResponseWriter, r *http.Request) {
	identity := accountIdentityFrom(r.Context())
	// Serialize with logins and envelope uploads: the device revoke bumps the
	// account epoch as part of the same generation change.
	s.accountRevocationMu.Lock()
	defer s.accountRevocationMu.Unlock()
	if err := s.accounts.RevokeDevice(r.Context(), identity.UserID, r.PathValue("id")); err != nil {
		writeAccountFailure(w, err)
		return
	}
	s.closeDeviceConnections(identity.UserID, r.PathValue("id"))
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
	fields := []string{"username", "password"}
	if request.DisplayName != "" {
		fields = append(fields, "display_name")
	}
	noteAdminAudit(r, user.ID, fields...)
	writeAccountJSON(w, http.StatusOK, map[string]accountUserView{"user": newAccountUserView(user)})
}

func (s *Server) serveAdminUserDisable(w http.ResponseWriter, r *http.Request) {
	identity := accountIdentityFrom(r.Context())
	target := r.PathValue("id")
	if target == identity.UserID {
		writeAccountError(w, http.StatusBadRequest, ipc.NewError(ipc.CodeBadParam, "不能禁用当前登录账号"))
		return
	}
	// Serialize with logins and envelope uploads: the disable revokes sessions
	// and bumps the account epoch as one atomic generation change.
	s.accountRevocationMu.Lock()
	defer s.accountRevocationMu.Unlock()
	if err := s.accounts.SetUserDisabled(r.Context(), target, true); err != nil {
		writeAccountFailure(w, err)
		return
	}
	noteAdminAudit(r, target, "state")
	s.closeUserConnections(target, "")
	writeAccountJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) serveAdminUserReset(w http.ResponseWriter, r *http.Request) {
	// Serialize with logins and envelope uploads: the admin reset deletes key
	// material and bumps the account epoch as one atomic generation change.
	s.accountRevocationMu.Lock()
	defer s.accountRevocationMu.Unlock()
	if err := s.accounts.AdminResetUser(r.Context(), r.PathValue("id")); err != nil {
		writeAccountFailure(w, err)
		return
	}
	adminID := ""
	if identity := accountIdentityFrom(r.Context()); identity != nil {
		adminID = identity.UserID
	}
	s.unlockMFA(r, r.PathValue("id"), "admin_reset", adminID)
	noteAdminAudit(r, r.PathValue("id"), "state", "must_change_password", "data_key", "totp", "sessions")
	s.closeUserConnections(r.PathValue("id"), "")
	writeAccountJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type accountAdminSettingsView struct {
	RegistrationOpen bool `json:"registration_open"`
	MFARequired      bool `json:"mfa_required"`
}

// accountAdminSettingsPutRequest uses pointers to distinguish "not carried" (keep unchanged) from "empty string" (clear).
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
	// Only the field names whose value changes are recorded; a failed read
	// counts the submitted field as changed.
	if current, err := s.accounts.RegistrationEnabled(r.Context()); err != nil || current != request.RegistrationOpen {
		noteAdminAudit(r, "", "registration_open")
	}
	if request.MFARequired != nil {
		if current, err := s.accounts.MFARequired(r.Context()); err != nil || current != *request.MFARequired {
			noteAdminAudit(r, "", "mfa_required")
		}
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
