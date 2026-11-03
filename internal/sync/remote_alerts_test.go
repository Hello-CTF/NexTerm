package sync

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestRemoteAPIAlertsRoundTrip(t *testing.T) {
	fixture := newRemoteAPIFixture(t)
	fixture.createUser(t, "alice", "alice-pw-123")
	client := fixture.loginClient(t, "alice", "alice-pw-123")

	var ackBody, putBody map[string]any
	mux := http.NewServeMux()
	mux.Handle("GET /fleet/alerts", fixture.session(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSyncJSON(w, http.StatusOK, map[string]any{
			"active": []map[string]any{{"device_id": "d-1", "rule": "cpu", "status": "firing", "since": 5, "value": 91.5}},
			"events": []map[string]any{{"id": "e-1", "device_id": "d-1", "rule": "cpu", "kind": "fired", "ts": 9, "duration_ms": 0, "muted": false}},
		})
	})))
	mux.Handle("POST /fleet/alerts/ack", fixture.session(fixture.csrf(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&ackBody)
		writeSyncJSON(w, http.StatusOK, map[string]any{"ok": true, "acked": 2})
	}))))
	mux.Handle("GET /fleet/devices/{id}/alert-settings", fixture.session(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeSyncJSON(w, http.StatusOK, map[string]any{"device_id": r.PathValue("id"), "kind": "agent", "disk_threshold": 90, "server_tz": "UTC", "suspect_after_minutes": 5})
	})))
	mux.Handle("PUT /fleet/devices/{id}/alert-settings", fixture.session(fixture.csrf(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&putBody)
		writeSyncJSON(w, http.StatusOK, map[string]any{"device_id": r.PathValue("id"), "cpu_threshold": putBody["cpu_threshold"], "quiet_tz": putBody["quiet_tz"]})
	}))))
	mux.Handle("/", fixture.Config.Handler)
	fixture.Config.Handler = mux

	ctx := context.Background()
	alerts, err := client.ListAlerts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts.Active) != 1 || alerts.Active[0].Value == nil || *alerts.Active[0].Value != 91.5 || len(alerts.Events) != 1 || alerts.Events[0].ID != "e-1" {
		t.Fatalf("alerts = %+v", alerts)
	}
	ack, err := client.AckAlerts(ctx, []string{"e-1", "e-2"})
	if err != nil {
		t.Fatal(err)
	}
	if !ack.OK || ack.Acked != 2 {
		t.Fatalf("ack = %+v", ack)
	}
	if ids, _ := ackBody["ids"].([]any); len(ids) != 2 || ids[0] != "e-1" {
		t.Fatalf("ack body = %v", ackBody)
	}
	got, err := client.GetAlertSettings(ctx, "d-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.DeviceID != "d-1" || got.DiskThreshold != 90 || got.ServerTZ != "UTC" || got.SuspectAfterMinutes != 5 {
		t.Fatalf("settings = %+v", got)
	}
	put, err := client.PutAlertSettings(ctx, "d-1", RemoteAlertSettingsInput{CPUThreshold: 55, QuietTZ: "Asia/Shanghai"})
	if err != nil {
		t.Fatal(err)
	}
	if put.CPUThreshold != 55 || put.QuietTZ != "Asia/Shanghai" || len(putBody) != 13 {
		t.Fatalf("put = %+v body = %v", put, putBody)
	}
}
