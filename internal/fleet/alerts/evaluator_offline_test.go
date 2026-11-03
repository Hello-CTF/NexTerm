package alerts

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
)

// presenceSet is a concurrency-safe fake of the control-channel registry.
type presenceSet struct {
	mu sync.Mutex
	on map[string]bool
}

func (p *presenceSet) set(id string, v bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.on == nil {
		p.on = map[string]bool{}
	}
	p.on[id] = v
}

func (p *presenceSet) online(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.on[id]
}

func (h *harness) offlineEvaluator(p *presenceSet) *Evaluator {
	h.t.Helper()
	e, err := NewEvaluator(Config{
		DB: h.db, Now: func() time.Time { return time.UnixMilli(h.clock.Load()) }, Presence: p.online,
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return e
}

// agentRow gives the device a device_agent row (and its credential) with the given last_seen_at.
func (h *harness) agentRow(owner, deviceID string, lastSeen *int64) {
	h.t.Helper()
	cred := &model.SyncCredential{ID: ids.New(), UserID: owner, DeviceID: deviceID, Purpose: "device-agent", SecretHash: ids.New(), CreatedAt: baseTS}
	if err := h.db.Create(cred).Error; err != nil {
		h.t.Fatal(err)
	}
	row := &model.DeviceAgent{DeviceID: deviceID, CredentialID: cred.ID, CreatedAt: baseTS, LastSeenAt: lastSeen}
	if err := h.db.Create(row).Error; err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) heard(deviceID string, ts int64) {
	h.t.Helper()
	if err := h.db.Model(&model.DeviceAgent{}).Where("device_id = ?", deviceID).Update("last_seen_at", ts).Error; err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) settings(owner, deviceID string, mutate func(*Settings)) {
	h.t.Helper()
	s := &Settings{DeviceID: deviceID, OwnerID: owner, UpdatedAt: baseTS}
	mutate(s)
	if err := putSettings(context.Background(), h.db, s); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) offlineState(deviceID string) *model.DeviceAlertState {
	h.t.Helper()
	states, err := loadStates(context.Background(), h.db)
	if err != nil {
		h.t.Fatal(err)
	}
	return states[stateKey{deviceID, RuleOffline}]
}

func (h *harness) status(deviceID string) string {
	if st := h.offlineState(deviceID); st != nil {
		return st.Status
	}
	return ""
}

// offlineSetup returns an agent device last heard at baseTS+10m with an evaluator started at baseTS.
func offlineSetup(t *testing.T, name string) (h *harness, e *Evaluator, p *presenceSet, owner, dev string) {
	h = newHarness(t)
	p = &presenceSet{}
	owner, dev = h.device(name)
	h.agentRow(owner, dev, ptr(baseTS+10*minMS))
	e = h.offlineEvaluator(p)
	return
}

func TestOfflineSingleMissedHeartbeatDatabase(t *testing.T) {
	h, e, _, _, dev := offlineSetup(t, "off-miss")
	h.tick(e, baseTS+12*minMS) // silence 2 minutes
	if len(h.events(dev)) != 0 || h.status(dev) == StatusFiring || h.status(dev) == StatusSuspect {
		t.Fatalf("one missed sync changed state: %q %+v", h.status(dev), h.events(dev))
	}
}

func TestOfflineSustainedSilenceFiresOnceDatabase(t *testing.T) {
	h, e, _, owner, dev := offlineSetup(t, "off-fire")
	for m := int64(11); m <= 24; m++ {
		h.tick(e, baseTS+m*minMS)
		switch m {
		case 15:
			if h.status(dev) != StatusSuspect || len(h.events(dev)) != 0 {
				t.Fatalf("5 min: %q %+v", h.status(dev), h.events(dev))
			}
		case 19:
			if len(h.events(dev)) != 0 {
				t.Fatalf("fired before 10 minutes: %+v", h.events(dev))
			}
		}
	}
	evs := h.events(dev)
	if len(evs) != 1 {
		t.Fatalf("events = %+v", evs)
	}
	ev := evs[0]
	if ev.Kind != KindFired || ev.RuleKey != "offline" || ev.OwnerID != owner || ev.TS != baseTS+20*minMS ||
		ev.DurationMS != 10*minMS || ev.Value != nil || ev.Threshold != nil || ev.Muted {
		t.Fatalf("event = %+v", ev)
	}
	aud := h.audits("device_alert_fired")
	if len(aud) != 1 || aud[0].OwnerID != owner || aud[0].AssetID == nil || *aud[0].AssetID != dev {
		t.Fatalf("audit = %+v", aud)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(aud[0].PayloadJSON), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["rule"] != "offline" || payload["action"] != "alert_fired" || payload["duration_ms"] != float64(10*minMS) {
		t.Fatalf("payload = %v", payload)
	}
	if _, ok := payload["value"]; ok {
		t.Fatalf("offline payload carries value: %v", payload)
	}
}

func TestOfflineMinutesSettingsDatabase(t *testing.T) {
	t.Run("longer than default", func(t *testing.T) {
		h, e, _, owner, dev := offlineSetup(t, "off-x30")
		h.settings(owner, dev, func(s *Settings) { s.OfflineMinutes = ptr(int64(30)) })
		h.tick(e, baseTS+20*minMS)
		h.tick(e, baseTS+25*minMS)
		h.tick(e, baseTS+39*minMS)
		if len(h.events(dev)) != 0 {
			t.Fatalf("fired before 30 minutes: %+v", h.events(dev))
		}
		h.tick(e, baseTS+40*minMS)
		if evs := h.events(dev); len(evs) != 1 || evs[0].DurationMS != 30*minMS {
			t.Fatalf("events = %+v", evs)
		}
	})
	t.Run("shorter than suspect threshold uses it", func(t *testing.T) {
		h, e, _, owner, dev := offlineSetup(t, "off-x1")
		h.settings(owner, dev, func(s *Settings) { s.OfflineMinutes = ptr(int64(1)) })
		h.tick(e, baseTS+14*minMS)
		if h.status(dev) != "" && h.status(dev) != StatusOK {
			t.Fatalf("state at 4 min = %q", h.status(dev))
		}
		h.tick(e, baseTS+15*minMS)
		if len(h.events(dev)) != 0 {
			t.Fatalf("fired at suspect time: %+v", h.events(dev))
		}
		h.tick(e, baseTS+16*minMS)
		if len(h.events(dev)) != 1 {
			t.Fatalf("events = %+v", h.events(dev))
		}
	})
	t.Run("invalid falls back to default", func(t *testing.T) {
		h, e, _, owner, dev := offlineSetup(t, "off-bad")
		h.settings(owner, dev, func(s *Settings) { s.OfflineMinutes = ptr(int64(5000)) })
		h.tick(e, baseTS+15*minMS)
		h.tick(e, baseTS+20*minMS)
		if len(h.events(dev)) != 1 {
			t.Fatalf("events = %+v", h.events(dev))
		}
	})
}

func TestOfflineQuietHoursDatabase(t *testing.T) {
	// baseTS is 08:00 UTC, which is 16:00 in Asia/Shanghai.
	quiet := func(tz string) func(*Settings) {
		return func(s *Settings) { s.QuietEnabled, s.QuietStart, s.QuietEnd, s.QuietTZ = true, 19*60, 9*60, tz }
	}
	t.Run("utc quiet suppresses and recovery is muted", func(t *testing.T) {
		h, e, p, owner, dev := offlineSetup(t, "off-quiet-utc")
		h.settings(owner, dev, quiet("UTC"))
		h.tick(e, baseTS+15*minMS)
		h.tick(e, baseTS+20*minMS)
		h.tick(e, baseTS+30*minMS)
		if len(h.events(dev)) != 0 || h.status(dev) != StatusMuted {
			t.Fatalf("quiet: %q %+v", h.status(dev), h.events(dev))
		}
		p.set(dev, true)
		h.tick(e, baseTS+40*minMS)
		h.tick(e, baseTS+45*minMS)
		evs := h.events(dev)
		if len(evs) != 1 || evs[0].Kind != KindResolved || !evs[0].Muted || evs[0].DurationMS != 30*minMS {
			t.Fatalf("events = %+v", evs)
		}
		aud := h.audits("device_alert_resolved")
		var payload map[string]any
		if len(aud) != 1 {
			t.Fatalf("audits = %+v", aud)
		}
		if err := json.Unmarshal([]byte(aud[0].PayloadJSON), &payload); err != nil {
			t.Fatal(err)
		}
		if payload["muted"] != true {
			t.Fatalf("payload = %v", payload)
		}
		if h.status(dev) != StatusOK {
			t.Fatalf("status = %q", h.status(dev))
		}
	})
	t.Run("same instant outside quiet in shanghai", func(t *testing.T) {
		h, e, _, owner, dev := offlineSetup(t, "off-quiet-sh")
		h.settings(owner, dev, quiet("Asia/Shanghai"))
		h.tick(e, baseTS+15*minMS)
		h.tick(e, baseTS+20*minMS)
		if evs := h.events(dev); len(evs) != 1 || evs[0].Kind != KindFired {
			t.Fatalf("events = %+v", evs)
		}
	})
	t.Run("shanghai quiet end restarts the alert timer", func(t *testing.T) {
		// 19:00-09:00 in Shanghai is 11:00Z-01:00Z, i.e. baseTS+3h to baseTS+17h.
		h, e, _, owner, dev := offlineSetup(t, "off-quiet-sh-end")
		h.settings(owner, dev, quiet("Asia/Shanghai"))
		h.heard(dev, baseTS+190*minMS) // 19:10 Shanghai
		h.tick(e, baseTS+195*minMS)
		h.tick(e, baseTS+200*minMS)
		h.tick(e, baseTS+17*60*minMS-minMS)
		if h.status(dev) != StatusMuted || len(h.events(dev)) != 0 {
			t.Fatalf("quiet: %q %+v", h.status(dev), h.events(dev))
		}
		h.tick(e, baseTS+17*60*minMS) // 09:00 Shanghai
		h.tick(e, baseTS+17*60*minMS+9*minMS)
		if len(h.events(dev)) != 0 {
			t.Fatalf("fired before A after the quiet end: %+v", h.events(dev))
		}
		h.tick(e, baseTS+17*60*minMS+10*minMS)
		if evs := h.events(dev); len(evs) != 1 || evs[0].Kind != KindFired {
			t.Fatalf("events = %+v", evs)
		}
	})
	t.Run("device lost just before the quiet end alerts A after it", func(t *testing.T) {
		h, e, _, owner, dev := offlineSetup(t, "off-quiet-sh-late")
		h.settings(owner, dev, quiet("Asia/Shanghai"))
		h.heard(dev, baseTS+17*60*minMS-3*minMS) // 08:57 Shanghai
		h.tick(e, baseTS+17*60*minMS+2*minMS)
		h.tick(e, baseTS+17*60*minMS+7*minMS) // would fire here without the quiet end
		h.tick(e, baseTS+17*60*minMS+9*minMS)
		if len(h.events(dev)) != 0 {
			t.Fatalf("events = %+v", h.events(dev))
		}
		h.tick(e, baseTS+17*60*minMS+10*minMS)
		if evs := h.events(dev); len(evs) != 1 || evs[0].Kind != KindFired {
			t.Fatalf("events = %+v", evs)
		}
	})
	t.Run("quiet ends while still offline", func(t *testing.T) {
		h, e, _, owner, dev := offlineSetup(t, "off-quiet-end")
		h.settings(owner, dev, quiet("UTC"))
		h.tick(e, baseTS+15*minMS)
		h.tick(e, baseTS+20*minMS)
		h.tick(e, baseTS+59*minMS) // 08:59Z
		if len(h.events(dev)) != 0 {
			t.Fatalf("events = %+v", h.events(dev))
		}
		h.tick(e, baseTS+60*minMS) // 09:00Z
		h.tick(e, baseTS+69*minMS)
		if len(h.events(dev)) != 0 {
			t.Fatalf("fired before A after the quiet end: %+v", h.events(dev))
		}
		h.tick(e, baseTS+70*minMS) // 09:10Z
		evs := h.events(dev)
		if len(evs) != 1 || evs[0].Kind != KindFired || evs[0].DurationMS != 60*minMS {
			t.Fatalf("events = %+v", evs)
		}
		h.tick(e, baseTS+61*minMS)
		if len(h.events(dev)) != 1 {
			t.Fatalf("repeat: %+v", h.events(dev))
		}
	})
}

func TestOfflineExpectedOfflineDatabase(t *testing.T) {
	h, e, p, owner, dev := offlineSetup(t, "off-expected")
	h.settings(owner, dev, func(s *Settings) { s.ExpectedOffline = true })
	h.tick(e, baseTS+15*minMS)
	h.tick(e, baseTS+25*minMS)
	if len(h.events(dev)) != 0 || h.status(dev) != StatusMuted {
		t.Fatalf("expected offline: %q %+v", h.status(dev), h.events(dev))
	}
	p.set(dev, true)
	h.tick(e, baseTS+26*minMS)
	h.tick(e, baseTS+31*minMS)
	if evs := h.events(dev); len(evs) != 1 || !evs[0].Muted || evs[0].Kind != KindResolved {
		t.Fatalf("events = %+v", evs)
	}
}

func TestOfflineRecoveryDatabase(t *testing.T) {
	t.Run("presence resolves once", func(t *testing.T) {
		h, e, p, owner, dev := offlineSetup(t, "off-rec")
		h.tick(e, baseTS+15*minMS)
		h.tick(e, baseTS+20*minMS)
		p.set(dev, true)
		h.tick(e, baseTS+25*minMS)
		h.tick(e, baseTS+29*minMS)
		if len(h.events(dev)) != 1 || h.status(dev) != StatusFiring {
			t.Fatalf("resolved before the hold: %q %+v", h.status(dev), h.events(dev))
		}
		h.tick(e, baseTS+30*minMS)
		evs := h.events(dev)
		if len(evs) != 2 || evs[1].Kind != KindResolved || evs[1].Muted || evs[1].DurationMS != 15*minMS || evs[1].OwnerID != owner {
			t.Fatalf("events = %+v", evs)
		}
		st := h.offlineState(dev)
		if st.Status != StatusOK || st.Since != 0 || st.FiredAt != 0 {
			t.Fatalf("state = %+v", st)
		}
		if n := len(h.audits("device_alert_resolved")); n != 1 {
			t.Fatalf("resolved audits = %d", n)
		}
		// Offline again later: no cooldown, silence must build up and fires anew.
		p.set(dev, false)
		h.heard(dev, baseTS+30*minMS)
		h.tick(e, baseTS+35*minMS)
		h.tick(e, baseTS+40*minMS)
		if evs := h.events(dev); len(evs) != 3 || evs[2].Kind != KindFired {
			t.Fatalf("events = %+v", evs)
		}
	})
	t.Run("last_seen update resolves", func(t *testing.T) {
		h, e, _, _, dev := offlineSetup(t, "off-rec-seen")
		h.tick(e, baseTS+15*minMS)
		h.tick(e, baseTS+20*minMS)
		for m := int64(22); m <= 28; m++ {
			h.heard(dev, baseTS+m*minMS)
			h.tick(e, baseTS+m*minMS)
		}
		if evs := h.events(dev); len(evs) != 2 || evs[1].Kind != KindResolved {
			t.Fatalf("events = %+v", evs)
		}
	})
	t.Run("suspect recovery is silent", func(t *testing.T) {
		h, e, p, _, dev := offlineSetup(t, "off-rec-suspect")
		h.tick(e, baseTS+15*minMS)
		if h.status(dev) != StatusSuspect {
			t.Fatalf("status = %q", h.status(dev))
		}
		p.set(dev, true)
		h.tick(e, baseTS+16*minMS)
		if len(h.events(dev)) != 0 || h.status(dev) != StatusOK {
			t.Fatalf("%q %+v", h.status(dev), h.events(dev))
		}
		if n := len(h.audits("device_alert_resolved")) + len(h.audits("device_alert_fired")); n != 0 {
			t.Fatalf("audits = %d", n)
		}
	})
}

func TestOfflineRestartDatabase(t *testing.T) {
	t.Run("recent last_seen does not alert right after restart", func(t *testing.T) {
		h := newHarness(t)
		p := &presenceSet{}
		owner, dev := h.device("off-restart-old")
		h.agentRow(owner, dev, ptr(baseTS-3*minMS)) // within A of the start: timed from the start
		e := h.offlineEvaluator(p)
		h.tick(e, baseTS+minMS) // warm-up
		if h.status(dev) != "" {
			t.Fatalf("evaluated during warm-up: %q", h.status(dev))
		}
		h.tick(e, baseTS+2*minMS)
		h.tick(e, baseTS+4*minMS)
		if h.status(dev) == StatusSuspect || h.status(dev) == StatusFiring || len(h.events(dev)) != 0 {
			t.Fatalf("alerted right after restart: %q %+v", h.status(dev), h.events(dev))
		}
		h.tick(e, baseTS+5*minMS)
		h.tick(e, baseTS+10*minMS)
		if evs := h.events(dev); len(evs) != 1 || evs[0].Kind != KindFired {
			t.Fatalf("events = %+v", evs)
		}
	})
	t.Run("firing before restart resolves once and never refires", func(t *testing.T) {
		h := newHarness(t)
		p := &presenceSet{}
		owner, dev := h.device("off-restart-firing")
		h.agentRow(owner, dev, ptr(baseTS-60*minMS))
		st := &model.DeviceAlertState{DeviceID: dev, RuleKey: "offline", OwnerID: owner, Status: StatusFiring, Since: baseTS - 60*minMS, FiredAt: baseTS - 50*minMS, UpdatedAt: baseTS - 50*minMS}
		if err := h.db.Create(st).Error; err != nil {
			t.Fatal(err)
		}
		e := h.offlineEvaluator(p)
		h.tick(e, baseTS+2*minMS)
		h.tick(e, baseTS+3*minMS)
		if len(h.events(dev)) != 0 || h.status(dev) != StatusFiring {
			t.Fatalf("restart disturbed the alert: %q %+v", h.status(dev), h.events(dev))
		}
		p.set(dev, true)
		h.tick(e, baseTS+4*minMS)
		h.tick(e, baseTS+9*minMS)
		evs := h.events(dev)
		if len(evs) != 1 || evs[0].Kind != KindResolved || evs[0].Muted || evs[0].DurationMS != 64*minMS {
			t.Fatalf("events = %+v", evs)
		}
	})
}

func TestOfflineEligibilityDatabase(t *testing.T) {
	t.Run("local-helper off by default", func(t *testing.T) {
		h := newHarness(t)
		p := &presenceSet{}
		u, err := h.accounts.CreateUser(context.Background(), "off-helper", "", "pw-123456")
		if err != nil {
			t.Fatal(err)
		}
		d, err := h.accounts.RegisterDevice(context.Background(), u.ID, "helper", "local-helper")
		if err != nil {
			t.Fatal(err)
		}
		h.agentRow(u.ID, d.ID, ptr(baseTS+10*minMS))
		e := h.offlineEvaluator(p)
		h.tick(e, baseTS+15*minMS)
		h.tick(e, baseTS+20*minMS)
		if len(h.events(d.ID)) != 0 {
			t.Fatalf("helper alerted by default: %+v", h.events(d.ID))
		}
		h.settings(u.ID, d.ID, func(s *Settings) { s.OfflineEnabled = ptr(true) })
		h.tick(e, baseTS+21*minMS)
		h.tick(e, baseTS+22*minMS)
		if evs := h.events(d.ID); len(evs) != 1 || evs[0].Kind != KindFired {
			t.Fatalf("events = %+v", evs)
		}
	})
	t.Run("agent can be disabled and resets silently", func(t *testing.T) {
		h, e, _, owner, dev := offlineSetup(t, "off-disable")
		h.tick(e, baseTS+15*minMS)
		h.tick(e, baseTS+20*minMS)
		h.settings(owner, dev, func(s *Settings) { s.OfflineEnabled = ptr(false) })
		h.tick(e, baseTS+21*minMS)
		if h.status(dev) != StatusOK || len(h.events(dev)) != 1 {
			t.Fatalf("%q %+v", h.status(dev), h.events(dev))
		}
	})
	t.Run("no agent row", func(t *testing.T) {
		h := newHarness(t)
		_, dev := h.device("off-norow")
		e := h.offlineEvaluator(&presenceSet{})
		h.tick(e, baseTS+15*minMS)
		h.tick(e, baseTS+60*minMS)
		if h.status(dev) != "" || len(h.events(dev)) != 0 {
			t.Fatalf("%q %+v", h.status(dev), h.events(dev))
		}
	})
	t.Run("revoked", func(t *testing.T) {
		h, e, _, _, dev := offlineSetup(t, "off-revoked")
		if err := h.db.Model(&model.UserDevice{}).Where("id = ?", dev).Update("revoked_at", baseTS).Error; err != nil {
			t.Fatal(err)
		}
		h.tick(e, baseTS+15*minMS)
		h.tick(e, baseTS+25*minMS)
		if h.status(dev) != "" || len(h.events(dev)) != 0 {
			t.Fatalf("%q %+v", h.status(dev), h.events(dev))
		}
	})
	t.Run("never heard", func(t *testing.T) {
		h := newHarness(t)
		owner, dev := h.device("off-never")
		h.agentRow(owner, dev, nil)
		e := h.offlineEvaluator(&presenceSet{})
		h.tick(e, baseTS+15*minMS)
		h.tick(e, baseTS+60*minMS)
		if h.status(dev) != "" {
			t.Fatalf("status = %q", h.status(dev))
		}
	})
	t.Run("device-clock metric samples are not a heartbeat", func(t *testing.T) {
		h := newHarness(t)
		owner, dev := h.device("off-metric")
		h.agentRow(owner, dev, ptr(baseTS+10*minMS))
		// A sample stamped far in the future by a skewed device clock must not keep it online.
		h.cpu(dev, baseTS+600*minMS, baseTS+601*minMS, 10)
		e := h.offlineEvaluator(&presenceSet{})
		h.tick(e, baseTS+15*minMS)
		if h.status(dev) != StatusSuspect {
			t.Fatalf("status = %q", h.status(dev))
		}
		h.tick(e, baseTS+21*minMS)
		if evs := h.events(dev); len(evs) != 1 || evs[0].Kind != KindFired {
			t.Fatalf("events = %+v", evs)
		}
	})
	t.Run("silence counts from the last observed presence", func(t *testing.T) {
		h, e, p, _, dev := offlineSetup(t, "off-presence")
		p.set(dev, true)
		h.tick(e, baseTS+30*minMS)
		p.set(dev, false) // channel drops; the stored last_seen stays at baseTS+10m
		h.tick(e, baseTS+33*minMS)
		h.tick(e, baseTS+36*minMS) // 6 minutes since presence
		if h.status(dev) != StatusSuspect || len(h.events(dev)) != 0 {
			t.Fatalf("status = %q %+v", h.status(dev), h.events(dev))
		}
		h.tick(e, baseTS+41*minMS)
		if evs := h.events(dev); len(evs) != 1 || evs[0].Kind != KindFired {
			t.Fatalf("events = %+v", evs)
		}
	})
}

func TestOfflineDormantDatabase(t *testing.T) {
	h := newHarness(t)
	p := &presenceSet{}
	owner, dev := h.device("off-dormant")
	h.agentRow(owner, dev, ptr(baseTS-30*24*60*minMS))
	e := h.offlineEvaluator(p)
	for _, m := range []int64{2, 3, 10, 60, 24 * 60} {
		h.tick(e, baseTS+m*minMS)
		if h.status(dev) != StatusDormant || len(h.events(dev)) != 0 {
			t.Fatalf("t+%dm: %q %+v", m, h.status(dev), h.events(dev))
		}
	}
	if n := len(h.audits("device_alert_fired")) + len(h.audits("device_alert_resolved")); n != 0 {
		t.Fatalf("audits = %d", n)
	}
	// Coming back online returns to ok without an event.
	p.set(dev, true)
	h.tick(e, baseTS+25*60*minMS)
	if h.status(dev) != StatusOK || len(h.events(dev)) != 0 {
		t.Fatalf("after recovery: %q %+v", h.status(dev), h.events(dev))
	}
	// A later outage alerts exactly once.
	p.set(dev, false)
	h.heard(dev, baseTS+25*60*minMS)
	for _, m := range []int64{1, 6, 11, 20} {
		h.tick(e, baseTS+25*60*minMS+m*minMS)
	}
	if evs := h.events(dev); len(evs) != 1 || evs[0].Kind != KindFired {
		t.Fatalf("events = %+v", evs)
	}
}

func TestOfflineRecoveryHoldDatabase(t *testing.T) {
	t.Run("flapping device gets one fired and no resolved", func(t *testing.T) {
		h, e, p, _, dev := offlineSetup(t, "off-flap")
		h.tick(e, baseTS+15*minMS)
		h.tick(e, baseTS+20*minMS)
		at := baseTS + 21*minMS
		for range 4 {
			p.set(dev, true)
			h.tick(e, at)
			h.tick(e, at+minMS)
			h.tick(e, at+2*minMS)
			p.set(dev, false)
			h.heard(dev, at+2*minMS)
			h.tick(e, at+5*minMS) // 3 minutes of silence breaks the hold
			at += 6 * minMS
		}
		if evs := h.events(dev); len(evs) != 1 || evs[0].Kind != KindFired || h.status(dev) != StatusFiring {
			t.Fatalf("%q %+v", h.status(dev), h.events(dev))
		}
		p.set(dev, true)
		for m := int64(0); m <= 5; m++ {
			h.tick(e, at+m*minMS)
		}
		evs := h.events(dev)
		if len(evs) != 2 || evs[1].Kind != KindResolved || h.status(dev) != StatusOK {
			t.Fatalf("%q %+v", h.status(dev), evs)
		}
		// Duration is the outage up to the start of the stable period (since = last heard).
		if want := at - (baseTS + 10*minMS); evs[1].DurationMS != want {
			t.Fatalf("duration = %d, want %d", evs[1].DurationMS, want)
		}
	})
	t.Run("muted device recovers with a single muted record", func(t *testing.T) {
		h, e, p, owner, dev := offlineSetup(t, "off-mute-hold")
		h.settings(owner, dev, func(s *Settings) { s.ExpectedOffline = true })
		h.tick(e, baseTS+15*minMS)
		h.tick(e, baseTS+21*minMS)
		if h.status(dev) != StatusMuted {
			t.Fatalf("status = %q", h.status(dev))
		}
		p.set(dev, true)
		for m := int64(30); m <= 36; m++ {
			h.tick(e, baseTS+m*minMS)
		}
		evs := h.events(dev)
		if len(evs) != 1 || evs[0].Kind != KindResolved || !evs[0].Muted {
			t.Fatalf("events = %+v", evs)
		}
	})
	t.Run("recovery hold survives a restart", func(t *testing.T) {
		h, e, p, _, dev := offlineSetup(t, "off-hold-restart")
		h.tick(e, baseTS+15*minMS)
		h.tick(e, baseTS+20*minMS)
		p.set(dev, true)
		h.tick(e, baseTS+30*minMS)
		if st := h.offlineState(dev); st.Status != StatusFiring || st.RecoveringSince != baseTS+30*minMS {
			t.Fatalf("state = %+v", st)
		}
		restart := baseTS + 31*minMS
		h.clock.Store(restart)
		e2 := h.offlineEvaluator(p)
		h.tick(e2, restart+2*minMS) // warm-up ends
		h.tick(e2, restart+3*minMS)
		if len(h.events(dev)) != 1 {
			t.Fatalf("resolved too early: %+v", h.events(dev))
		}
		h.tick(e2, baseTS+36*minMS)
		if evs := h.events(dev); len(evs) != 2 || evs[1].Kind != KindResolved {
			t.Fatalf("events = %+v", evs)
		}
	})
}

func (h *harness) setOwnerState(owner, state string) {
	h.t.Helper()
	if err := h.db.Model(&model.AppUser{}).Where("id = ?", owner).Update("state", state).Error; err != nil {
		h.t.Fatal(err)
	}
}

func TestEvaluatorDisabledOwnerDatabase(t *testing.T) {
	t.Run("threshold rule resumes without duplicates", func(t *testing.T) {
		h := newHarness(t)
		owner, dev := h.device("alerts-disabled-cpu")
		e := h.evaluator()
		h.cpu(dev, baseTS-20*minMS, baseTS+30*minMS, 95)
		h.setOwnerState(owner, "disabled")
		for _, m := range []int64{2, 3, 20, 30} {
			h.tick(e, baseTS+m*minMS)
		}
		if h.offlineStateAny(dev) || len(h.events(dev)) != 0 || len(h.audits("device_alert_fired")) != 0 {
			t.Fatalf("disabled owner was evaluated: %+v", h.events(dev))
		}
		h.setOwnerState(owner, "reset_required") // also skipped
		h.tick(e, baseTS+31*minMS)
		if len(h.events(dev)) != 0 {
			t.Fatalf("reset_required owner was evaluated: %+v", h.events(dev))
		}
		h.setOwnerState(owner, "active")
		h.tick(e, baseTS+32*minMS)
		h.tick(e, baseTS+33*minMS)
		if evs := h.events(dev); len(evs) != 1 || evs[0].Kind != KindFired {
			t.Fatalf("events = %+v", evs)
		}
	})
	t.Run("reset_required owner is skipped and timed from the recovery", func(t *testing.T) {
		h, e, _, owner, dev := offlineSetup(t, "off-reset-required")
		h.setOwnerState(owner, "reset_required")
		h.tick(e, baseTS+15*minMS)
		h.tick(e, baseTS+30*minMS)
		if h.status(dev) != "" || len(h.events(dev)) != 0 {
			t.Fatalf("%q %+v", h.status(dev), h.events(dev))
		}
		h.setOwnerState(owner, "active")
		h.tick(e, baseTS+31*minMS)
		h.tick(e, baseTS+33*minMS)
		if h.status(dev) == StatusSuspect || len(h.events(dev)) != 0 {
			t.Fatalf("%q %+v", h.status(dev), h.events(dev))
		}
		h.tick(e, baseTS+41*minMS)
		h.tick(e, baseTS+42*minMS)
		if evs := h.events(dev); len(evs) != 1 || evs[0].Kind != KindFired {
			t.Fatalf("events = %+v", evs)
		}
	})
	t.Run("offline rule resumes and keeps its state meanwhile", func(t *testing.T) {
		h, e, p, owner, dev := offlineSetup(t, "off-disabled")
		h.setOwnerState(owner, "disabled")
		h.tick(e, baseTS+15*minMS)
		h.tick(e, baseTS+30*minMS)
		if h.status(dev) != "" || len(h.events(dev)) != 0 {
			t.Fatalf("%q %+v", h.status(dev), h.events(dev))
		}
		h.setOwnerState(owner, "active")
		h.tick(e, baseTS+31*minMS)
		h.tick(e, baseTS+33*minMS) // 2 minutes after re-enabling
		if st := h.status(dev); st == StatusSuspect || st == StatusFiring || len(h.events(dev)) != 0 {
			t.Fatalf("alerted right after re-enabling: %q %+v", st, h.events(dev))
		}
		h.tick(e, baseTS+36*minMS) // S after re-enabling
		if h.status(dev) != StatusSuspect || len(h.events(dev)) != 0 {
			t.Fatalf("%q %+v", h.status(dev), h.events(dev))
		}
		h.tick(e, baseTS+40*minMS)
		if len(h.events(dev)) != 0 {
			t.Fatalf("fired before A: %+v", h.events(dev))
		}
		h.tick(e, baseTS+41*minMS) // A after re-enabling
		h.tick(e, baseTS+42*minMS)
		if evs := h.events(dev); len(evs) != 1 || evs[0].Kind != KindFired {
			t.Fatalf("events = %+v", evs)
		}
		// An existing firing state is kept while disabled, even when the device comes back.
		h.setOwnerState(owner, "disabled")
		p.set(dev, true)
		h.tick(e, baseTS+40*minMS)
		h.tick(e, baseTS+50*minMS)
		if h.status(dev) != StatusFiring || len(h.events(dev)) != 1 {
			t.Fatalf("%q %+v", h.status(dev), h.events(dev))
		}
	})
}

func TestServiceListSkipsRevokedDeviceEventsDatabase(t *testing.T) {
	h := newHarness(t)
	owner, live := h.device("svc-rev-live")
	_, gone := h.device("svc-rev-gone")
	if err := h.db.Model(&model.UserDevice{}).Where("id = ?", gone).Update("user_id", owner).Error; err != nil {
		t.Fatal(err)
	}
	h.addEvent(owner, live, "cpu", KindFired, baseTS-minMS)
	h.addEvent(owner, gone, "cpu", KindFired, baseTS-minMS)
	if err := h.db.Model(&model.UserDevice{}).Where("id = ?", gone).Update("revoked_at", baseTS).Error; err != nil {
		t.Fatal(err)
	}
	got, err := h.service().List(context.Background(), owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Events) != 1 || got.Events[0].DeviceID != live {
		t.Fatalf("events = %+v", got.Events)
	}
}

// offlineStateAny reports whether the device has any persisted alert state row.
func (h *harness) offlineStateAny(deviceID string) bool {
	h.t.Helper()
	states, err := loadStates(context.Background(), h.db)
	if err != nil {
		h.t.Fatal(err)
	}
	for k := range states {
		if k.device == deviceID {
			return true
		}
	}
	return false
}

func listKinds(t *testing.T, h *harness, owner, dev string) map[string]int {
	t.Helper()
	got, err := h.service().List(context.Background(), owner)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, ev := range got.Events {
		if ev.DeviceID == dev {
			out[ev.Rule+":"+ev.Kind]++
		}
	}
	return out
}

func TestResolvedAcknowledgesFiredDatabase(t *testing.T) {
	t.Run("threshold rule", func(t *testing.T) {
		h := newHarness(t)
		owner, dev := h.device("ack-cpu")
		h.addEvent(owner, dev, "mem", KindFired, baseTS-minMS)
		e := h.evaluator()
		h.cpu(dev, baseTS-20*minMS, baseTS+20*minMS, 95)
		h.tick(e, baseTS+2*minMS)
		if got := listKinds(t, h, owner, dev); got["cpu:fired"] != 1 {
			t.Fatalf("after fire: %v", got)
		}
		// Restart, then the value drops and the alert resolves.
		h.cpu(dev, baseTS+21*minMS, baseTS+40*minMS, 10)
		e2 := h.evaluator()
		h.tick(e2, baseTS+40*minMS)
		h.tick(e2, baseTS+41*minMS)
		got := listKinds(t, h, owner, dev)
		if got["cpu:fired"] != 0 || got["cpu:resolved"] != 1 || got["mem:fired"] != 1 {
			t.Fatalf("after resolve: %v", got)
		}
	})
	t.Run("offline rule", func(t *testing.T) {
		h, e, p, owner, dev := offlineSetup(t, "ack-off")
		_, otherDev := h.device("ack-off-other")
		h.addEvent(owner, otherDev, "offline", KindFired, baseTS-minMS)
		h.tick(e, baseTS+15*minMS)
		h.tick(e, baseTS+20*minMS)
		if got := listKinds(t, h, owner, dev); got["offline:fired"] != 1 {
			t.Fatalf("after fire: %v", got)
		}
		p.set(dev, true)
		h.tick(e, baseTS+25*minMS)
		h.tick(e, baseTS+30*minMS)
		got := listKinds(t, h, owner, dev)
		if got["offline:fired"] != 0 || got["offline:resolved"] != 1 {
			t.Fatalf("after resolve: %v", got)
		}
		var n int64
		h.db.Model(&model.DeviceAlertEvent{}).Where("device_id = ? AND kind = ? AND acked_at IS NULL", otherDev, KindFired).Count(&n)
		if n != 1 {
			t.Fatalf("another device's fired was acknowledged")
		}
	})
	t.Run("muted resolve leaves other rules alone", func(t *testing.T) {
		h, e, p, owner, dev := offlineSetup(t, "ack-muted")
		h.addEvent(owner, dev, "cpu", KindFired, baseTS-minMS)
		h.settings(owner, dev, func(s *Settings) { s.ExpectedOffline = true })
		h.tick(e, baseTS+15*minMS)
		h.tick(e, baseTS+25*minMS)
		p.set(dev, true)
		h.tick(e, baseTS+26*minMS)
		h.tick(e, baseTS+32*minMS)
		got := listKinds(t, h, owner, dev)
		if got["offline:resolved"] != 1 || got["cpu:fired"] != 1 {
			t.Fatalf("%v", got)
		}
	})
}

func TestOfflineRestartBaselineDatabase(t *testing.T) {
	restart := func(h *harness, p *presenceSet, at int64) *Evaluator {
		h.clock.Store(at)
		return h.offlineEvaluator(p)
	}
	t.Run("suspect survives a restart and alerts once", func(t *testing.T) {
		h, e, p, _, dev := offlineSetup(t, "base-suspect")
		h.tick(e, baseTS+15*minMS)
		if h.status(dev) != StatusSuspect {
			t.Fatalf("status = %q", h.status(dev))
		}
		at := baseTS + 16*minMS
		e2 := restart(h, p, at)
		for _, m := range []int64{2, 3, 6} {
			h.tick(e2, at+m*minMS)
			if h.status(dev) != StatusSuspect || len(h.events(dev)) != 0 {
				t.Fatalf("+%dm: %q %+v", m, h.status(dev), h.events(dev))
			}
		}
		h.tick(e2, at+10*minMS)
		h.tick(e2, at+12*minMS)
		if evs := h.events(dev); len(evs) != 1 || evs[0].Kind != KindFired {
			t.Fatalf("events = %+v", evs)
		}
	})
	t.Run("device lost just before the downtime alerts after the restart", func(t *testing.T) {
		h, e, p, _, dev := offlineSetup(t, "base-just-lost")
		h.heard(dev, baseTS+18*minMS)
		h.tick(e, baseTS+20*minMS) // last tick of the first run
		at := baseTS + 8*60*minMS
		e2 := restart(h, p, at)
		h.tick(e2, at+2*minMS)
		for _, m := range []int64{5, 10, 12} {
			h.tick(e2, at+m*minMS)
		}
		if evs := h.events(dev); len(evs) != 1 || evs[0].Kind != KindFired {
			t.Fatalf("events = %+v status=%q", evs, h.status(dev))
		}
	})
	t.Run("device already gone during the previous run is dormant after a restart", func(t *testing.T) {
		h, e, p, owner, dev := offlineSetup(t, "base-gone")
		h.settings(owner, dev, func(s *Settings) { s.OfflineEnabled = ptr(false) })
		h.tick(e, baseTS+60*minMS) // rule off: no state row, but the tick is recorded
		h.settings(owner, dev, func(s *Settings) { s.OfflineEnabled = ptr(true) })
		at := baseTS + 2*60*minMS
		e2 := restart(h, p, at)
		for _, m := range []int64{2, 5, 15, 60} {
			h.tick(e2, at+m*minMS)
		}
		if h.status(dev) != StatusDormant || len(h.events(dev)) != 0 {
			t.Fatalf("%q %+v", h.status(dev), h.events(dev))
		}
	})
	t.Run("first deployment keeps long-gone devices dormant", func(t *testing.T) {
		h := newHarness(t)
		p := &presenceSet{}
		owner, dev := h.device("base-first")
		h.agentRow(owner, dev, ptr(baseTS-24*60*minMS))
		e := h.offlineEvaluator(p)
		h.tick(e, baseTS+2*minMS)
		if h.status(dev) != StatusDormant {
			t.Fatalf("status = %q", h.status(dev))
		}
	})
}
