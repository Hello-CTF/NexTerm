package model

// AgentEvidenceKeyRow is the per-owner evidence signer key: the trusted
// recorder-side component persists it so evidence anchors issued before a
// restart still verify afterwards. The key never leaves the store.
type AgentEvidenceKeyRow struct {
	ID        string `gorm:"column:id;primaryKey" json:"id"`
	OwnerID   string `gorm:"size:191;column:owner_id;not null;default:'';uniqueIndex:idx_ai_evidence_key_owner" json:"owner_id"`
	Key       []byte `gorm:"column:key;not null" json:"-"`
	CreatedAt int64  `gorm:"column:created_at;not null" json:"createdAt"`
	UpdatedAt int64  `gorm:"column:updated_at;not null" json:"updatedAt"`
}

func (AgentEvidenceKeyRow) TableName() string { return "ai_evidence_signer_key" }

// AgentEvidenceAnchorRow is a provenance anchor issued by the trusted
// signer, keyed by (owner, capture, chain head): a capture whose content
// moved away from an anchored head has no anchor row and no evidence.
type AgentEvidenceAnchorRow struct {
	OwnerID      string `gorm:"size:191;column:owner_id;not null;default:'';primaryKey" json:"owner_id"`
	CaptureID    string `gorm:"size:191;column:capture_id;not null;primaryKey" json:"captureId"`
	ChainHead    string `gorm:"size:64;column:chain_head;not null;primaryKey" json:"chainHead"`
	EnvelopeHash string `gorm:"size:64;column:envelope_hash;not null" json:"envelopeHash"`
	MAC          string `gorm:"size:64;column:mac;not null" json:"mac"`
	CreatedAt    int64  `gorm:"column:created_at;not null" json:"createdAt"`
}

func (AgentEvidenceAnchorRow) TableName() string { return "ai_evidence_anchor" }
