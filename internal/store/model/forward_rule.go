package model

type ForwardRuleRow struct {
	ID         string `gorm:"column:id;primaryKey" json:"id"`
	OwnerID    string `gorm:"column:owner_id;size:191;not null;default:'';index:idx_forward_rule_owner" json:"-"`
	AssetID    string `gorm:"column:asset_id;size:191;not null;index:idx_forward_rule_asset" json:"assetId"`
	Name       string `gorm:"column:name;not null;default:''" json:"name"`
	ListenPort int32  `gorm:"column:listen_port;not null;check:chk_forward_rule_listen_port,listen_port BETWEEN 1 AND 65535" json:"listenPort"`
	TargetHost string `gorm:"column:target_host;not null" json:"targetHost"`
	TargetPort int32  `gorm:"column:target_port;not null;check:chk_forward_rule_target_port,target_port BETWEEN 1 AND 65535" json:"targetPort"`
	AutoStart  bool   `gorm:"column:auto_start;not null;default:0" json:"autoStart"`
	CreatedAt  int64  `gorm:"column:created_at;not null" json:"createdAt"`
	UpdatedAt  int64  `gorm:"column:updated_at;not null" json:"updatedAt"`
}

func (ForwardRuleRow) TableName() string { return "forward_rule" }
