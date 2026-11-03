package alerts

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
)

const (
	// EventListWindow bounds how far back List looks.
	EventListWindow = 7 * 24 * time.Hour
	// MaxListedEvents caps the events List returns.
	MaxListedEvents = 200
	// MaxAckIDs caps one Ack request.
	MaxAckIDs = 500
)

// Service is the owner-scoped read and settings API over the alert tables. It does not authorize
// devices: callers pass an owner they already authenticated and a device they already authorized.
type Service struct {
	db       *gorm.DB
	interval time.Duration
	now      func() time.Time
}

// NewService builds a Service; a non-positive interval means DefaultInterval and a nil clock time.Now.
func NewService(db *gorm.DB, interval time.Duration, now func() time.Time) *Service {
	if interval <= 0 {
		interval = DefaultInterval
	}
	if now == nil {
		now = time.Now
	}
	return &Service{db: db, interval: interval, now: now}
}

// ActiveAlert is a firing alert.
type ActiveAlert struct {
	DeviceID string   `json:"device_id"`
	Rule     string   `json:"rule"`
	Status   string   `json:"status"`
	Since    int64    `json:"since"`
	Value    *float64 `json:"value,omitempty"`
}

// EventView is one fired or resolved event.
type EventView struct {
	ID         string   `json:"id"`
	DeviceID   string   `json:"device_id"`
	Rule       string   `json:"rule"`
	Kind       string   `json:"kind"`
	TS         int64    `json:"ts"`
	Value      *float64 `json:"value,omitempty"`
	Threshold  *float64 `json:"threshold,omitempty"`
	DurationMS int64    `json:"duration_ms"`
	Muted      bool     `json:"muted"`
}

// Overview is the List result; both slices are never nil.
type Overview struct {
	Active []ActiveAlert `json:"active"`
	Events []EventView   `json:"events"`
}

// List returns the owner's firing alerts and recent unacknowledged events.
// GEN-TODO: DeviceAlertState / DeviceAlertEvent owner queries.
func (s *Service) List(ctx context.Context, ownerID string) (*Overview, error) {
	out := &Overview{Active: []ActiveAlert{}, Events: []EventView{}}
	var states []model.DeviceAlertState
	live := s.db.Table("user_device").Select("id").Where("revoked_at IS NULL")
	err := s.db.WithContext(ctx).Model(&model.DeviceAlertState{}).
		Where("owner_id = ? AND status = ? AND device_id IN (?)", ownerID, StatusFiring, live).
		Order("device_id ASC, rule_key ASC").Find(&states).Error
	if err != nil {
		return nil, fmt.Errorf("list active alerts: %w", err)
	}
	for _, st := range states {
		out.Active = append(out.Active, ActiveAlert{DeviceID: st.DeviceID, Rule: st.RuleKey, Status: st.Status, Since: st.Since, Value: st.LastValue})
	}
	var events []model.DeviceAlertEvent
	since := s.now().Add(-EventListWindow).UnixMilli()
	err = s.db.WithContext(ctx).Model(&model.DeviceAlertEvent{}).
		Where("owner_id = ? AND acked_at IS NULL AND ts >= ? AND device_id IN (?)", ownerID, since, live).
		Order("ts DESC, id DESC").Limit(MaxListedEvents).Find(&events).Error
	if err != nil {
		return nil, fmt.Errorf("list alert events: %w", err)
	}
	for _, ev := range events {
		out.Events = append(out.Events, EventView{
			ID: ev.ID, DeviceID: ev.DeviceID, Rule: ev.RuleKey, Kind: ev.Kind, TS: ev.TS,
			Value: ev.Value, Threshold: ev.Threshold, DurationMS: ev.DurationMS, Muted: ev.Muted,
		})
	}
	return out, nil
}

