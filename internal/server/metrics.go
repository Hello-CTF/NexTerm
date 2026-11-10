package server

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/metrics"
)

// Login steps and results labelling nexterm_auth_login_total. Both sets are
// closed, so the counter never carries user-controlled values.
const (
	loginStepPassword = "password"
	loginStepTOTP     = "totp"

	loginSuccess     = "success"
	loginFailure     = "failure"
	loginThrottled   = "throttled"
	loginLocked      = "locked"
	loginMFARequired = "mfa_required"
	loginError       = "error"
)

var loginSeries = []struct{ step, result string }{
	{loginStepPassword, loginSuccess},
	{loginStepPassword, loginFailure},
	{loginStepPassword, loginThrottled},
	{loginStepPassword, loginMFARequired},
	{loginStepPassword, loginError},
	{loginStepTOTP, loginSuccess},
	{loginStepTOTP, loginFailure},
	{loginStepTOTP, loginThrottled},
	{loginStepTOTP, loginLocked},
	{loginStepTOTP, loginError},
}

type loginKey struct{ step, result string }

type serverMetrics struct {
	logins   map[loginKey]*atomic.Uint64
	requests sync.Map // httpRequestKey -> *atomic.Uint64
	duration sync.Map // httpRouteKey -> *metrics.Histogram
}

type httpRouteKey struct{ method, route string }

type httpRequestKey struct {
	httpRouteKey
	class string
}

func newServerMetrics() *serverMetrics {
	m := &serverMetrics{logins: make(map[loginKey]*atomic.Uint64, len(loginSeries))}
	for _, series := range loginSeries {
		m.logins[loginKey{series.step, series.result}] = &atomic.Uint64{}
	}
	return m
}

func (m *serverMetrics) login(step, result string) {
	if m == nil {
		return
	}
	if counter := m.logins[loginKey{step, result}]; counter != nil {
		counter.Add(1)
	}
}

// passwordLoginResult classifies an Authenticate error: a rejection of the
// credentials or account is a failure, anything else a server-side error.
func passwordLoginResult(err error) string {
	var appErr *ipc.Error
	if errors.As(err, &appErr) && appErr.Code == ipc.CodeForbidden {
		return loginFailure
	}
	return loginError
}

func (m *serverMetrics) observeRequest(method, route string, status int, elapsed time.Duration, upgraded bool) {
	key := httpRequestKey{httpRouteKey{method, route}, statusClass(status)}
	counter, ok := m.requests.Load(key)
	if !ok {
		counter, _ = m.requests.LoadOrStore(key, &atomic.Uint64{})
	}
	counter.(*atomic.Uint64).Add(1)
	// A WebSocket's lifetime is not a request latency.
	if upgraded {
		return
	}
	histogram, ok := m.duration.Load(key.httpRouteKey)
	if !ok {
		histogram, _ = m.duration.LoadOrStore(key.httpRouteKey, metrics.NewHistogram(metrics.DefaultDurationBuckets))
	}
	histogram.(*metrics.Histogram).Observe(elapsed.Seconds())
}

func statusClass(status int) string {
	if status < 100 || status > 599 {
		return "other"
	}
	return strconv.Itoa(status/100) + "xx"
}

func metricMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return method
	}
	return "OTHER"
}

// routeTemplate turns a ServeMux pattern into the route label: the pattern
// text without its method, so path wildcards stay templates such as
// /ws/channel/{id}. Requests no pattern matched share one label.
func routeTemplate(pattern string) string {
	if pattern == "" {
		return "unmatched"
	}
	if _, path, ok := strings.Cut(pattern, " "); ok {
		pattern = strings.TrimSpace(path)
	}
	return pattern
}

type routeCaptureKey struct{}

// captureRoute records the pattern the mux matched; ServeMux sets it on the
// request it receives, which the outer counter cannot see after wrappers
// clone the request.
func captureRoute(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		if holder, ok := r.Context().Value(routeCaptureKey{}).(*string); ok {
			*holder = r.Pattern
		}
	})
}

func (m *serverMetrics) countRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		pattern := new(string)
		recorder := &metricsResponseWriter{ResponseWriter: w}
		next.ServeHTTP(recorder, r.WithContext(context.WithValue(r.Context(), routeCaptureKey{}, pattern)))
		status := recorder.status
		if status == 0 {
			status = http.StatusOK
		}
		m.observeRequest(metricMethod(r.Method), routeTemplate(*pattern), status, time.Since(started), recorder.hijacked || status == http.StatusSwitchingProtocols)
	})
}

type metricsResponseWriter struct {
	http.ResponseWriter
	status   int
	hijacked bool
}

func (r *metricsResponseWriter) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *metricsResponseWriter) Write(data []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(data)
}

func (r *metricsResponseWriter) ReadFrom(source io.Reader) (int64, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if from, ok := r.ResponseWriter.(io.ReaderFrom); ok {
		return from.ReadFrom(source)
	}
	return io.Copy(struct{ io.Writer }{r.ResponseWriter}, source)
}

func (r *metricsResponseWriter) Flush() {
	_ = http.NewResponseController(r.ResponseWriter).Flush()
}

func (r *metricsResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(r.ResponseWriter).Hijack()
	if err == nil {
		r.hijacked = true
	}
	return conn, rw, err
}

func (r *metricsResponseWriter) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// WriteMetrics appends the server's connection, event, database, device,
// authentication and HTTP metrics.
func (s *Server) WriteMetrics(w *metrics.Writer) {
	sockets := s.sockets.countByKind()
	for _, kind := range socketKinds {
		w.Gauge("nexterm_websocket_connections", "Live WebSocket connections by kind.", float64(sockets[kind]), metrics.L("kind", kind))
	}
	w.Gauge("nexterm_event_subscribers", "Live event stream subscribers.", float64(s.events.SubscriberCount()))
	w.Counter("nexterm_event_subscriber_drops_total", "Event subscribers dropped because they could not keep up.", float64(s.events.SlowSubscriberDrops()))
	if s.channelStats != nil {
		stats := s.channelStats()
		w.Gauge("nexterm_terminal_channels", "Terminal channels by state.", float64(stats.LiveChannels), metrics.L("state", "live"))
		w.Gauge("nexterm_terminal_channels", "Terminal channels by state.", float64(stats.PendingChannels), metrics.L("state", "pending"))
	}
	if s.fleet != nil {
		w.Gauge("nexterm_device_agents_connected", "Device agents holding a live control channel.", float64(s.fleet.OnlineAgents()))
	}
	if s.db != nil {
		backend := metrics.L("backend", string(s.db.Backend()))
		stats := s.db.DB().Stats()
		w.Gauge("nexterm_db_max_open_connections", "Maximum open database connections (0 is unlimited).", float64(stats.MaxOpenConnections), backend)
		w.Gauge("nexterm_db_open_connections", "Open database connections.", float64(stats.OpenConnections), backend)
		w.Gauge("nexterm_db_in_use_connections", "Database connections in use.", float64(stats.InUse), backend)
		w.Gauge("nexterm_db_idle_connections", "Idle database connections.", float64(stats.Idle), backend)
		w.Counter("nexterm_db_wait_count_total", "Database connections waited for.", float64(stats.WaitCount), backend)
		w.Counter("nexterm_db_wait_duration_seconds_total", "Time spent waiting for database connections.", stats.WaitDuration.Seconds(), backend)
	}
	if s.metrics == nil {
		return
	}
	for _, series := range loginSeries {
		w.Counter("nexterm_auth_login_total", "Login attempts by step and result.", float64(s.metrics.logins[loginKey{series.step, series.result}].Load()),
			metrics.L("step", series.step), metrics.L("result", series.result))
	}
	s.writeHTTPMetrics(w)
}

func (s *Server) writeHTTPMetrics(w *metrics.Writer) {
	var requestKeys []httpRequestKey
	s.metrics.requests.Range(func(key, _ any) bool {
		requestKeys = append(requestKeys, key.(httpRequestKey))
		return true
	})
	sort.Slice(requestKeys, func(i, j int) bool {
		a, b := requestKeys[i], requestKeys[j]
		if a.route != b.route {
			return a.route < b.route
		}
		if a.method != b.method {
			return a.method < b.method
		}
		return a.class < b.class
	})
	for _, key := range requestKeys {
		counter, _ := s.metrics.requests.Load(key)
		w.Counter("nexterm_http_requests_total", "HTTP requests by method, route template and status class.", float64(counter.(*atomic.Uint64).Load()),
			metrics.L("method", key.method), metrics.L("route", key.route), metrics.L("code", key.class))
	}
	var routeKeys []httpRouteKey
	s.metrics.duration.Range(func(key, _ any) bool {
		routeKeys = append(routeKeys, key.(httpRouteKey))
		return true
	})
	sort.Slice(routeKeys, func(i, j int) bool {
		if routeKeys[i].route != routeKeys[j].route {
			return routeKeys[i].route < routeKeys[j].route
		}
		return routeKeys[i].method < routeKeys[j].method
	})
	for _, key := range routeKeys {
		histogram, _ := s.metrics.duration.Load(key)
		w.Histogram("nexterm_http_request_duration_seconds", "HTTP request duration by method and route template, excluding WebSocket upgrades.", histogram.(*metrics.Histogram).Snapshot(),
			metrics.L("method", key.method), metrics.L("route", key.route))
	}
}
