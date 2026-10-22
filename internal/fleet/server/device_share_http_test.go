//go:build unix

package fleetserver

import (
	"net/http"
	"testing"
)

func TestDeviceShareLinkRoutesRetired(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "retired-device-link-owner")
	session := f.session(t, owner)
	f.service.SetSharePublicPage(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	calls := []httpCall{
		f.call(t, http.MethodPost, "/share/device-links", map[string]any{"device_id": "legacy-device"}, session, session.csrf),
		f.call(t, http.MethodGet, "/share/device-links", nil, session, ""),
		f.call(t, http.MethodPost, "/share/device-links/legacy-id/revoke", nil, session, session.csrf),
		f.call(t, http.MethodGet, "/share/public/device/legacy-token", nil, nil, ""),
	}
	for i, call := range calls {
		if call.status != http.StatusNotFound {
			t.Fatalf("retired device-link call %d status = %d, want 404 (body %v)", i, call.status, call.body)
		}
	}
	if status := dialShareViewerStatus(t, f, "/share/public/device/legacy-token"); status != http.StatusNotFound {
		t.Fatalf("retired device-link WS status = %d, want 404", status)
	}

	if call := f.call(t, http.MethodGet, "/share/public/legacy-session-token", nil, nil, ""); call.status != http.StatusNoContent {
		t.Fatalf("legacy session public page status = %d, want 204", call.status)
	}
}
