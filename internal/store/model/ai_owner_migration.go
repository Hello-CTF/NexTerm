package model

type AIOwnerMigration struct {
	Migration string `gorm:"size:64;column:migration;primaryKey" json:"migration"`
	Kind      string `gorm:"size:32;column:kind;primaryKey" json:"kind"`
	RecordID  string `gorm:"size:191;column:record_id;primaryKey" json:"recordId"`
	OwnerID   string `gorm:"size:191;column:owner_id;not null;default:''" json:"owner_id"`
	AddedAt   int64  `gorm:"column:added_at;not null" json:"addedAt"`
}

func (AIOwnerMigration) TableName() string { return "ai_owner_migration" }
