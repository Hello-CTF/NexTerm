package main

import (
	"net"
	"net/http"
	"runtime"
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
//
// Pages of the app must also never be framed by another document, so every response forbids embedding
// and requests that the browser marks as frame navigations are refused.
func runtimeRequestGuard(next http.Handler) http.Handler {
	hosts := wailsHostsForGOOS(runtime.GOOS)
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		rw := &antiFramingWriter{ResponseWriter: w}
		defer rw.applyHeaders()
		if dest := req.Header.Get("Sec-Fetch-Dest"); dest == "iframe" || dest == "frame" {
			http.Error(rw, "forbidden", http.StatusForbidden)
			return
		}
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
		if req.Method == http.MethodOptions || !hostIsWails(req.Host, hosts) ||
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

// wailsHostsForGOOS lists the host names under which the webview of a platform reaches the asset
// handler, derived from the Wails v3 beta.27 asset server: Windows (WebView2) and Android request
// http(s)://wails.localhost, while the other platforms use the custom scheme wails://localhost.
// "wails" is accepted there as well because the frontend treats wails://wails as the app origin; the
// source does not produce it, and it costs nothing to accept. The first entry is the canonical host.
func wailsHostsForGOOS(goos string) []string {
	switch goos {
	case "windows", "android":
		return []string{"wails.localhost"}
	}
	return []string{"localhost", "wails"}
}

// hostIsWails compares the request host name, ignoring case and any port such as the one the dev
// server adds, with the exact names in hosts.
func hostIsWails(host string, hosts []string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	for _, allowed := range hosts {
		if strings.EqualFold(host, allowed) {
			return true
		}
	}
	return false
}

// antiFramingWriter adds the headers that stop any document from embedding a response, just before
// the headers are sent.
type antiFramingWriter struct {
	http.ResponseWriter
	applied bool
}

func (w *antiFramingWriter) applyHeaders() {
	if w.applied {
		return
	}
	w.applied = true
	header := w.Header()
	header.Set("X-Frame-Options", "DENY")
	const directive = "frame-ancestors 'none'"
	values := header.Values("Content-Security-Policy")
	switch {
	case len(values) == 0:
		header.Set("Content-Security-Policy", directive)
	case !strings.Contains(strings.ToLower(strings.Join(values, ";")), "frame-ancestors"):
		header.Set("Content-Security-Policy", strings.TrimRight(strings.TrimSpace(values[0]), ";")+"; "+directive)
	default:
		// Several policies apply together, so an extra one can only tighten the existing frame-ancestors.
		header.Add("Content-Security-Policy", directive)
	}
}

func (w *antiFramingWriter) WriteHeader(status int) {
	w.applyHeaders()
	w.ResponseWriter.WriteHeader(status)
}

func (w *antiFramingWriter) Write(data []byte) (int, error) {
	w.applyHeaders()
	return w.ResponseWriter.Write(data)
}

func (w *antiFramingWriter) Flush() {
	w.applyHeaders()
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *antiFramingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
