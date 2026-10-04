package sync

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

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
		authorized := h.service.PlatformTrusted() && hasPlatformUser(r)
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

func (h *PeerRPCHandler) PlatformTrusted() bool { return h.service.PlatformTrusted() }

func hasPlatformUser(r *http.Request) bool {
	for name := range r.Header {
		if strings.EqualFold(name, PlatformUserHeader) {
			return true
		}
	}
	return false
}

func writePeerError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ipc.Failure(ipc.NormalizeError(err)))
}
