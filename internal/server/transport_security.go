package server

import (
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"

	core "github.com/Hello-CTF/NexTerm/internal/app"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

var defaultAllowedOrigins = []string{"localhost:*", "127.0.0.1:*"}

func (s *Server) transportGuard(next http.Handler) http.Handler {
	hostGuard := s.options.Auth == AuthLoopback && core.LoopbackListen(s.options.Listen)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hostGuard && !loopbackHost(r.Host) {
			http.Error(w, "回环监听模式只允许通过 localhost、127.0.0.1 或 ::1 访问", http.StatusMisdirectedRequest)
			return
		}
		origin := r.Header.Get("Origin")
		if origin != "" {
			if !requestOriginAllowed(r, s.options.AllowedOrigins) {
				http.Error(w, "请求来源不在允许列表中", http.StatusForbidden)
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Add("Vary", "Origin")
		}
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			if s.options.SyncOnly && r.URL.Path != "/sync/rpc" && r.URL.Path != "/healthz" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-NexTerm-Client-Id, X-NexTerm-Sync-Token, X-NexTerm-CSRF")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		isRPCPath := r.URL.Path == "/sync/rpc" || !s.options.SyncOnly && r.URL.Path == "/rpc"
		if r.Method == http.MethodPost && isRPCPath {
			mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || mediaType != "application/json" {
				writeRPCError(w, http.StatusUnsupportedMediaType, ipc.NewError(ipc.CodeBadParam, "RPC 请求必须使用 Content-Type: application/json"))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func loopbackHost(hostport string) bool {
	host := hostport
	if parsed, _, err := net.SplitHostPort(hostport); err == nil {
		host = parsed
	} else {
		host = strings.TrimPrefix(strings.TrimSuffix(hostport, "]"), "[")
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
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
