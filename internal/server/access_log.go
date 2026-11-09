package server

import (
	"bufio"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// EnvAccessLog enables one Info record per HTTP request when set to a true value.
const EnvAccessLog = "NEXTERM_ACCESS_LOG"

func ParseAccessLogEnv(getenv func(string) string) (bool, error) {
	raw := strings.TrimSpace(getenv(EnvAccessLog))
	if raw == "" {
		return false, nil
	}
	enabled, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("invalid %s value %q (want 1 or 0)", EnvAccessLog, raw)
	}
	return enabled, nil
}

// tokenPathPrefixes are routes whose next path segment is a bearer credential.
var tokenPathPrefixes = []string{"/share/preview/", "/share/public/"}

// accessLogPath drops the query string and masks credential path segments; request bodies are never read.
func accessLogPath(path string) string {
	for _, prefix := range tokenPathPrefixes {
		if rest, ok := strings.CutPrefix(path, prefix); ok && rest != "" {
			if _, tail, found := strings.Cut(rest, "/"); found {
				return prefix + "***/" + tail
			}
			return prefix + "***"
		}
	}
	return path
}

// withAccessLog logs method, path, status, duration and client address once the handler returns. For an
// upgraded (hijacked) connection the record is written when the connection ends and duration covers its lifetime.
func withAccessLog(logger *slog.Logger, clientIP func(*http.Request) string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &accessLogRecorder{ResponseWriter: w}
		defer func() {
			status := recorder.status
			if status == 0 {
				status = http.StatusOK
			}
			logger.LogAttrs(r.Context(), slog.LevelInfo, "http request",
				slog.String("method", r.Method),
				slog.String("path", accessLogPath(r.URL.Path)),
				slog.Int("status", status),
				slog.Int64("bytes", recorder.bytes),
				slog.Duration("duration", time.Since(start)),
				slog.String("client_ip", clientIP(r)),
				slog.Bool("upgraded", recorder.hijacked),
			)
		}()
		next.ServeHTTP(recorder, r)
	})
}

// accessLogRecorder keeps http.Flusher, http.Hijacker and http.ResponseController working through Unwrap.
type accessLogRecorder struct {
	http.ResponseWriter
	status   int
	bytes    int64
	hijacked bool
}

func (w *accessLogRecorder) WriteHeader(status int) {
	if w.status == 0 && (status >= 200 || status == http.StatusSwitchingProtocols) {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *accessLogRecorder) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(data)
	w.bytes += int64(n)
	return n, err
}

func (w *accessLogRecorder) Flush() {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *accessLogRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err == nil {
		w.hijacked = true
		if w.status == 0 {
			w.status = http.StatusSwitchingProtocols
		}
	}
	return conn, rw, err
}

func (w *accessLogRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }
