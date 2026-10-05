package sync

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"net/http"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

type PeerRPCHandler struct {
	service *Service
	rpc     *ipc.RPCHandler
	err     error
}

func (s *Service) PeerHandler() *PeerRPCHandler {
	handler := &PeerRPCHandler{service: s}
	dispatcher, err := s.PeerDispatcher()
	if err != nil {
		handler.err = err
		return handler
	}
	handler.rpc = ipc.NewRPCHandler(dispatcher, ipc.Environment{})
	return handler
}

func (h *PeerRPCHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if h.err != nil {
		writePeerError(w, http.StatusInternalServerError, h.err)
		return
	}
	if r.URL.Path != "/sync/rpc" {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodPost {
		authorized := GatewayAuthorized(r, h.service.GatewayAuthKey())
		if !authorized {
			valid, err := h.service.VerifyToken(r.Context(), r.Header.Get(TokenHeader))
			if err != nil {
				writePeerError(w, http.StatusInternalServerError, err)
				return
			}
			authorized = valid
		}
		if !authorized {
			writePeerError(w, http.StatusUnauthorized, ipc.NewError(ipc.CodeForbidden, "同步令牌无效或缺失"))
			return
		}
	}
	h.rpc.ServeHTTP(w, r)
}

func (h *PeerRPCHandler) VerifyToken(ctx context.Context, token string) (bool, error) {
	return h.service.VerifyToken(ctx, token)
}

func (h *PeerRPCHandler) VerifyTokenIdentity(ctx context.Context, token string) (TokenIdentity, bool, error) {
	return h.service.VerifyTokenIdentity(ctx, token)
}

func (h *PeerRPCHandler) GatewayAuthKey() string { return h.service.GatewayAuthKey() }

func GatewayAuthorized(r *http.Request, key string) bool {
	if key == "" {
		return false
	}
	presented := r.Header.Get(GatewayAuthHeader)
	if presented == "" {
		return false
	}
	expectedDigest := sha256.Sum256([]byte(key))
	presentedDigest := sha256.Sum256([]byte(presented))
	return subtle.ConstantTimeCompare(expectedDigest[:], presentedDigest[:]) == 1
}

func writePeerError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ipc.Failure(ipc.NormalizeError(err)))
}
