package fleetserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/realip"
)

func TestEnrollThrottleKeyedByResolvedClientIP(t *testing.T) {
	fixture := newHTTPFixture(t, false)
	handler := fixture.service.Handler()
	attempt := func(ip string) int {
		request := httptest.NewRequest(http.MethodPost, "/device/enroll", strings.NewReader(`{"code":"bad","name":"n"}`))
		request.Header.Set("Content-Type", "application/json")
		request.RemoteAddr = "127.0.0.1:4000"
		request = request.WithContext(realip.WithClientIP(request.Context(), ip))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder.Code
	}
	throttled := false
	for i := 0; i < 100 && !throttled; i++ {
		throttled = attempt("203.0.113.9") == http.StatusTooManyRequests
	}
	if !throttled {
		t.Fatal("enroll was never rate limited")
	}
	if attempt("198.51.100.4") == http.StatusTooManyRequests {
		t.Fatal("a different resolved client shares the throttle")
	}
}
