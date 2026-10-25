//go:build unix

package fleetserver

import (
	"net/http"
	"testing"
)

// The legacy share-link and device-link routes were removed: management routes 404, and the public token route answers
// 410 with no-store for plain GETs and WS upgrades alike, never reaching token lookup or any data path.
func TestLegacyShareLinkRoutesRetired(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "retired-link-owner")
	session := f.session(t, owner)

	calls := []httpCall{
		f.call(t, http.MethodGet, "/share/links", nil, session, ""),
		f.call(t, http.MethodPost, "/share/links/legacy-id/revoke", nil, session, session.csrf),
		f.call(t, http.MethodPost, "/share/device-links", map[string]any{"device_id": "legacy-device"}, session, session.csrf),
		f.call(t, http.MethodGet, "/share/device-links", nil, session, ""),
		f.call(t, http.MethodPost, "/share/device-links/legacy-id/revoke", nil, session, session.csrf),
		f.call(t, http.MethodGet, "/share/public/device/legacy-token", nil, nil, ""),
	}
	for i, call := range calls {
		if call.status != http.StatusNotFound && call.status != http.StatusMethodNotAllowed {
			t.Fatalf("retired link call %d status = %d, want 404 or 405 (body %v)", i, call.status, call.body)
		}
	}

	response, err := f.client.Get(f.http.URL + "/share/public/legacy-session-token")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusGone || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("legacy public page = %d %q, want 410 no-store", response.StatusCode, response.Header.Get("Cache-Control"))
	}
	if status := dialShareViewerStatus(t, f, "/share/public/legacy-session-token"); status != http.StatusGone {
		t.Fatalf("legacy public WS status = %d, want 410", status)
	}
}
