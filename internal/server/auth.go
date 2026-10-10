package server

import (
	"net/http"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	syncservice "github.com/Hello-CTF/NexTerm/internal/sync"
)

// mfaEnrollLockedMessage 是 mfa_required 锁定会话的统一拒绝文案。
const mfaEnrollLockedMessage = "管理员已要求启用两步验证: 完成 TOTP 绑定前,该账号只能使用绑定相关功能"

// mfaEnrollmentLockedFor 判定指定会话身份是否被 mfa_required 策略锁定(未绑定)。
// reset_required 会话由改密流程单独约束, 两者不叠加(先改密再绑定)。
// 网关分支无账号会话身份, 不适用该策略。
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
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if syncservice.GatewayAuthorized(r, s.gatewayAuthKey) {
			next.ServeHTTP(w, r)
			return
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
					// 同步 opt-in 等按账号用户隔离的设备端状态经 ipc.UserIDFromContext 读取会话身份, 不接受客户端自报
					ctx = ipc.WithUserID(ctx, identity.UserID)
					ctx = ipc.WithRole(ctx, string(identity.Role))
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}
		}
		writeRPCError(w, http.StatusUnauthorized, ipc.NewError(ipc.CodeForbidden, sessionReloginMessage))
	})
}

func unsafeAccountMethod(r *http.Request) bool {
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// authorizeWebSocket 校验 WS 连接; 返回的 identity 仅覆盖账号会话分支,
// 网关分支无用户身份(返回 nil)。
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