// Ack acknowledges the owner's events among ids; other owners' ids are ignored silently.
// It returns how many rows changed.
// GEN-TODO: DeviceAlertEvent ack update.
func (s *Service) Ack(ctx context.Context, ownerID string, ids []string) (int64, error) {
	if len(ids) == 0 || len(ids) > MaxAckIDs {
		return 0, ipc.BadParam(fmt.Errorf("确认的告警数量需在 1-%d 之间", MaxAckIDs))
	}
	for _, id := range ids {
		if id == "" {
			return 0, ipc.BadParam(errors.New("告警 ID 不能为空"))
		}
	}
	res := s.db.WithContext(ctx).Model(&model.DeviceAlertEvent{}).
		Where("owner_id = ? AND acked_at IS NULL AND id IN ?", ownerID, ids).
		Update("acked_at", s.now().UnixMilli())
	if res.Error != nil {
		return 0, fmt.Errorf("ack alert events: %w", res.Error)
	}
	return res.RowsAffected, nil
}

// EffectiveSettings is a device's settings with defaults filled in.
type EffectiveSettings struct {
	DeviceID        string  `json:"device_id"`
	Kind            string  `json:"kind"`
	DiskEnabled     bool    `json:"disk_enabled"`
	DiskThreshold   float64 `json:"disk_threshold"`
	MemEnabled      bool    `json:"mem_enabled"`
	MemThreshold    float64 `json:"mem_threshold"`
	CPUEnabled      bool    `json:"cpu_enabled"`
	CPUThreshold    float64 `json:"cpu_threshold"`
	OfflineEnabled  bool    `json:"offline_enabled"`
	OfflineMinutes  int64   `json:"offline_minutes"`
	ExpectedOffline bool    `json:"expected_offline"`
	QuietEnabled    bool    `json:"quiet_enabled"`
	QuietStart      int64   `json:"quiet_start"`
	QuietEnd        int64   `json:"quiet_end"`
	QuietTZ         string  `json:"quiet_tz"`
	ServerTZ        string  `json:"server_tz"`
	// ClockSkewMS is the device clock minus the server clock seen on its latest out-of-range metric
	// sample (0 when none was observed in the last hour); the fleet server fills it in.
	ClockSkewMS         int64 `json:"clock_skew_ms"`
	SuspectAfterMinutes int64 `json:"suspect_after_minutes"`
}

// SettingsInput is the PUT body; every field is required (nil is rejected) and the row is replaced whole.
type SettingsInput struct {
	DiskEnabled     *bool    `json:"disk_enabled"`
	DiskThreshold   *float64 `json:"disk_threshold"`
	MemEnabled      *bool    `json:"mem_enabled"`
	MemThreshold    *float64 `json:"mem_threshold"`
	CPUEnabled      *bool    `json:"cpu_enabled"`
	CPUThreshold    *float64 `json:"cpu_threshold"`
	OfflineEnabled  *bool    `json:"offline_enabled"`
	OfflineMinutes  *int64   `json:"offline_minutes"`
	ExpectedOffline *bool    `json:"expected_offline"`
	QuietEnabled    *bool    `json:"quiet_enabled"`
	QuietStart      *int64   `json:"quiet_start"`
	QuietEnd        *int64   `json:"quiet_end"`
	QuietTZ         string   `json:"quiet_tz"`
}

