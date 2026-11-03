package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
	"github.com/Hello-CTF/NexTerm/internal/store/query"

	// Quiet-hours zone names must load on hosts without system zoneinfo.
	_ "time/tzdata"
)

const (
	auditSource        = "fleet"
	auditKindFired     = "device_alert_fired"
	auditKindResolved  = "device_alert_resolved"
	attemptTimeout     = 5 * time.Minute
	cleanupEvery       = time.Hour
	warmupIntervals    = 2
	maxThresholdWindow = 15 * time.Minute
)

// Config wires an Evaluator.
type Config struct {
	DB       *gorm.DB
	Interval time.Duration // sync interval I; default 60s
	Now      func() time.Time
	// Presence reports whether the device's control channel is registered; used by the offline rule.
	Presence func(deviceID string) bool
	Logger   *slog.Logger
}

// Evaluator runs alert rules over all devices. Per device, state changes, events and audit rows are
// written in one transaction.
type Evaluator struct {
	db       *gorm.DB
	interval time.Duration
	now      func() time.Time
	presence func(deviceID string) bool
	logger   *slog.Logger
	after    func(time.Duration) <-chan time.Time
	started  int64

	tickMu      sync.Mutex
	lastCleanup int64
	// lastOnline is the last time Presence reported each device; only touched inside Tick.
	lastOnline map[string]int64

	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	running bool
}

func NewEvaluator(cfg Config) (*Evaluator, error) {
	if cfg.DB == nil {
		return nil, errors.New("alerts: database is required")
	}
	e := &Evaluator{
		db: cfg.DB, interval: cfg.Interval, now: cfg.Now, presence: cfg.Presence, logger: cfg.Logger,
		after: time.After, lastOnline: map[string]int64{},
	}
	if e.interval <= 0 {
		e.interval = DefaultInterval
	}
	if e.now == nil {
		e.now = time.Now
	}
	if e.logger == nil {
		e.logger = slog.Default()
	}
	e.started = e.now().UnixMilli()
	return e, nil
}

type change struct {
	state model.DeviceAlertState
	event *model.DeviceAlertEvent
	audit *model.AuditRow
}

