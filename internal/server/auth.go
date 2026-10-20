package server

import (
	"context"
	"net/http"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	syncservice "github.com/Hello-CTF/NexTerm/internal/sync"
)

// mfaEnrollLockedMessage is the uniform rejection text for mfa_required locked sessions.
const mfaEnrollLockedMessage = "管理员已要求启用两步验证：完成 TOTP 绑定前，该账号只能使用绑定相关功能"

// mfaEnrollmentLockedFor decides whether the given session identity is locked by the mfa_required policy (unbound).
// reset_required sessions are constrained separately by the password-change flow; the two do not stack (change password first, then bind).
// Gateway admission without a session cookie has no account identity; the policy applies only when an identity exists.
func (s *Server) mfaEnrollmentLockedFor(r *http.Request, identity *account.Identity) (bool, error) {
	if identity == nil || identity.MFAEnabled || identity.State == account.StateResetRequired {
		return false, nil
	}
	required, err := s.accounts.MFARequired(r.Context())
	if err != nil {
		return false, err
	}
	return required, nil
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	if !s.authRequired {
		if s.options.Auth != AuthOff && s.options.Auth != AuthLoopback {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(agent.WithOpenAccess(r.Context())))
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		gatewayAuthorized := syncservice.GatewayAuthorized(r, s.gatewayAuthKey)
		if s.options.Auth == AuthPlatform && !gatewayAuthorized {
			writeRPCError(w, http.StatusUnauthorized, ipc.NewError(ipc.CodeForbidden, sessionReloginMessage))
			return
		}
		// A gateway-only caller stays anonymous; owner-scoped stores must fail closed rather than inherit shared trust.
		if gatewayAuthorized {
			if _, hasSessionCookie := sessionCookie(r); !hasSessionCookie {
				next.ServeHTTP(w, r)
				return
			}
		}
		if s.accounts != nil {
			if cookieToken, ok := sessionCookie(r); ok {
				identity, err := s.accounts.ValidateSession(r.Context(), cookieToken)
				if err == nil {
					if identity.State == account.StateResetRequired {
						writeRPCError(w, http.StatusForbidden, ipc.NewError(ipc.CodeForbidden, "必须先完成密码重置"))
						return
					}
					locked, err := s.mfaEnrollmentLockedFor(r, identity)
					if err != nil {
						writeRPCError(w, http.StatusInternalServerError, ipc.NormalizeError(err))
						return
					}
					if locked {
						writeRPCError(w, http.StatusForbidden, ipc.NewError(ipc.CodeMFAEnrollmentRequired, mfaEnrollLockedMessage))
						return
					}
					if unsafeAccountMethod(r) && !accountCSRFSafeEqual(identity.SessionID, r.Header.Get(csrfHeaderName)) {
						writeRPCError(w, http.StatusForbidden, ipc.NewError(ipc.CodeForbidden, csrfRefreshMessage))
						return
					}
					ctx := withAccountIdentity(r.Context(), identity)
					// Per-account-user isolated device-side state such as sync opt-in reads the session identity via ipc.UserIDFromContext; client-reported values are not accepted.
					ctx = ipc.WithUserID(ctx, identity.UserID)
					ctx = ipc.WithRole(ctx, string(identity.Role))
					ctx = ipc.WithAccountSessionID(ctx, identity.SessionID)
					ctx = ipc.WithAccountSessionCheck(ctx, func(checkCtx context.Context) error {
						return s.validateAccountSession(checkCtx, cookieToken, identity, func() {
							s.cleanupAccount(checkCtx, identity.UserID, identity.SessionID)
						})
					})
					admit := s.accountExecutionAdmission
					if admit == nil {
						admit = s.admitAccountExecution
					}
					ctx = execbatch.WithAdmission(ctx, func(admitCtx context.Context, fn func() error) error {
						return admit(admitCtx, cookieToken, identity, fn)
					})
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}
		}
		writeRPCError(w, http.StatusUnauthorized, ipc.NewError(ipc.CodeForbidden, sessionReloginMessage))
	})
}

func (s *Server) validateAccountSession(ctx context.Context, cookieToken string, identity *account.Identity, cleanup func()) error {
	validationCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	current, err := s.accounts.ValidateSession(validationCtx, cookieToken)
	if err != nil {
		if confirmedAccountSessionInvalid(err) {
			cleanup()
		}
		return err
	}
	if current.SessionID != identity.SessionID || current.UserID != identity.UserID {
		cleanup()
		return ipc.NewError(ipc.CodeForbidden, sessionReloginMessage)
	}
	return nil
}

func (s *Server) admitAccountExecution(ctx context.Context, cookieToken string, identity *account.Identity, fn func() error) error {
	s.accountRevocationMu.Lock()
	defer s.accountRevocationMu.Unlock()
	if err := s.validateAccountSession(ctx, cookieToken, identity, func() {
		s.cleanupAccountLocked(ctx, identity.UserID, identity.SessionID)
	}); err != nil {
		return err
	}
	return fn()
}

func unsafeAccountMethod(r *http.Request) bool {
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// authorizeWebSocket authenticates a WS connection; the returned identity only covers the account session branch,
// and the gateway branch has no user identity (returns nil).
func (s *Server) authorizeWebSocket(r *http.Request) (*account.Identity, bool) {
	if !s.authRequired {
		return nil, true
	}
	if syncservice.GatewayAuthorized(r, s.gatewayAuthKey) {
		return nil, true
	}
	if s.accounts != nil {
		if cookieToken, ok := sessionCookie(r); ok {
			identity, err := s.accounts.ValidateSession(r.Context(), cookieToken)
			if err == nil {
				return identity, identity.State != account.StateResetRequired
			}
		}
	}
	return nil, false
}

func (s *Server) admitWebSocket(w http.ResponseWriter, r *http.Request) (*account.Identity, bool) {
	identity, authorized := s.authorizeWebSocket(r)
	if !authorized {
		http.Error(w, sessionReloginMessage, http.StatusUnauthorized)
		return nil, false
	}
	locked, err := s.mfaEnrollmentLockedFor(r, identity)
	if err != nil {
		writeAccountFailure(w, err)
		return nil, false
	}
	if locked {
		writeAccountError(w, http.StatusForbidden, ipc.NewError(ipc.CodeMFAEnrollmentRequired, mfaEnrollLockedMessage))
		return nil, false
	}
	return identity, true
}