// Validate checks that every field is present and in range.
func (in SettingsInput) Validate() error {
	if in.DiskEnabled == nil || in.DiskThreshold == nil || in.MemEnabled == nil || in.MemThreshold == nil ||
		in.CPUEnabled == nil || in.CPUThreshold == nil || in.OfflineEnabled == nil || in.OfflineMinutes == nil ||
		in.ExpectedOffline == nil || in.QuietEnabled == nil || in.QuietStart == nil || in.QuietEnd == nil {
		return ipc.BadParam(errors.New("告警设置的所有字段都必须提供"))
	}
	for name, v := range map[string]float64{"磁盘": *in.DiskThreshold, "内存": *in.MemThreshold, "CPU": *in.CPUThreshold} {
		if math.IsNaN(v) || !ValidThreshold(v) {
			return ipc.BadParam(fmt.Errorf("%s阈值需在 %g-%g 之间", name, MinThreshold, MaxThreshold))
		}
	}
	if m := *in.OfflineMinutes; m < MinOfflineMinutes || m > MaxOfflineMinutes {
		return ipc.BadParam(fmt.Errorf("离线分钟数需在 %d-%d 之间", MinOfflineMinutes, MaxOfflineMinutes))
	}
	if *in.QuietEnabled {
		if err := ValidateQuiet(*in.QuietStart, *in.QuietEnd, in.QuietTZ); err != nil {
			return ipc.BadParam(fmt.Errorf("免打扰时段无效: %w", err))
		}
		return nil
	}
	if s, e := *in.QuietStart, *in.QuietEnd; s < 0 || s >= minutesPerDay || e < 0 || e >= minutesPerDay {
		return ipc.BadParam(fmt.Errorf("免打扰时间需在 0-%d 分钟之间", minutesPerDay-1))
	}
	if in.QuietTZ != "" {
		if _, err := time.LoadLocation(in.QuietTZ); err != nil {
			return ipc.BadParam(fmt.Errorf("未知时区 %q", in.QuietTZ))
		}
	}
	return nil
}

// GetSettings returns the effective settings of a device of the given kind.
// GEN-TODO: DeviceAlertSettings find by device.
func (s *Service) GetSettings(ctx context.Context, deviceID, kind string) (*EffectiveSettings, error) {
	var row model.DeviceAlertSettings
	err := s.db.WithContext(ctx).Model(&model.DeviceAlertSettings{}).Where("device_id = ?", deviceID).Take(&row).Error
	var set *Settings
	switch {
	case err == nil:
		set = &row
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return nil, fmt.Errorf("load alert settings: %w", err)
	}
	return s.effective(deviceID, kind, set), nil
}

func (s *Service) effective(deviceID, kind string, set *Settings) *EffectiveSettings {
	out := &EffectiveSettings{DeviceID: deviceID, Kind: kind, ServerTZ: time.Local.String()}
	out.DiskEnabled, out.DiskThreshold = ResolveThreshold(RuleDisk, set)
	out.MemEnabled, out.MemThreshold = ResolveThreshold(RuleMem, set)
	out.CPUEnabled, out.CPUThreshold = ResolveThreshold(RuleCPU, set)
	out.OfflineEnabled, out.OfflineMinutes = ResolveOffline(kind, set)
	if set != nil {
		out.ExpectedOffline, out.QuietEnabled = set.ExpectedOffline, set.QuietEnabled
		out.QuietStart, out.QuietEnd, out.QuietTZ = set.QuietStart, set.QuietEnd, set.QuietTZ
	}
	suspect := SuspectAfter(s.interval)
	out.SuspectAfterMinutes = int64((suspect + time.Minute - 1) / time.Minute)
	return out
}

// PutSettings validates and replaces the device's settings row, then returns the effective values.
// ownerID must be the device owner; the caller has already authorized the device.
func (s *Service) PutSettings(ctx context.Context, ownerID, deviceID, kind string, in SettingsInput) (*EffectiveSettings, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	row := &Settings{
		DeviceID: deviceID, OwnerID: ownerID,
		DiskEnabled: in.DiskEnabled, MemEnabled: in.MemEnabled, CPUEnabled: in.CPUEnabled, OfflineEnabled: in.OfflineEnabled,
		DiskThreshold: in.DiskThreshold, MemThreshold: in.MemThreshold, CPUThreshold: in.CPUThreshold,
		OfflineMinutes: in.OfflineMinutes, ExpectedOffline: *in.ExpectedOffline,
		QuietEnabled: *in.QuietEnabled, QuietStart: *in.QuietStart, QuietEnd: *in.QuietEnd, QuietTZ: in.QuietTZ,
		UpdatedAt: s.now().UnixMilli(),
	}
	err := s.db.WithContext(ctx).Model(&model.DeviceAlertSettings{}).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "device_id"}}, UpdateAll: true,
	}).Create(row).Error
	if err != nil {
		return nil, fmt.Errorf("save alert settings: %w", err)
	}
	return s.effective(deviceID, kind, row), nil
}
