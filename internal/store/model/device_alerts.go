package model

// DeviceAlertSettings holds a device's alert overrides; nil pointers mean the rule default applies.
type DeviceAlertSettings struct {
	DeviceID        string     `gorm:"size:191;column:device_id;primaryKey" json:"deviceId"`
	OwnerID         string     `gorm:"size:191;column:owner_id;not null;index:idx_device_alert_settings_owner" json:"ownerId"`
	DiskEnabled     *bool      `gorm:"column:disk_enabled" json:"diskEnabled"`
	MemEnabled      *bool      `gorm:"column:mem_enabled" json:"memEnabled"`
	CPUEnabled      *bool      `gorm:"column:cpu_enabled" json:"cpuEnabled"`
	OfflineEnabled  *bool      `gorm:"column:offline_enabled" json:"offlineEnabled"`
	DiskThreshold   *float64   `gorm:"column:disk_threshold" json:"diskThreshold"`
	MemThreshold    *float64   `gorm:"column:mem_threshold" json:"memThreshold"`
	CPUThreshold    *float64   `gorm:"column:cpu_threshold" json:"cpuThreshold"`
	OfflineMinutes  *int64     `gorm:"column:offline_minutes" json:"offlineMinutes"`
	ExpectedOffline bool       `gorm:"column:expected_offline;not null;default:false" json:"expectedOffline"`
	QuietEnabled    bool       `gorm:"column:quiet_enabled;not null;default:false" json:"quietEnabled"`
	QuietStart      int64      `gorm:"column:quiet_start;not null;default:0" json:"quietStart"`
	QuietEnd        int64      `gorm:"column:quiet_end;not null;default:0" json:"quietEnd"`
	QuietTZ         string     `gorm:"column:quiet_tz;not null;default:''" json:"quietTz"`
	UpdatedAt       int64      `gorm:"column:updated_at;not null" json:"updatedAt"`
	Device          UserDevice `gorm:"foreignKey:DeviceID;references:ID;constraint:OnDelete:CASCADE" json:"-"`
}

func (DeviceAlertSettings) TableName() string { return "device_alert_settings" }

// DeviceAlertState is the persisted per-device, per-rule state machine position.
type DeviceAlertState struct {
	DeviceID   string `gorm:"size:191;column:device_id;primaryKey" json:"deviceId"`
	RuleKey    string `gorm:"size:32;column:rule_key;primaryKey" json:"ruleKey"`
	OwnerID    string `gorm:"size:191;column:owner_id;not null;index:idx_device_alert_state_owner" json:"ownerId"`
	Status     string `gorm:"column:status;not null" json:"status"`
	Since      int64  `gorm:"column:since;not null;default:0" json:"since"`
	FiredAt    int64  `gorm:"column:fired_at;not null;default:0" json:"firedAt"`
	ResolvedAt int64  `gorm:"column:resolved_at;not null;default:0" json:"resolvedAt"`
	// RecoveringSince is when an offline alert's device was first heard again; 0 while not recovering.
	RecoveringSince int64      `gorm:"column:recovering_since;not null;default:0" json:"recoveringSince"`
	LastValue       *float64   `gorm:"column:last_value" json:"lastValue"`
	UpdatedAt       int64      `gorm:"column:updated_at;not null" json:"updatedAt"`
	Device          UserDevice `gorm:"foreignKey:DeviceID;references:ID;constraint:OnDelete:CASCADE" json:"-"`
}

func (DeviceAlertState) TableName() string { return "device_alert_state" }

// DeviceAlertEvent is one fired or resolved alert occurrence.
type DeviceAlertEvent struct {
	ID         string     `gorm:"column:id;primaryKey" json:"id"`
	OwnerID    string     `gorm:"size:191;column:owner_id;not null;index:idx_device_alert_event_owner_ts,priority:1" json:"ownerId"`
	DeviceID   string     `gorm:"size:191;column:device_id;not null;index:idx_device_alert_event_device" json:"deviceId"`
	RuleKey    string     `gorm:"size:32;column:rule_key;not null" json:"ruleKey"`
	Kind       string     `gorm:"size:16;column:kind;not null" json:"kind"`
	TS         int64      `gorm:"column:ts;not null;index:idx_device_alert_event_owner_ts,priority:2" json:"ts"`
	Value      *float64   `gorm:"column:value" json:"value"`
	Threshold  *float64   `gorm:"column:threshold" json:"threshold"`
	DurationMS int64      `gorm:"column:duration_ms;not null;default:0" json:"durationMs"`
	Muted      bool       `gorm:"column:muted;not null;default:false" json:"muted"`
	AckedAt    *int64     `gorm:"column:acked_at" json:"ackedAt"`
	Device     UserDevice `gorm:"foreignKey:DeviceID;references:ID;constraint:OnDelete:CASCADE" json:"-"`
}

func (DeviceAlertEvent) TableName() string { return "device_alert_event" }

// AlertEvaluatorState is the single-row bookkeeping of the device alert evaluator.
type AlertEvaluatorState struct {
	ID         string `gorm:"size:32;column:id;primaryKey" json:"id"`
	LastTickAt int64  `gorm:"column:last_tick_at;not null;default:0" json:"lastTickAt"`
	UpdatedAt  int64  `gorm:"column:updated_at;not null;default:0" json:"updatedAt"`
}

func (AlertEvaluatorState) TableName() string { return "alert_evaluator_state" }
