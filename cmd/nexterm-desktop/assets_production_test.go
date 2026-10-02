//go:build production

package main

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestProductionEmbeddedReactDist(t *testing.T) {
	handler, err := newDesktopAssets("")
	if err != nil {
		t.Fatal(err)
	}
	var modulePath string
	for _, path := range []string{"/", "/workspace/session-1"} {
		request := httptest.NewRequest(http.MethodGet, "http://wails.local"+path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d", path, response.Code)
		}
		body := response.Body.String()
		if !strings.Contains(body, `<div id="root"></div>`) || !strings.Contains(body, "/assets/index-") || !strings.Contains(body, desktopTransportMarker) {
			t.Fatalf("%s embedded index = %s", path, body)
		}
		if strings.Contains(body, `/wails/runtime.js`) {
			t.Fatalf("%s contains duplicate runtime: %s", path, body)
		}
		if modulePath == "" {
			modulePath = regexp.MustCompile(`src="([^"]+\.js)"`).FindStringSubmatch(body)[1]
		}
	}
	request := httptest.NewRequest(http.MethodGet, "http://wails.local"+modulePath, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.Len() == 0 {
		t.Fatalf("embedded module %s status = %d, bytes = %d", modulePath, response.Code, response.Body.Len())
	}
	if got := response.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Fatalf("module Cache-Control = %q", got)
	}
}
