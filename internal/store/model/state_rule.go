package model

// StateRuleRow is a user-defined session state rule: when the screen region
// matches Pattern, the session is in TargetState. Scope "global" applies to
// every host; "host" applies to AssetID only. The ID is stable across
// devices and UpdatedAt orders concurrent edits, so rules can be exported,
// imported and synced.
type StateRuleRow struct {
	ID          string `gorm:"column:id;primaryKey" json:"id"`
	OwnerID     string `gorm:"column:owner_id;size:191;not null;default:'';index:idx_state_rule_owner" json:"-"`
	Name        string `gorm:"column:name;not null;default:''" json:"name"`
	Scope       string `gorm:"column:scope;size:16;not null" json:"scope"`
	AssetID     string `gorm:"column:asset_id;size:191;not null;default:''" json:"assetId"`
	Region      string `gorm:"column:region;size:16;not null" json:"region"`
	RegionLines int32  `gorm:"column:region_lines;not null;default:0" json:"lines"`
	Pattern     string `gorm:"column:pattern;not null" json:"pattern"`
	TargetState string `gorm:"column:target_state;size:32;not null" json:"state"`
	KeySequence string `gorm:"column:key_sequence;not null;default:''" json:"keys"`
	Enabled     bool   `gorm:"column:enabled;not null" json:"enabled"`
	CreatedAt   int64  `gorm:"column:created_at;not null" json:"createdAt"`
	UpdatedAt   int64  `gorm:"column:updated_at;not null" json:"updatedAt"`
}

func (StateRuleRow) TableName() string { return "state_rule" }
