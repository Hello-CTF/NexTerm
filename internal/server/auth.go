package server

import (
	"net/http"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	syncservice "github.com/ProbiusOfficial/NexTerm/internal/sync"
)

const wsAuthProtocol = "nexterm"

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
		token := r.Header.Get(TokenHeader)
		if token != "" {
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
		writeRPCError(w, http.StatusUnauthorized, ipc.NewError(ipc.CodeForbidden, "访问令牌无效或缺失"))
	})
}

func (s *Server) authorizeWebSocket(r *http.Request) (bool, error) {
	if !s.authRequired {
		return true, nil
	}
	if syncservice.GatewayAuthorized(r, s.gatewayAuthKey) {
		return true, nil
	}
	if token := r.Header.Get(TokenHeader); token != "" {
		valid, err := s.tokens.VerifyToken(r.Context(), token)
		if err != nil {
			return false, err
		}
		if valid {
			return true, nil
		}
	}
	for _, candidate := range webSocketProtocolTokens(r) {
		if candidate == wsAuthProtocol {
			continue
		}
		valid, err := s.tokens.VerifyToken(r.Context(), candidate)
		if err != nil {
			return false, err
		}
		if valid {
			return true, nil
		}
	}
	return false, nil
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

func webSocketProtocolTokens(r *http.Request) []string {
	raw := r.Header.Get("Sec-WebSocket-Protocol")
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	tokens := make([]string, 0, len(parts))
	for _, part := range parts {
		if token := strings.TrimSpace(part); token != "" {
			tokens = append(tokens, token)
		}
	}
	return tokens
}