// Tick runs one evaluation pass at the injected clock's current time.
func (e *Evaluator) Tick(ctx context.Context) error {
	e.tickMu.Lock()
	defer e.tickMu.Unlock()
	now := e.now().UnixMilli()
	if now < e.started+warmupIntervals*e.interval.Milliseconds() {
		return nil
	}
	if err := deleteRevokedStates(ctx, e.db); err != nil {
		return err
	}
	ud := query.Use(e.db).UserDevice
	devices, err := ud.WithContext(ctx).Where(ud.RevokedAt.IsNull()).Find()
	if err != nil {
		return fmt.Errorf("list alert devices: %w", err)
	}
	settings, err := loadSettings(ctx, e.db)
	if err != nil {
		return err
	}
	states, err := loadStates(ctx, e.db)
	if err != nil {
		return err
	}
	off, err := e.loadOffline(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, dev := range devices {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := e.evaluateDevice(ctx, now, dev, settings[dev.ID], states, off); err != nil {
			errs = append(errs, fmt.Errorf("device %s: %w", dev.ID, err))
		}
	}
	if now >= e.lastCleanup+cleanupEvery.Milliseconds() {
		if err := deleteEventsBefore(ctx, e.db, now-EventRetention.Milliseconds()); err != nil {
			errs = append(errs, err)
		} else {
			e.lastCleanup = now
		}
	}
	return errors.Join(errs...)
}

// evaluateDevice runs every rule for one device and commits their changes in a single transaction.
func (e *Evaluator) evaluateDevice(ctx context.Context, now int64, dev *model.UserDevice, set *Settings, states map[stateKey]*model.DeviceAlertState, off *offlineData) error {
	changes, err := e.thresholdChanges(ctx, now, dev, set, states)
	if err != nil {
		return err
	}
	changes = append(changes, e.offlineChanges(now, dev, set, states, off)...)
	if len(changes) == 0 {
		return nil
	}
	err = e.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for i := range changes {
			c := &changes[i]
			if err := upsertState(ctx, tx, &c.state); err != nil {
				return err
			}
			if c.event != nil {
				if err := insertEvent(ctx, tx, c.event); err != nil {
					return err
				}
			}
			if c.audit != nil {
				if err := query.Use(tx).AuditRow.WithContext(ctx).Create(c.audit); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for i := range changes {
		s := changes[i].state
		states[stateKey{s.DeviceID, Rule(s.RuleKey)}] = &s
	}
	return nil
}

func (e *Evaluator) thresholdChanges(ctx context.Context, now int64, dev *model.UserDevice, set *Settings, states map[stateKey]*model.DeviceAlertState) ([]change, error) {
	gap := GapTolerance(e.interval)
	from := now - (maxThresholdWindow + gap).Milliseconds()
	m := query.Use(e.db).DeviceMetrics
	rows, err := m.WithContext(ctx).
		Where(m.DeviceID.Eq(dev.ID), m.TS.Gte(from), m.TS.Lte(now)).
		Order(m.TS.Asc()).Find()
	if err != nil {
		return nil, fmt.Errorf("read metrics: %w", err)
	}
	var out []change
	for _, rule := range ThresholdRules {
		enabled, threshold := ResolveThreshold(rule, set)
		var samples []Sample
		for _, r := range rows {
			if v, ok := metricValue(r, rule); ok {
				samples = append(samples, Sample{TS: r.TS, Value: v})
			}
		}
		verdict, value := Classify(samples, now, threshold, rule.Sustain(), gap)
		prev := State{Status: StatusOK}
		if old := states[stateKey{dev.ID, rule}]; old != nil {
			prev = State{Status: old.Status, Since: old.Since, FiredAt: old.FiredAt, ResolvedAt: old.ResolvedAt, LastValue: old.LastValue}
		}
		next, emit := StepThreshold(prev, StepInput{Now: now, Enabled: enabled, Verdict: verdict, Value: value, Threshold: threshold})
		if emit == nil && sameState(prev, next) {
			continue
		}
		c := change{state: model.DeviceAlertState{
			DeviceID: dev.ID, RuleKey: string(rule), OwnerID: dev.UserID, Status: next.Status,
			Since: next.Since, FiredAt: next.FiredAt, ResolvedAt: next.ResolvedAt, LastValue: next.LastValue, UpdatedAt: now,
		}}
		if emit != nil {
			c.event, c.audit = buildRecords(now, dev, rule, emit)
		}
		out = append(out, c)
	}
	return out, nil
}

func sameState(a, b State) bool {
	if a.Status != b.Status || a.Since != b.Since || a.FiredAt != b.FiredAt || a.ResolvedAt != b.ResolvedAt {
		return false
	}
	if (a.LastValue == nil) != (b.LastValue == nil) {
		return false
	}
	return a.LastValue == nil || *a.LastValue == *b.LastValue
}

type auditPayload struct {
	Action     string   `json:"action"`
	DeviceID   string   `json:"device_id"`
	Rule       string   `json:"rule"`
	Value      *float64 `json:"value,omitempty"`
	Threshold  *float64 `json:"threshold,omitempty"`
	DurationMS *int64   `json:"duration_ms,omitempty"`
	Muted      *bool    `json:"muted,omitempty"`
}

func buildRecords(now int64, dev *model.UserDevice, rule Rule, emit *Emit) (*model.DeviceAlertEvent, *model.AuditRow) {
	value, threshold := emit.Value, emit.Threshold
	ev := &model.DeviceAlertEvent{
		ID: ids.New(), OwnerID: dev.UserID, DeviceID: dev.ID, RuleKey: string(rule), Kind: emit.Kind, TS: now,
		Value: &value, Threshold: &threshold, DurationMS: emit.DurationMS,
	}
	p := auditPayload{Action: "alert_" + emit.Kind, DeviceID: dev.ID, Rule: string(rule), Value: &value, Threshold: &threshold}
	kind := auditKindFired
	var durationMS *int64
	if emit.Kind == KindResolved {
		kind = auditKindResolved
		d := emit.DurationMS
		p.DurationMS, durationMS = &d, &d
	}
	body, _ := json.Marshal(p)
	assetID := dev.ID
	return ev, &model.AuditRow{TS: now, AssetID: &assetID, Source: auditSource, Kind: kind, PayloadJSON: string(body), DurationMS: durationMS, OwnerID: dev.UserID}
}

// Start runs Tick every Interval until Shutdown or ctx ends.
func (e *Evaluator) Start(ctx context.Context) error {
	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		return errors.New("alert evaluator has already started")
	}
	e.running = true
	runCtx, cancel := context.WithCancel(ctx)
	e.cancel = cancel
	e.done = make(chan struct{})
	done := e.done
	e.mu.Unlock()
	go func() {
		defer close(done)
		for {
			e.attempt(runCtx)
			select {
			case <-runCtx.Done():
				return
			case <-e.after(e.interval):
			}
		}
	}()
	return nil
}

func (e *Evaluator) attempt(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
	defer cancel()
	if err := e.Tick(attemptCtx); err != nil && !errors.Is(err, context.Canceled) {
		e.logger.Warn("device alert evaluation failed", "error", err)
	}
}

// Shutdown stops the loop and waits for it to exit.
func (e *Evaluator) Shutdown(ctx context.Context) error {
	e.mu.Lock()
	cancel, done := e.cancel, e.done
	e.cancel, e.done = nil, nil
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
