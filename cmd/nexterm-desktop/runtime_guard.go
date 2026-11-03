package main

import (
	"net/http"
	"strings"
)

const (
	wailsRuntimePath      = "/wails/runtime"
	wailsStreamPrefix     = "/wails/stream/"
	wailsEventPayloadPath = "/wails/eventpayload/"

	wailsClientIDHeader      = "x-wails-client-id"
	wailsStreamSessionHeader = "x-wails-stream-session"
)

// runtimeRequestGuard keeps documents other than the app itself, such as third-party pages shown in
// the in-app browser, from reaching the Wails runtime endpoints. The runtime handler performs no
// origin check and also accepts calls from the query string, so anything that can issue a request
// to the app origin could otherwise invoke every IPC command.
//
// A legitimate call always carries a custom header that a cross-origin page cannot set without a
// CORS preflight, and preflights are refused here. Only paths whose legitimate callers send a
// custom header require it: the runtime call (x-wails-client-id) and the stream poll and send
// requests (x-wails-stream-session). Event payload fetches send no custom header, so they are
// limited by fetch metadata alone.
func runtimeRequestGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		path := req.URL.Path
		var requiredHeader string
		switch {
		case path == wailsRuntimePath:
			requiredHeader = wailsClientIDHeader
		case strings.HasPrefix(path, wailsStreamPrefix):
			requiredHeader = wailsStreamSessionHeader
		case strings.HasPrefix(path, wailsEventPayloadPath):
		default:
			next.ServeHTTP(rw, req)
			return
		}
		if req.Method == http.MethodOptions ||
			(requiredHeader != "" && req.Header.Get(requiredHeader) == "") ||
			!fetchSiteTrusted(req) {
			http.Error(rw, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(rw, req)
	})
}

// fetchSiteTrusted accepts requests the browser did not flag as coming from another origin. Webviews
// that omit Sec-Fetch-Site are accepted; the required custom header covers them.
func fetchSiteTrusted(req *http.Request) bool {
	switch req.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
		return true
	}
	return false
}
