//go:build smoke

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync/atomic"
)

// desktopSmokeEvidenceWritten makes the evidence file single-shot: the first
// posted result is the verdict, so the file on disk and the Go-side exit code
// can never diverge when a racing re-injection posts a duplicate.
var desktopSmokeEvidenceWritten atomic.Bool

func desktopSmokeAssetHandler(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__desktop_smoke_result__" && r.Method == http.MethodPost {
			var result desktopSmokeResult
			if err := json.NewDecoder(r.Body).Decode(&result); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if desktopSmokeEvidenceWritten.CompareAndSwap(false, true) {
				raw, err := json.Marshal(result)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				if err := os.MkdirAll(".buildcheck/m27", 0o700); err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				if err := os.WriteFile(".buildcheck/m27/webview-smoke-result.json", raw, 0o600); err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				select {
				case desktopSmokeResults <- result:
				default:
				}
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		fmt.Fprintln(os.Stderr, "desktop smoke asset:", r.Method, r.URL.Path)
		handler.ServeHTTP(w, r)
	})
}
