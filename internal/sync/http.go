package sync

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

func (s *Service) PeerHandler() http.Handler {
	dispatcher, err := s.PeerDispatcher()
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writePeerError(w, http.StatusInternalServerError, err)
		})
	}
	rpc := ipc.NewRPCHandler(dispatcher, ipc.Environment{})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path != "/sync/rpc" {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodPost {
			authorized := hasPlatformUser(r)
			if !authorized {
				valid, err := s.VerifyToken(r.Context(), r.Header.Get(TokenHeader))
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
		rpc.ServeHTTP(w, r)
	})
}

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
