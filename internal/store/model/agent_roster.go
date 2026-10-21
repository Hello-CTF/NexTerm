package model

// AgentRosterRow is the persistent, owner-isolated roster of agent conversation runtimes.
// A row is created provisioning-first with a preassigned TabID before any runtime exists;
// the (ID, CreatedAt, Incarnation) triple is the exact durable identity used for recovery,
// and the per-owner unique Handle is the permanent attach name that is never reused.
type AgentRosterRow struct {
	ID             string `gorm:"column:id;primaryKey" json:"id"`
	OwnerID        string `gorm:"size:191;column:owner_id;not null;default:'';uniqueIndex:idx_ai_roster_owner_handle,priority:1;index:idx_ai_roster_owner" json:"owner_id"`
	ConversationID string `gorm:"size:191;column:conversation_id;not null;index:idx_ai_roster_conv" json:"conversationId"`
	Handle         string `gorm:"size:191;column:handle;not null;uniqueIndex:idx_ai_roster_owner_handle,priority:2" json:"handle"`
	Alias          string `gorm:"size:191;column:alias;not null;default:''" json:"alias"`
	TabID          string `gorm:"size:191;column:tab_id;not null;default:''" json:"tabId"`
	State          string `gorm:"size:32;column:state;not null" json:"state"`
	Incarnation    string `gorm:"size:191;column:incarnation;not null" json:"incarnation"`
	JobID          string `gorm:"size:191;column:job_id;not null;default:''" json:"jobId"`
	MetadataJSON   string `gorm:"column:metadata_json;not null;default:'{}'" json:"metadataJson"`
	CreatedAt      int64  `gorm:"column:created_at;not null" json:"createdAt"`
	UpdatedAt      int64  `gorm:"column:updated_at;not null" json:"updatedAt"`
	ReleasedAt     *int64 `gorm:"column:released_at" json:"releasedAt"`
}

func (AgentRosterRow) TableName() string { return "ai_agent_roster" }

// AgentRosterIdentity is the exact durable identity of a roster row: the
// (ID, CreatedAt, Incarnation) triple tells the original row apart from a
// reused ID, so recovery and mutation never land on a replacement.
type AgentRosterIdentity struct {
	CreatedAt   int64
	Incarnation string
}

// Identity returns the exact durable identity of the row.
func (r AgentRosterRow) Identity() AgentRosterIdentity {
	return AgentRosterIdentity{CreatedAt: r.CreatedAt, Incarnation: r.Incarnation}
}
