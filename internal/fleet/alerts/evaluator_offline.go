package alerts

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
)

// offlineData is what one Tick reads once for every device's offline rule.
type offlineData struct {
	agents map[string]*model.DeviceAgent
}

func (e *Evaluator) loadOffline(ctx context.Context) (*offlineData, error) {
	rows, err := query.Use(e.db).DeviceAgent.WithContext(ctx).Find()
	if err != nil {
		return nil, fmt.Errorf("list device agents: %w", err)
	}
	d := &offlineData{agents: make(map[string]*model.DeviceAgent, len(rows))}
	for _, r := range rows {
		d.agents[r.DeviceID] = r
	}
	return d, nil
}

// lastHeard is now while the control channel is registered, else the newest sign of life. Only
// server-clock sources count: device-reported metric timestamps can be skewed. It runs inside Tick,
// so lastOnline needs no extra locking.
func (e *Evaluator) lastHeard(now int64, dev *model.UserDevice, off *offlineData) (int64, bool) {
	if e.presence != nil && e.presence(dev.ID) {
		e.lastOnline[dev.ID] = now
		return now, true
	}
	var heard int64
	found := false
	consider := func(ts *int64) {
		if ts != nil && (!found || *ts > heard) {
			heard, found = *ts, true
		}
	}
	consider(off.agents[dev.ID].LastSeenAt)
	consider(dev.LastSeenAt)
	if ts, ok := e.lastOnline[dev.ID]; ok {
		consider(&ts)
	}
	return heard, found
}

func (e *Evaluator) offlineChanges(now int64, dev *model.UserDevice, set *Settings, states map[stateKey]*model.DeviceAlertState, off *offlineData) []change {
	if off == nil || off.agents[dev.ID] == nil {
		return nil
	}
	enabled, minutes := ResolveOffline(dev.Kind, set)
	lastHeard, ok := e.lastHeard(now, dev, off)
	if !ok {
		return nil
	}
	suspectAfter := SuspectAfter(e.interval).Milliseconds()
	alertAfter := max(minutes*60_000, suspectAfter)
	muted := false
	var quietEnd int64
	if set != nil {
		muted = set.ExpectedOffline
		if set.QuietEnabled {
			loc := ResolveLocation(set.QuietTZ)
			muted = muted || InQuiet(e.now(), set.QuietStart, set.QuietEnd, loc)
			quietEnd = LastQuietEnd(e.now(), set.QuietStart, set.QuietEnd, loc)
		}
	}
	prev := State{Status: StatusOK}
	if old := states[stateKey{dev.ID, RuleOffline}]; old != nil {
		prev = State{Status: old.Status, Since: old.Since, FiredAt: old.FiredAt, ResolvedAt: old.ResolvedAt, RecoveringSince: old.RecoveringSince}
	}
	next, emit := StepOffline(prev, OfflineInput{
		Now: now, LastHeard: lastHeard, Effective: max(lastHeard, e.started), Baseline: e.baseline(), QuietEnd: quietEnd, G: GapTolerance(e.interval).Milliseconds(),
		Enabled: enabled, Muted: muted, S: suspectAfter, A: alertAfter,
	})
	if emit == nil && sameState(prev, next) {
		return nil
	}
	c := change{state: model.DeviceAlertState{
		DeviceID: dev.ID, RuleKey: string(RuleOffline), OwnerID: dev.UserID, Status: next.Status,
		Since: next.Since, FiredAt: next.FiredAt, ResolvedAt: next.ResolvedAt, RecoveringSince: next.RecoveringSince, UpdatedAt: now,
	}}
	if emit != nil {
		c.event, c.audit = buildOfflineRecords(now, dev, emit)
	}
	return []change{c}
}

func buildOfflineRecords(now int64, dev *model.UserDevice, emit *OfflineEmit) (*model.DeviceAlertEvent, *model.AuditRow) {
	ev := &model.DeviceAlertEvent{
		ID: ids.New(), OwnerID: dev.UserID, DeviceID: dev.ID, RuleKey: string(RuleOffline), Kind: emit.Kind,
		TS: now, DurationMS: emit.DurationMS, Muted: emit.Muted,
	}
	d := emit.DurationMS
	p := auditPayload{Action: "alert_" + emit.Kind, DeviceID: dev.ID, Rule: string(RuleOffline), DurationMS: &d}
	if emit.Muted {
		p.Muted = &emit.Muted
	}
	kind := auditKindFired
	if emit.Kind == KindResolved {
		kind = auditKindResolved
	}
	body, _ := json.Marshal(p)
	assetID := dev.ID
	return ev, &model.AuditRow{TS: now, AssetID: &assetID, Source: auditSource, Kind: kind, PayloadJSON: string(body), DurationMS: &d, OwnerID: dev.UserID}
}

// baseline is the previous run's last evaluation time, or the start time on a first deployment.
func (e *Evaluator) baseline() int64 {
	if e.prevLastTick > 0 {
		return e.prevLastTick
	}
	return e.started
}
