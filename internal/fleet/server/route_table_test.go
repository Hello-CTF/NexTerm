package fleetserver

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

type recordingRegistrar struct{ patterns []string }

func (r *recordingRegistrar) Handle(pattern string, _ http.Handler) {
	r.patterns = append(r.patterns, pattern)
}

func (r *recordingRegistrar) HandleFunc(pattern string, _ func(http.ResponseWriter, *http.Request)) {
	r.patterns = append(r.patterns, pattern)
}

// The shared server mounts only fleetRoutePatterns; a route the fleet mux
// registers without a pattern there falls through to the web app's
// index.html, which a desktop then fails to decode as JSON.
func TestEveryFleetRouteIsMountedOnTheSharedMux(t *testing.T) {
	recorded := &recordingRegistrar{}
	newServiceFixture(t).service.mountRoutes(recorded)
	got := slices.Sorted(slices.Values(recorded.patterns))
	want := slices.Sorted(slices.Values(fleetRoutePatterns))
	if !slices.Equal(got, want) {
		t.Fatalf("fleet routes = %v\nmounted patterns = %v", got, want)
	}

	mux := http.NewServeMux()
	newServiceFixture(t).service.Mount(mux)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!doctype html>"))
	})
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/fleet/sessions", nil))
	if response.Code == http.StatusOK || response.Header().Get("Content-Type") == "text/html" {
		t.Fatalf("GET /fleet/sessions reached the web app: HTTP %d %s", response.Code, response.Header().Get("Content-Type"))
	}
}
