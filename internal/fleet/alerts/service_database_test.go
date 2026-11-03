package alerts

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
)

func (h *harness) service() *Service {
	h.t.Helper()
	return NewService(h.db, 0, func() time.Time { return time.UnixMilli(h.clock.Load()) })
}

func (h *harness) addEvent(owner, dev, rule, kind string, ts int64) string {
	h.t.Helper()
	ev := &model.DeviceAlertEvent{ID: ids.New(), OwnerID: owner, DeviceID: dev, RuleKey: rule, Kind: kind, TS: ts}
	if err := h.db.Create(ev).Error; err != nil {
		h.t.Fatal(err)
	}
	return ev.ID
}

func (h *harness) addState(owner, dev, rule, status string, since int64, v *float64) {
	h.t.Helper()
	st := &model.DeviceAlertState{DeviceID: dev, RuleKey: rule, OwnerID: owner, Status: status, Since: since, LastValue: v, UpdatedAt: since}
	if err := h.db.Create(st).Error; err != nil {
		h.t.Fatal(err)
	}
}

func TestServiceListIsolationAndWindowDatabase(t *testing.T) {
	h := newHarness(t)
	ownerA, devA := h.device("svc-list-a")
	ownerB, devB := h.device("svc-list-b")
	svc := h.service()
	now := baseTS
	day := 24 * 60 * minMS

	fresh := h.addEvent(ownerA, devA, "cpu", KindFired, now-minMS)
	older := h.addEvent(ownerA, devA, "disk", KindFired, now-3*day)
	h.addEvent(ownerA, devA, "mem", KindFired, now-8*day) // outside the 7-day window
	h.addEvent(ownerB, devB, "cpu", KindFired, now-minMS)
	v := 93.5
	h.addState(ownerA, devA, "cpu", StatusFiring, now-5*minMS, &v)
	h.addState(ownerA, devA, "mem", StatusSuspect, now-5*minMS, nil)
	h.addState(ownerA, devA, "disk", StatusOK, now-5*minMS, nil)
	h.addState(ownerB, devB, "cpu", StatusFiring, now-5*minMS, nil)

	got, err := svc.List(context.Background(), ownerA)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Active) != 1 || got.Active[0].DeviceID != devA || got.Active[0].Rule != "cpu" || got.Active[0].Status != StatusFiring ||
		got.Active[0].Since != now-5*minMS || got.Active[0].Value == nil || *got.Active[0].Value != 93.5 {
		t.Fatalf("active = %+v", got.Active)
	}
	if len(got.Events) != 2 || got.Events[0].ID != fresh || got.Events[1].ID != older {
		t.Fatalf("events = %+v", got.Events)
	}

	empty, err := svc.List(context.Background(), "nobody")
	if err != nil {
		t.Fatal(err)
	}
	if empty.Active == nil || empty.Events == nil || len(empty.Active) != 0 || len(empty.Events) != 0 {
		t.Fatalf("empty lists must be non-nil: %+v", empty)
	}

	// Revoked devices drop out of the active list.
	if err := h.db.Model(&model.UserDevice{}).Where("id = ?", devA).Update("revoked_at", now).Error; err != nil {
		t.Fatal(err)
	}
	got, err = svc.List(context.Background(), ownerA)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Active) != 0 {
		t.Fatalf("revoked device still active: %+v", got.Active)
	}
}

func TestServiceListLimitDatabase(t *testing.T) {
	h := newHarness(t)
	owner, dev := h.device("svc-limit")
	for i := 0; i < MaxListedEvents+5; i++ {
		h.addEvent(owner, dev, "cpu", KindFired, baseTS-int64(i)*1000)
	}
	got, err := h.service().List(context.Background(), owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Events) != MaxListedEvents {
		t.Fatalf("events = %d", len(got.Events))
	}
	for i := 1; i < len(got.Events); i++ {
		if got.Events[i].TS > got.Events[i-1].TS {
			t.Fatalf("events not sorted by ts desc at %d", i)
		}
	}
}

