package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

const (
	sessionCookieName = "nexterm_session"
	csrfHeaderName    = "X-NexTerm-CSRF"
	csrfPurpose       = "nexterm-csrf-v1"
	maxAccountBody    = 64 << 10

	// Session and CSRF failures uniformly give the next step (refresh or re-login); wording stays consistent across server and fleet.
	// The CSRF text must keep the "CSRF" keyword: the frontend isCsrfRejection (src/ipc/commands.ts, webFiles.ts) relies on it to detect and auto-refresh/retry.
	sessionReloginMessage = "会话无效或缺失，请重新登录"
	csrfRefreshMessage    = "CSRF 校验失败：页面已过期，请刷新后重试"
)

type accountIdentityContextKey struct{}

func withAccountIdentity(ctx context.Context, identity *account.Identity) context.Context {
	return context.WithValue(ctx, accountIdentityContextKey{}, identity)
}

func accountIdentityFrom(ctx context.Context) *account.Identity {
	identity, _ := ctx.Value(accountIdentityContextKey{}).(*account.Identity)
	return identity
}

// accountCSRFToken derives a stateless CSRF token per session: HMAC(sessionID).
// The token expires in step when the session is revoked or expires; the server needs no extra storage.
func accountCSRFToken(sessionID string) string {
	mac := hmac.New(sha256.New, []byte(sessionID))
	mac.Write([]byte(csrfPurpose))
	return hex.EncodeToString(mac.Sum(nil))
}

func accountCSRFSafeEqual(sessionID, presented string) bool {
	expected, err := hex.DecodeString(accountCSRFToken(sessionID))
	if err != nil {
		return false
	}
	provided, err := hex.DecodeString(presented)
	if err != nil || len(provided) != len(expected) {
		return false
	}
	return hmac.Equal(expected, provided)
}

func sessionCookie(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return "", false
	}
	return cookie.Value, true
}

func requestTLS(r *http.Request) bool {
	return r.TLS != nil
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	cookie := &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   requestTLS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(account.SessionAbsoluteTTL / time.Second),
	}
	http.SetCookie(w, cookie)
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   requestTLS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func loginThrottleKey(r *http.Request, username string) string {
	return "login|" + strings.ToLower(username) + "|" + clientIP(r)
}

func recoveryThrottleKey(r *http.Request, username string) string {
	return "recovery|" + strings.ToLower(username) + "|" + clientIP(r)
}

func ipThrottleKey(r *http.Request, scope string) string {
	return scope + "|" + clientIP(r)
}

// accountGuard resolves the session cookie into a server-derived identity.
// Missing or invalid cookies both pass through anonymously; session requirements are enforced by requireAccountSession,
// so an expired cookie cannot block re-login on public routes such as login.
func (s *Server) accountGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if s.accountClosed() {
			writeAccountError(w, http.StatusForbidden, ipc.NewError(ipc.CodeForbidden, "当前为共享工作区模式（未启用账号登录），账号功能不可用"))
			return
		}
		if token, ok := sessionCookie(r); ok {
			if identity, err := s.accounts.ValidateSession(r.Context(), token); err == nil {
				r = r.WithContext(withAccountIdentity(r.Context(), identity))
			}
		}
		next.ServeHTTP(w, r)
	})
}

// resetRequiredAllowedPaths lists the only routes a reset_required session may use before completing the password reset.
var resetRequiredAllowedPaths = map[string]bool{
	"/auth/me":         true,
	"/auth/password":   true,
	"/auth/dek":        true,
	"/auth/logout":     true,
	"/auth/logout-all": true,
}

func resetRequiredLocked(r *http.Request) bool {
	identity := accountIdentityFrom(r.Context())
	return identity != nil && identity.State == account.StateResetRequired && !resetRequiredAllowedPaths[r.URL.Path]
}

// mfaEnrollAllowedPaths lists the only routes an unbound session may use under the mfa_required policy before completing TOTP binding.
var mfaEnrollAllowedPaths = map[string]bool{
	"/auth/me":           true,
	"/auth/totp":         true,
	"/auth/totp/setup":   true,
	"/auth/totp/confirm": true,
	"/auth/logout":       true,
	"/auth/logout-all":   true,
}

// mfaEnrollLocked enforces the mfa_required semantics: unbound sessions get 403 on everything except the routes needed for binding.
func (s *Server) mfaEnrollLocked(r *http.Request) (bool, error) {
	if mfaEnrollAllowedPaths[r.URL.Path] {
		return false, nil
	}
	return s.mfaEnrollmentLockedFor(r, accountIdentityFrom(r.Context()))
}

// requireAccountSession requires a session identity already resolved by accountGuard.
// reset_required sessions are limited to the routes needed to complete the reset, everything else is 403;
// unbound sessions under the mfa_required policy are limited to the routes needed to complete TOTP binding, everything else is 403.
func (s *Server) requireAccountSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if accountIdentityFrom(r.Context()) == nil {
			writeAccountError(w, http.StatusUnauthorized, ipc.NewError(ipc.CodeForbidden, sessionReloginMessage))
			return
		}
		if resetRequiredLocked(r) {
			writeAccountError(w, http.StatusForbidden, ipc.NewError(ipc.CodeForbidden, "必须先完成密码重置"))
			return
		}
		locked, err := s.mfaEnrollLocked(r)
		if err != nil {
			writeAccountFailure(w, err)
			return
		}
		if locked {
			writeAccountError(w, http.StatusForbidden, ipc.NewError(ipc.CodeMFAEnrollmentRequired, mfaEnrollLockedMessage))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireSuperadmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity := accountIdentityFrom(r.Context())
		if identity == nil || identity.Role != account.RoleSuperadmin {
			writeAccountError(w, http.StatusForbidden, ipc.NewError(ipc.CodeForbidden, "需要超级管理员权限"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireAccountCSRF verifies that state-changing requests of a cookie session carry the CSRF token bound to the session.
func (s *Server) requireAccountCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity := accountIdentityFrom(r.Context())
		if identity == nil {
			writeAccountError(w, http.StatusUnauthorized, ipc.NewError(ipc.CodeForbidden, sessionReloginMessage))
			return
		}
		if !accountCSRFSafeEqual(identity.SessionID, r.Header.Get(csrfHeaderName)) {
			s.logger.Warn("account csrf rejected", "path", r.URL.Path, "remote", clientIP(r))
			writeAccountError(w, http.StatusForbidden, ipc.NewError(ipc.CodeForbidden, csrfRefreshMessage))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) accountClosed() bool {
	return s.options.Auth == AuthOff
}

// ValidateAuthOffBounds enforces the --auth=off hard boundary: startup is refused as long as any user account exists.
// off is only allowed to map to a local shared workspace; it must not run implicitly degraded on a database with an account system.
func ValidateAuthOffBounds(ctx context.Context, accounts *account.Accounts) error {
	count, err := accounts.CountUsers(ctx)
	if err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("--auth=off 拒绝启动：数据库已存在 %d 个用户账号；请改用 --auth=on 或清理账号数据", count)
	}
	return nil
}
