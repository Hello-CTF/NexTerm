package server

import (
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

var defaultAllowedOrigins = []string{"localhost:*", "127.0.0.1:*"}

func (s *Server) transportGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			if !requestOriginAllowed(r, s.options.AllowedOrigins) {
				http.Error(w, "origin is not allowed", http.StatusForbidden)
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Add("Vary", "Origin")
		}
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			if s.options.SyncOnly && r.URL.Path != "/sync/rpc" && r.URL.Path != "/healthz" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-NexTerm-Client-Id, X-NexTerm-Sync-Token")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		isRPCPath := r.URL.Path == "/sync/rpc" || !s.options.SyncOnly && r.URL.Path == "/rpc"
		if r.Method == http.MethodPost && isRPCPath {
			mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || mediaType != "application/json" {
				writeRPCError(w, http.StatusUnsupportedMediaType, ipc.NewError(ipc.CodeBadParam, "RPC requires Content-Type application/json"))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func requestOriginAllowed(r *http.Request, patterns []string) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	if strings.EqualFold(parsed.Host, r.Host) {
		return true
	}
	for _, pattern := range patterns {
		target := parsed.Host
		if strings.Contains(pattern, "://") {
			target = parsed.Scheme + "://" + target
		}
		matched, err := path.Match(strings.ToLower(pattern), strings.ToLower(target))
		if err == nil && matched {
			return true
		}
	}
	return false
}