func TestServiceAckOwnRowsOnlyDatabase(t *testing.T) {
	h := newHarness(t)
	ownerA, devA := h.device("svc-ack-a")
	ownerB, devB := h.device("svc-ack-b")
	svc := h.service()
	a1 := h.addEvent(ownerA, devA, "cpu", KindFired, baseTS-minMS)
	a2 := h.addEvent(ownerA, devA, "mem", KindFired, baseTS-2*minMS)
	b1 := h.addEvent(ownerB, devB, "cpu", KindFired, baseTS-minMS)

	n, err := svc.Ack(context.Background(), ownerA, []string{a1, b1, "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("acked = %d, want 1", n)
	}
	gotA, _ := svc.List(context.Background(), ownerA)
	if len(gotA.Events) != 1 || gotA.Events[0].ID != a2 {
		t.Fatalf("A events = %+v", gotA.Events)
	}
	gotB, _ := svc.List(context.Background(), ownerB)
	if len(gotB.Events) != 1 || gotB.Events[0].ID != b1 {
		t.Fatalf("B's event must be untouched: %+v", gotB.Events)
	}
	if n, _ := svc.Ack(context.Background(), ownerA, []string{a1}); n != 0 {
		t.Fatalf("second ack changed %d rows", n)
	}

	if _, err := svc.Ack(context.Background(), ownerA, nil); err == nil {
		t.Fatal("empty ids accepted")
	}
	tooMany := make([]string, MaxAckIDs+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("id-%d", i)
	}
	if _, err := svc.Ack(context.Background(), ownerA, tooMany); err == nil {
		t.Fatal("501 ids accepted")
	}
}

func TestServiceGetSettingsDefaultsDatabase(t *testing.T) {
	h := newHarness(t)
	_, dev := h.device("svc-defaults")
	svc := h.service()
	got, err := svc.GetSettings(context.Background(), dev, "agent")
	if err != nil {
		t.Fatal(err)
	}
	want := EffectiveSettings{
		DeviceID: dev, Kind: "agent", DiskEnabled: true, DiskThreshold: 90, MemEnabled: true, MemThreshold: 90,
		CPUEnabled: true, CPUThreshold: 90, OfflineEnabled: true, OfflineMinutes: 10,
		ServerTZ: time.Local.String(), SuspectAfterMinutes: 5,
	}
	if *got != want {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	helper, err := svc.GetSettings(context.Background(), dev, "local-helper")
	if err != nil {
		t.Fatal(err)
	}
	if helper.OfflineEnabled || helper.Kind != "local-helper" {
		t.Fatalf("local-helper must default to offline off: %+v", helper)
	}
}

func validInput() SettingsInput {
	b := func(v bool) *bool { return &v }
	f := func(v float64) *float64 { return &v }
	i := func(v int64) *int64 { return &v }
	return SettingsInput{
		DiskEnabled: b(true), DiskThreshold: f(80), MemEnabled: b(false), MemThreshold: f(70),
		CPUEnabled: b(true), CPUThreshold: f(60.5), OfflineEnabled: b(true), OfflineMinutes: i(30),
		ExpectedOffline: b(true), QuietEnabled: b(true), QuietStart: i(1140), QuietEnd: i(540), QuietTZ: "Asia/Shanghai",
	}
}

func TestServicePutSettingsRoundTripDatabase(t *testing.T) {
	h := newHarness(t)
	owner, dev := h.device("svc-put")
	svc := h.service()
	got, err := svc.PutSettings(context.Background(), owner, dev, "agent", validInput())
	if err != nil {
		t.Fatal(err)
	}
	if got.DiskThreshold != 80 || got.MemEnabled || got.CPUThreshold != 60.5 || got.OfflineMinutes != 30 || !got.ExpectedOffline ||
		!got.QuietEnabled || got.QuietStart != 1140 || got.QuietEnd != 540 || got.QuietTZ != "Asia/Shanghai" {
		t.Fatalf("put result = %+v", got)
	}
	again, err := svc.GetSettings(context.Background(), dev, "agent")
	if err != nil {
		t.Fatal(err)
	}
	if *again != *got {
		t.Fatalf("get after put = %+v, want %+v", again, got)
	}
	// Whole-row replacement.
	in := validInput()
	in.QuietEnabled = ptr(false)
	in.ExpectedOffline = ptr(false)
	got, err = svc.PutSettings(context.Background(), owner, dev, "agent", in)
	if err != nil {
		t.Fatal(err)
	}
	if got.QuietEnabled || got.ExpectedOffline {
		t.Fatalf("second put = %+v", got)
	}
}

func TestServicePutSettingsValidationDatabase(t *testing.T) {
	h := newHarness(t)
	owner, dev := h.device("svc-validate")
	svc := h.service()
	cases := []struct {
		name string
		edit func(*SettingsInput)
		ok   bool
	}{
		{"threshold 0", func(i *SettingsInput) { i.DiskThreshold = ptr(0.0) }, false},
		{"threshold 1", func(i *SettingsInput) { i.DiskThreshold = ptr(1.0) }, true},
		{"threshold 100", func(i *SettingsInput) { i.MemThreshold = ptr(100.0) }, true},
		{"threshold 100.5", func(i *SettingsInput) { i.CPUThreshold = ptr(100.5) }, false},
		{"offline 0", func(i *SettingsInput) { i.OfflineMinutes = ptr(int64(0)) }, false},
		{"offline 1440", func(i *SettingsInput) { i.OfflineMinutes = ptr(int64(1440)) }, true},
		{"offline 1441", func(i *SettingsInput) { i.OfflineMinutes = ptr(int64(1441)) }, false},
		{"quiet equal enabled", func(i *SettingsInput) { i.QuietStart, i.QuietEnd = ptr(int64(60)), ptr(int64(60)) }, false},
		{"quiet equal disabled", func(i *SettingsInput) {
			i.QuietEnabled, i.QuietStart, i.QuietEnd = ptr(false), ptr(int64(0)), ptr(int64(0))
		}, true},
		{"quiet out of range", func(i *SettingsInput) { i.QuietEnd = ptr(int64(1440)) }, false},
		{"bad tz", func(i *SettingsInput) { i.QuietTZ = "Mars/Base" }, false},
		{"empty tz", func(i *SettingsInput) { i.QuietTZ = "" }, true},
		{"missing field", func(i *SettingsInput) { i.CPUEnabled = nil }, false},
	}
	for _, tc := range cases {
		in := validInput()
		tc.edit(&in)
		_, err := svc.PutSettings(context.Background(), owner, dev, "agent", in)
		if (err == nil) != tc.ok {
			t.Errorf("%s: err = %v, want ok=%v", tc.name, err, tc.ok)
		}
	}
}

func TestServicePutSettingsDrivesEvaluatorDatabase(t *testing.T) {
	h := newHarness(t)
	owner, dev := h.device("svc-evaluator")
	e := h.evaluator()
	h.cpu(dev, baseTS-20*minMS, baseTS+3*minMS, 70)
	h.tick(e, baseTS+2*minMS)
	if n := len(h.events(dev)); n != 0 {
		t.Fatalf("fired under the default threshold: %d", n)
	}
	in := validInput()
	in.CPUThreshold = ptr(50.0)
	if _, err := h.service().PutSettings(context.Background(), owner, dev, "agent", in); err != nil {
		t.Fatal(err)
	}
	h.tick(e, baseTS+3*minMS)
	evs := h.events(dev)
	if len(evs) != 1 || evs[0].RuleKey != string(RuleCPU) || evs[0].Threshold == nil || *evs[0].Threshold != 50 {
		t.Fatalf("events = %+v", evs)
	}
}
