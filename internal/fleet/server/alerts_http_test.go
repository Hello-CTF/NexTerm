package fleetserver

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
)

func alertEnroll(t *testing.T, f *httpFixture, session *httpSession, name string) string {
	t.Helper()
	call := f.call(t, http.MethodPost, "/device/enroll-codes", map[string]any{}, session, session.csrf)
	if call.status != http.StatusOK {
		t.Fatalf("issue code status=%d", call.status)
	}
	call = f.call(t, http.MethodPost, "/device/enroll", map[string]any{"code": call.body["code"], "name": name}, nil, "")
	if call.status != http.StatusOK {
		t.Fatalf("enroll status=%d body=%v", call.status, call.body)
	}
	return call.body["device_id"].(string)
}

func alertSeedEvent(t *testing.T, f *httpFixture, owner, device string) string {
	t.Helper()
	ev := &model.DeviceAlertEvent{ID: ids.New(), OwnerID: owner, DeviceID: device, RuleKey: "cpu", Kind: "fired", TS: time.Now().UnixMilli()}
	if err := f.service.db.Create(ev).Error; err != nil {
		t.Fatal(err)
	}
	return ev.ID
}

func alertSettingsBody() map[string]any {
	return map[string]any{
		"disk_enabled": true, "disk_threshold": 80.0, "mem_enabled": false, "mem_threshold": 70.0,
		"cpu_enabled": true, "cpu_threshold": 60.5, "offline_enabled": true, "offline_minutes": 30,
		"expected_offline": false, "quiet_enabled": true, "quiet_start": 1140, "quiet_end": 540, "quiet_tz": "Asia/Shanghai",
	}
}

func TestFleetHTTPAlertsAuthAndCSRF(t *testing.T) {
	f := newHTTPFixture(t, false)
	alice := f.createUser(t, "alice")
	s := f.session(t, alice)
	device := alertEnroll(t, f, s, "box")
	paths := []struct{ method, path string }{
		{http.MethodGet, "/fleet/alerts"},
		{http.MethodPost, "/fleet/alerts/ack"},
		{http.MethodGet, "/fleet/devices/" + device + "/alert-settings"},
		{http.MethodPut, "/fleet/devices/" + device + "/alert-settings"},
	}
	for _, p := range paths {
		if call := f.call(t, p.method, p.path, map[string]any{}, nil, ""); call.status != http.StatusUnauthorized {
			t.Errorf("%s %s anonymous status=%d", p.method, p.path, call.status)
		}
	}
	if call := f.call(t, http.MethodPost, "/fleet/alerts/ack", map[string]any{"ids": []string{"x"}}, s, ""); call.status != http.StatusForbidden {
		t.Errorf("ack without csrf status=%d", call.status)
	}
	if call := f.call(t, http.MethodPut, "/fleet/devices/"+device+"/alert-settings", alertSettingsBody(), s, ""); call.status != http.StatusForbidden {
		t.Errorf("put without csrf status=%d", call.status)
	}
	if call := f.call(t, http.MethodGet, "/fleet/devices/"+device+"/alert-settings", nil, s, ""); call.status != http.StatusOK {
		t.Errorf("get settings status=%d", call.status)
	}
}

func TestFleetHTTPAlertsListAckIsolation(t *testing.T) {
	f := newHTTPFixture(t, false)
	alice, bob := f.createUser(t, "alice"), f.createUser(t, "bob")
	as, bs := f.session(t, alice), f.session(t, bob)
	device := alertEnroll(t, f, as, "alice-box")
	evID := alertSeedEvent(t, f, alice.ID, device)

	call := f.call(t, http.MethodGet, "/fleet/alerts", nil, bs, "")
	if call.status != http.StatusOK || len(call.body["events"].([]any)) != 0 || call.body["active"] == nil || len(call.body["active"].([]any)) != 0 {
		t.Fatalf("bob list = %d %v", call.status, call.body)
	}
	call = f.call(t, http.MethodPost, "/fleet/alerts/ack", map[string]any{"ids": []string{evID}}, bs, bs.csrf)
	if call.status != http.StatusOK || call.body["acked"] != float64(0) {
		t.Fatalf("bob ack = %d %v", call.status, call.body)
	}
	call = f.call(t, http.MethodGet, "/fleet/alerts", nil, as, "")
	events := call.body["events"].([]any)
	if len(events) != 1 || events[0].(map[string]any)["id"] != evID || events[0].(map[string]any)["device_id"] != device {
		t.Fatalf("alice list = %v", call.body)
	}
	if call = f.call(t, http.MethodPost, "/fleet/alerts/ack", map[string]any{"ids": []string{}}, as, as.csrf); call.status != http.StatusBadRequest {
		t.Fatalf("empty ack status=%d", call.status)
	}
	call = f.call(t, http.MethodPost, "/fleet/alerts/ack", map[string]any{"ids": []string{evID}}, as, as.csrf)
	if call.status != http.StatusOK || call.body["ok"] != true || call.body["acked"] != float64(1) {
		t.Fatalf("alice ack = %d %v", call.status, call.body)
	}
	call = f.call(t, http.MethodGet, "/fleet/alerts", nil, as, "")
	if len(call.body["events"].([]any)) != 0 {
		t.Fatalf("acked event still listed: %v", call.body)
	}
}

