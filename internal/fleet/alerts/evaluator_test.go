package alerts

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
)

const baseTS = int64(1_800_000_000_000)

type harness struct {
	t        *testing.T
	db       *gorm.DB
	accounts *account.Accounts
	clock    atomic.Int64
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t}
	h.db = dbtest.NewFixture(t).OpenStore(t).GORM()
	h.accounts = account.New(h.db)
	h.clock.Store(baseTS)
	return h
}

func (h *harness) evaluator() *Evaluator {
	h.t.Helper()
	e, err := NewEvaluator(Config{DB: h.db, Now: func() time.Time { return time.UnixMilli(h.clock.Load()) }})
	if err != nil {
		h.t.Fatal(err)
	}
	return e
}

func (h *harness) device(owner string) (userID, deviceID string) {
	h.t.Helper()
	u, err := h.accounts.CreateUser(context.Background(), owner, "", "pw-123456")
	if err != nil {
		h.t.Fatal(err)
	}
	d, err := h.accounts.RegisterDevice(context.Background(), u.ID, owner+"-box", "agent")
	if err != nil {
		h.t.Fatal(err)
	}
	return u.ID, d.ID
}

// cpu inserts one cpu sample per minute from from to to (inclusive).
func (h *harness) cpu(deviceID string, from, to int64, v float64) {
	h.t.Helper()
	for ts := from; ts <= to; ts += minMS {
		val := v
		row := &model.DeviceMetrics{ID: ids.New(), DeviceID: deviceID, TS: ts, CPUPct: &val}
		if err := query.Use(h.db).DeviceMetrics.WithContext(context.Background()).Create(row); err != nil {
			h.t.Fatal(err)
		}
	}
}

func (h *harness) tick(e *Evaluator, at int64) {
	h.t.Helper()
	h.clock.Store(at)
	if err := e.Tick(context.Background()); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) events(deviceID string) []model.DeviceAlertEvent {
	h.t.Helper()
	var out []model.DeviceAlertEvent
	if err := h.db.Model(&model.DeviceAlertEvent{}).Where("device_id = ?", deviceID).Find(&out).Error; err != nil {
		h.t.Fatal(err)
	}
	slices.SortFunc(out, func(a, b model.DeviceAlertEvent) int { return int(a.TS - b.TS) })
	return out
}

func (h *harness) audits(kind string) []model.AuditRow {
	h.t.Helper()
	a := query.Use(h.db).AuditRow
	rows, err := a.WithContext(context.Background()).Where(a.Source.Eq("fleet"), a.Kind.Eq(kind)).Find()
	if err != nil {
		h.t.Fatal(err)
	}
	out := make([]model.AuditRow, len(rows))
	for i, r := range rows {
		out[i] = *r
	}
	slices.SortFunc(out, func(a, b model.AuditRow) int { return int(a.TS - b.TS) })
	return out
}

