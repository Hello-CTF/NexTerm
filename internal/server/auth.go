package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/account"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	syncservice "github.com/ProbiusOfficial/NexTerm/internal/sync"
)

const (
	wsAuthProtocol       = "nexterm"
	wsAuthHeaderMaxBytes = 256
)

// TokenIdentityVerifier 在令牌验证通过时返回令牌身份，
// 供命令层区分管理员令牌与普通客户端令牌。
type TokenIdentityVerifier interface {
	VerifyTokenIdentity(ctx context.Context, presented string) (syncservice.TokenIdentity, bool, error)
}

// 过渡说明: 令牌时代路径仅为内部过渡保留, 供后续切片删除。
// M117(全量 E2E 同步协议)与 M125(账号同步前端)落地后删除:
//   - requireAuth / authorizeWebSocket / authenticatedSync 中的 TokenHeader 静态令牌分支
//   - ws 的 "nexterm,<token>" 子协议认证(webSocketAuthToken)
//   - token / rotate-token CLI 命令与 core.TokenStore
//   - /rpc 上的 sync.token.* 管理命令(syncservice.CommandToken 系列)及 TokenIdentityVerifier
// 删除条件: 会话 cookie(/auth/*)成为浏览器唯一入口, 且同步协议不再接受静态令牌。

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
			identity, valid, err := s.verifyTokenIdentity(r.Context(), token)
			if err != nil {
				writeRPCError(w, http.StatusInternalServerError, ipc.NormalizeError(err))
				return
			}
			if valid {
				next.ServeHTTP(w, r.WithContext(syncservice.WithTokenIdentity(r.Context(), identity)))
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
					if unsafeAccountMethod(r) && !accountCSRFSafeEqual(identity.SessionID, r.Header.Get(csrfHeaderName)) {
						writeRPCError(w, http.StatusForbidden, ipc.NewError(ipc.CodeForbidden, "CSRF 校验失败"))
						return
					}
					next.ServeHTTP(w, r.WithContext(withAccountIdentity(r.Context(), identity)))
					return
				}
			}
		}
		writeRPCError(w, http.StatusUnauthorized, ipc.NewError(ipc.CodeForbidden, "访问令牌无效或缺失"))
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

func (s *Server) verifyTokenIdentity(ctx context.Context, token string) (syncservice.TokenIdentity, bool, error) {
	if verifier, ok := s.tokens.(TokenIdentityVerifier); ok {
		return verifier.VerifyTokenIdentity(ctx, token)
	}
	valid, err := s.tokens.VerifyToken(ctx, token)
	return syncservice.TokenIdentity{}, valid, err
}

func (s *Server) authorizeWebSocket(r *http.Request) (bool, error) {
	if !s.authRequired {
		return true, nil
	}
	if syncservice.GatewayAuthorized(r, s.gatewayAuthKey) {
		return true, nil
	}
	if token := r.Header.Get(TokenHeader); token != "" && s.tokens != nil {
		return s.tokens.VerifyToken(r.Context(), token)
	}
	if token, ok := webSocketAuthToken(r); ok && s.tokens != nil {
		return s.tokens.VerifyToken(r.Context(), token)
	}
	if s.accounts != nil {
		if cookieToken, ok := sessionCookie(r); ok {
			identity, err := s.accounts.ValidateSession(r.Context(), cookieToken)
			if err == nil {
				return identity.State != account.StateResetRequired, nil
			}
		}
	}
	return false, nil
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

func (s *Server) admitWebSocket(w http.ResponseWriter, r *http.Request) bool {
	authorized, err := s.authorizeWebSocket(r)
	if err != nil {
		http.Error(w, "token verifier unavailable", http.StatusInternalServerError)
		return false
	}
	if !authorized {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
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
