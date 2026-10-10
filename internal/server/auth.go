package server

import (
	"net/http"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	syncservice "github.com/Hello-CTF/NexTerm/internal/sync"
)

const (
	wsAuthProtocol       = "nexterm"
	wsAuthHeaderMaxBytes = 256
)

// 过渡说明: 遗留静态令牌分支仅为内部过渡保留, 供 M125(账号同步前端)删除。
//   - requireAuth / authorizeWebSocket 中的 TokenHeader 静态令牌分支
//   - ws 的 "nexterm,<token>" 子协议认证(webSocketAuthToken)
// 删除条件: 会话 cookie(/auth/*)成为浏览器唯一入口。v2 同步协议只认会话, 不再产生静态令牌。

// mfaEnrollLockedMessage 是 mfa_required 锁定会话的统一拒绝文案。
const mfaEnrollLockedMessage = "管理员已要求启用两步验证: 完成 TOTP 绑定前,该账号只能使用绑定相关功能"

// mfaEnrollmentLockedFor 判定指定会话身份是否被 mfa_required 策略锁定(未绑定)。
// reset_required 会话由改密流程单独约束, 两者不叠加(先改密再绑定)。
// 网关与遗留静态令牌分支无账号会话身份, 不适用该策略。
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
		if token := r.Header.Get(TokenHeader); token != "" && s.tokens != nil {
			valid, err := s.tokens.VerifyToken(r.Context(), token)
			if err != nil {
				writeRPCError(w, http.StatusInternalServerError, ipc.NormalizeError(err))
				return
			}
			if valid {
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
// 网关/遗留静态令牌分支无用户身份(返回 nil)。
func (s *Server) authorizeWebSocket(r *http.Request) (*account.Identity, bool, error) {
	if !s.authRequired {
		return nil, true, nil
	}
	if syncservice.GatewayAuthorized(r, s.gatewayAuthKey) {
		return nil, true, nil
	}
	if token := r.Header.Get(TokenHeader); token != "" && s.tokens != nil {
		valid, err := s.tokens.VerifyToken(r.Context(), token)
		return nil, valid, err
	}
	if token, ok := webSocketAuthToken(r); ok && s.tokens != nil {
		valid, err := s.tokens.VerifyToken(r.Context(), token)
		return nil, valid, err
	}
	if s.accounts != nil {
		if cookieToken, ok := sessionCookie(r); ok {
			identity, err := s.accounts.ValidateSession(r.Context(), cookieToken)
			if err == nil {
				return identity, identity.State != account.StateResetRequired, nil
			}
		}
	}
	return nil, false, nil
}

func webSocketAuthToken(r *http.Request) (string, bool) {
	raw := r.Header.Get("Sec-WebSocket-Protocol")
	if raw == "" || len(raw) > wsAuthHeaderMaxBytes {
		return "", false
	}
	tokens := webSocketProtocolTokens(raw)
	if len(tokens) != 2 || !strings.EqualFold(tokens[0], wsAuthProtocol) {
		return "", false
	}
	return tokens[1], true
}

func (s *Server) admitWebSocket(w http.ResponseWriter, r *http.Request) (*account.Identity, bool) {
	identity, authorized, err := s.authorizeWebSocket(r)
	if err != nil {
		http.Error(w, "令牌校验器不可用，请稍后重试", http.StatusInternalServerError)
		return nil, false
	}
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

func webSocketProtocolTokens(raw string) []string {
	parts := strings.Split(raw, ",")
	tokens := make([]string, 0, len(parts))
	for _, part := range parts {
		if token := strings.TrimSpace(part); token != "" {
			tokens = append(tokens, token)
		}
	}
	return tokens
}