func TestFleetHTTPAlertSettingsOwnerOnly(t *testing.T) {
	f := newHTTPFixture(t, false)
	root := f.createSuperadmin(t, "root")
	alice, bob := f.createUser(t, "alice"), f.createUser(t, "bob")
	as, bs, rs := f.session(t, alice), f.session(t, bob), f.session(t, root)
	device := alertEnroll(t, f, as, "alice-box")
	path := "/fleet/devices/" + device + "/alert-settings"

	call := f.call(t, http.MethodGet, path, nil, as, "")
	if call.status != http.StatusOK || call.body["device_id"] != device || call.body["disk_threshold"] != 90.0 ||
		call.body["offline_minutes"] != 10.0 || call.body["suspect_after_minutes"] != 5.0 || call.body["server_tz"] == "" {
		t.Fatalf("defaults = %d %v", call.status, call.body)
	}
	for name, sess := range map[string]*httpSession{"bob": bs, "superadmin": rs} {
		if call := f.call(t, http.MethodGet, path, nil, sess, ""); call.status != http.StatusForbidden {
			t.Errorf("%s get status=%d", name, call.status)
		}
		if call := f.call(t, http.MethodPut, path, alertSettingsBody(), sess, sess.csrf); call.status != http.StatusForbidden {
			t.Errorf("%s put status=%d", name, call.status)
		}
	}
	if call := f.call(t, http.MethodGet, path, nil, as, ""); call.body["disk_threshold"] != 90.0 {
		t.Fatalf("foreign put changed settings: %v", call.body)
	}

	call = f.call(t, http.MethodPut, path, alertSettingsBody(), as, as.csrf)
	if call.status != http.StatusOK || call.body["disk_threshold"] != 80.0 || call.body["cpu_threshold"] != 60.5 || call.body["quiet_tz"] != "Asia/Shanghai" {
		t.Fatalf("put = %d %v", call.status, call.body)
	}
	again := f.call(t, http.MethodGet, path, nil, as, "")
	for _, k := range []string{"disk_threshold", "mem_enabled", "cpu_threshold", "offline_minutes", "quiet_start", "quiet_end", "quiet_tz"} {
		if again.body[k] != call.body[k] {
			t.Errorf("%s: get=%v put=%v", k, again.body[k], call.body[k])
		}
	}

	bad := alertSettingsBody()
	bad["disk_threshold"] = 0
	if call := f.call(t, http.MethodPut, path, bad, as, as.csrf); call.status != http.StatusBadRequest {
		t.Errorf("bad threshold status=%d", call.status)
	}
	missing := alertSettingsBody()
	delete(missing, "cpu_enabled")
	if call := f.call(t, http.MethodPut, path, missing, as, as.csrf); call.status != http.StatusBadRequest {
		t.Errorf("missing field status=%d", call.status)
	}
	if call := f.call(t, http.MethodGet, "/fleet/devices/no-such-device/alert-settings", nil, as, ""); call.status != http.StatusNotFound {
		t.Errorf("unknown device status=%d", call.status)
	}

	if call := f.call(t, http.MethodPost, "/fleet/devices/"+device+"/revoke", map[string]any{}, as, as.csrf); call.status != http.StatusOK {
		t.Fatalf("revoke status=%d", call.status)
	}
	if call := f.call(t, http.MethodPut, path, alertSettingsBody(), as, as.csrf); call.status == http.StatusOK {
		t.Errorf("put on revoked device succeeded")
	}
}

// The evaluator's presence signal is the control-channel registry: a registered device is never offline.
func TestNewAlertEvaluatorUsesRegistryPresence(t *testing.T) {
	var clock time.Time = time.Unix(1_800_000_000, 0)
	nowMS := func() int64 { return clock.UnixMilli() }
	f := newHTTPFixture(t, false, WithNow(nowMS))
	alice := f.createUser(t, "alice")
	device := alertEnroll(t, f, f.session(t, alice), "box")
	// Enrollment alone leaves no sign of life; without one the offline rule has nothing to measure.
	if err := f.service.db.Model(&model.DeviceAgent{}).Where("device_id = ?", device).Update("last_seen_at", clock.UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	evaluator, err := NewAlertEvaluator(f.service, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, control := wsPair(t)
	f.service.registry.RegisterControl(device, control, "", nil)
	tick := func() {
		t.Helper()
		if err := evaluator.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	count := func() (events, states int64) {
		f.service.db.Model(&model.DeviceAlertEvent{}).Where("device_id = ?", device).Count(&events)
		f.service.db.Model(&model.DeviceAlertState{}).Where("device_id = ?", device).Count(&states)
		return
	}
	for i := 0; i < 3; i++ {
		clock = clock.Add(20 * time.Minute)
		tick()
	}
	if e, s := count(); e != 0 || s != 0 {
		t.Fatalf("registered device alerted: events=%d states=%d", e, s)
	}
	f.service.registry.UnregisterControl(device, control)
	for i := 0; i < 2; i++ {
		clock = clock.Add(20 * time.Minute)
		tick()
	}
	var fired int64
	f.service.db.Model(&model.DeviceAlertEvent{}).Where("device_id = ? AND rule_key = ? AND kind = ?", device, "offline", "fired").Count(&fired)
	if fired != 1 {
		t.Fatalf("offline fired events = %d, want 1", fired)
	}
}
