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

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

const (
	sessionCookieName = "nexterm_session"
	csrfHeaderName    = "X-NexTerm-CSRF"
	csrfPurpose       = "nexterm-csrf-v1"
	maxAccountBody    = 64 << 10

	// 会话与 CSRF 失败统一给出刷新或重新登录的下一步, server 与 fleet 两侧措辞保持一致。
	// CSRF 文案必须保留 "CSRF" 关键字: 前端 isCsrfRejection (src/ipc/commands.ts, webFiles.ts) 靠它识别并自动刷新重试。
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

// accountCSRFToken 为每个会话派生无状态 CSRF 令牌: HMAC(sessionID)。
// 会话被吊销或过期后令牌同步失效; 服务端无需额外存储。
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

// accountGuard 将会话 cookie 解析为服务端派生身份。
// cookie 缺失或无效都匿名放行; 会话要求由 requireAccountSession 把关,
// 避免过期 cookie 阻断 login 等公共路由的重新登录。
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

// resetRequiredAllowedPaths 列出 reset_required 会话在完成密码重置前唯一可用的路由。
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

// mfaEnrollAllowedPaths 列出 mfa_required 策略下未绑定会话在完成 TOTP 绑定前唯一可用的路由。
var mfaEnrollAllowedPaths = map[string]bool{
	"/auth/me":           true,
	"/auth/totp":         true,
	"/auth/totp/setup":   true,
	"/auth/totp/confirm": true,
	"/auth/logout":       true,
	"/auth/logout-all":   true,
}

// mfaEnrollLocked 落实 mfa_required 强制语义: 未绑定会话除绑定所需路由外一律 403。
func (s *Server) mfaEnrollLocked(r *http.Request) bool {
	return !mfaEnrollAllowedPaths[r.URL.Path] && s.mfaEnrollmentLockedFor(r, accountIdentityFrom(r.Context()))
}

// requireAccountSession 要求已通过 accountGuard 解析出会话身份。
// reset_required 会话仅限完成重置所需路由, 其余一律 403;
// mfa_required 策略下的未绑定会话仅限完成 TOTP 绑定所需路由, 其余一律 403。
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
		if s.mfaEnrollLocked(r) {
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
			writeAccountError(w, http.StatusForbidden, ipc.NewError(ipc.CodeForbidden, "需要超管权限"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireAccountCSRF 校验 cookie 会话的状态变更请求携带与会话绑定的 CSRF 令牌。
func (s *Server) requireAccountCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity := accountIdentityFrom(r.Context())
		if identity == nil {
			writeAccountError(w, http.StatusUnauthorized, ipc.NewError(ipc.CodeForbidden, sessionReloginMessage))
			return
		}
		if !accountCSRFSafeEqual(identity.SessionID, r.Header.Get(csrfHeaderName)) {
			writeAccountError(w, http.StatusForbidden, ipc.NewError(ipc.CodeForbidden, csrfRefreshMessage))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) accountClosed() bool {
	return s.options.Auth == AuthOff
}

// ValidateAuthOffBounds 落实 --auth=off 硬边界: 只要存在任何用户账号就拒绝启动。
// off 只允许映射到本地共享工作区, 不得在有账号体系的库上隐式降级运行。
func ValidateAuthOffBounds(ctx context.Context, accounts *account.Accounts) error {
	count, err := accounts.CountUsers(ctx)
	if err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("--auth=off 拒绝启动: 数据库已存在 %d 个用户账号; 请改用 --auth=on 或清理账号数据", count)
	}
	return nil
}