func TestEvaluatorWarmupAndFireDatabase(t *testing.T) {
	h := newHarness(t)
	owner, dev := h.device("alerts-fire")
	e := h.evaluator() // started at baseTS
	h.cpu(dev, baseTS-20*minMS, baseTS+3*minMS, 95)

	h.tick(e, baseTS)
	h.tick(e, baseTS+2*minMS-1)
	if n := len(h.events(dev)); n != 0 {
		t.Fatalf("evaluated during warmup: %d events", n)
	}
	h.tick(e, baseTS+2*minMS)
	evs := h.events(dev)
	if len(evs) != 1 || evs[0].Kind != KindFired || evs[0].RuleKey != string(RuleCPU) || evs[0].OwnerID != owner || evs[0].TS != baseTS+2*minMS {
		t.Fatalf("events = %+v", evs)
	}
	if evs[0].Threshold == nil || *evs[0].Threshold != 90 || evs[0].Value == nil || *evs[0].Value != 95 {
		t.Fatalf("event values = %+v", evs[0])
	}
	h.tick(e, baseTS+3*minMS)
	if n := len(h.events(dev)); n != 1 {
		t.Fatalf("repeat fire: %d events", n)
	}
	aud := h.audits("device_alert_fired")
	if len(aud) != 1 || aud[0].OwnerID != owner || aud[0].AssetID == nil || *aud[0].AssetID != dev {
		t.Fatalf("audit = %+v", aud)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(aud[0].PayloadJSON), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["action"] != "alert_fired" || payload["device_id"] != dev || payload["rule"] != "cpu" {
		t.Fatalf("payload = %v", payload)
	}
}

func TestEvaluatorRestartKeepsStateDatabase(t *testing.T) {
	h := newHarness(t)
	_, dev := h.device("alerts-restart")
	e1 := h.evaluator()
	h.cpu(dev, baseTS-20*minMS, baseTS+2*minMS, 95)
	h.tick(e1, baseTS+2*minMS)
	if n := len(h.events(dev)); n != 1 {
		t.Fatalf("events = %d", n)
	}

	// New process: new start time, same database.
	restart := baseTS + 10*minMS
	h.clock.Store(restart)
	e2 := h.evaluator()
	h.cpu(dev, baseTS+3*minMS, restart+3*minMS, 95)
	h.tick(e2, restart+2*minMS)
	if n := len(h.events(dev)); n != 1 {
		t.Fatalf("fired again after restart: %d events", n)
	}

	// Recover: low values for 6 minutes, then the restarted evaluator resolves exactly once.
	h.cpu(dev, restart+4*minMS, restart+10*minMS, 20)
	at := restart + 10*minMS
	h.tick(e2, at)
	h.tick(e2, at+minMS)
	evs := h.events(dev)
	if len(evs) != 2 || evs[1].Kind != KindResolved || evs[1].DurationMS != at-(baseTS+2*minMS) {
		t.Fatalf("events = %+v", evs)
	}
	if n := len(h.audits("device_alert_resolved")); n != 1 {
		t.Fatalf("resolved audits = %d", n)
	}
}

func TestEvaluatorStaleDataKeepsFiringDatabase(t *testing.T) {
	h := newHarness(t)
	_, dev := h.device("alerts-stale")
	e := h.evaluator()
	h.cpu(dev, baseTS-20*minMS, baseTS+2*minMS, 95)
	h.tick(e, baseTS+2*minMS)
	h.tick(e, baseTS+60*minMS) // no fresh samples: neither fires nor resolves
	if evs := h.events(dev); len(evs) != 1 {
		t.Fatalf("events = %+v", evs)
	}
}

func TestEvaluatorSettingsOverrideDatabase(t *testing.T) {
	h := newHarness(t)
	owner, dev := h.device("alerts-override")
	e := h.evaluator()
	h.cpu(dev, baseTS-20*minMS, baseTS+2*minMS, 60)
	h.tick(e, baseTS+2*minMS)
	if n := len(h.events(dev)); n != 0 {
		t.Fatalf("fired at default threshold: %d", n)
	}
	thr := 50.0
	if err := putSettings(context.Background(), h.db, &Settings{DeviceID: dev, OwnerID: owner, CPUThreshold: &thr, UpdatedAt: baseTS}); err != nil {
		t.Fatal(err)
	}
	h.tick(e, baseTS+2*minMS)
	if evs := h.events(dev); len(evs) != 1 || evs[0].Threshold == nil || *evs[0].Threshold != 50 {
		t.Fatalf("events = %+v", evs)
	}
	off := false
	if err := putSettings(context.Background(), h.db, &Settings{DeviceID: dev, OwnerID: owner, CPUEnabled: &off, UpdatedAt: baseTS + 1}); err != nil {
		t.Fatal(err)
	}
	h.tick(e, baseTS+3*minMS)
	if evs := h.events(dev); len(evs) != 1 {
		t.Fatalf("disable emitted events: %+v", evs)
	}
	states, err := loadStates(context.Background(), h.db)
	if err != nil {
		t.Fatal(err)
	}
	if st := states[stateKey{dev, RuleCPU}]; st.Status != StatusOK {
		t.Fatalf("state after disable = %+v", st)
	}
}

func TestEvaluatorRevokedDeviceDatabase(t *testing.T) {
	h := newHarness(t)
	_, dev := h.device("alerts-revoked")
	e := h.evaluator()
	h.cpu(dev, baseTS-20*minMS, baseTS+2*minMS, 95)
	h.tick(e, baseTS+2*minMS)
	ud := query.Use(h.db).UserDevice
	if _, err := ud.WithContext(context.Background()).Where(ud.ID.Eq(dev)).Update(ud.RevokedAt, baseTS); err != nil {
		t.Fatal(err)
	}
	h.tick(e, baseTS+3*minMS)
	states, err := loadStates(context.Background(), h.db)
	if err != nil {
		t.Fatal(err)
	}
	for k := range states {
		if k.device == dev {
			t.Fatalf("state survived revoke: %+v", states)
		}
	}
	if evs := h.events(dev); len(evs) != 1 {
		t.Fatalf("revoke emitted events: %+v", evs)
	}
}

func TestEvaluatorMultiUserOwnersDatabase(t *testing.T) {
	h := newHarness(t)
	ownerA, devA := h.device("alerts-multi-a")
	ownerB, devB := h.device("alerts-multi-b")
	e := h.evaluator()
	h.cpu(devA, baseTS-20*minMS, baseTS+2*minMS, 95)
	h.cpu(devB, baseTS-20*minMS, baseTS+2*minMS, 99)
	h.tick(e, baseTS+2*minMS)
	for dev, owner := range map[string]string{devA: ownerA, devB: ownerB} {
		evs := h.events(dev)
		if len(evs) != 1 || evs[0].OwnerID != owner {
			t.Fatalf("device %s events = %+v", dev, evs)
		}
	}
	got := map[string]string{}
	for _, a := range h.audits("device_alert_fired") {
		got[*a.AssetID] = a.OwnerID
	}
	if len(got) != 2 || got[devA] != ownerA || got[devB] != ownerB {
		t.Fatalf("audit owners = %v", got)
	}
}

func TestEvaluatorRetentionDatabase(t *testing.T) {
	h := newHarness(t)
	owner, dev := h.device("alerts-retention")
	e := h.evaluator()
	now := baseTS + 2*minMS
	for _, ts := range []int64{now - 31*24*60*minMS, now - 29*24*60*minMS} {
		ev := &model.DeviceAlertEvent{ID: ids.New(), OwnerID: owner, DeviceID: dev, RuleKey: "cpu", Kind: KindFired, TS: ts}
		if err := h.db.Create(ev).Error; err != nil {
			t.Fatal(err)
		}
	}
	h.tick(e, now)
	evs := h.events(dev)
	if len(evs) != 1 || evs[0].TS != now-29*24*60*minMS {
		t.Fatalf("events = %+v", evs)
	}
}

func TestEvaluatorConcurrentSettingsDatabase(t *testing.T) {
	h := newHarness(t)
	owner, dev := h.device("alerts-race")
	e := h.evaluator()
	h.cpu(dev, baseTS-20*minMS, baseTS+2*minMS, 95)
	h.clock.Store(baseTS + 2*minMS)

	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for g := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 3 {
				if err := e.Tick(context.Background()); err != nil {
					errs <- fmt.Errorf("tick %d: %w", g, err)
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range 5 {
			thr := float64(80 + i)
			if err := putSettings(context.Background(), h.db, &Settings{DeviceID: dev, OwnerID: owner, CPUThreshold: &thr, UpdatedAt: int64(i)}); err != nil {
				errs <- err
				return
			}
			if _, err := loadSettings(context.Background(), h.db); err != nil {
				errs <- err
				return
			}
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if n := len(h.events(dev)); n != 1 {
		t.Fatalf("events = %d, want exactly one fired", n)
	}
}

func TestEvaluatorRunnerDatabase(t *testing.T) {
	h := newHarness(t)
	called := make(chan time.Duration)
	release := make(chan time.Time)
	e, err := NewEvaluator(Config{
		DB: h.db, Interval: 7 * time.Second,
		Now: func() time.Time { return time.UnixMilli(h.clock.Load()) },
	})
	if err != nil {
		t.Fatal(err)
	}
	e.after = func(d time.Duration) <-chan time.Time {
		called <- d
		return release
	}
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.Start(context.Background()); err == nil {
		t.Fatal("second Start succeeded")
	}
	if d := <-called; d != 7*time.Second {
		t.Fatalf("wait = %v", d)
	}
	release <- time.Time{}
	<-called
	if err := e.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}
