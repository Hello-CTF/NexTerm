package model

// AgentOutcomeRow is the durable outcome ledger for uncertain agent mutations.
// The (OwnerID, OperationID) key identifies one mutation attempt; completion is
// monotonic — initiated → succeeded|failed, never regressed — so a result query
// before a retry always returns the original operation's outcome. The ledger
// records outcomes; it never claims exactly-once execution.
type AgentOutcomeRow struct {
	OwnerID           string `gorm:"size:191;column:owner_id;primaryKey" json:"owner_id"`
	OperationID       string `gorm:"size:191;column:operation_id;primaryKey" json:"operationId"`
	Kind              string `gorm:"size:64;column:kind;not null" json:"kind"`
	State             string `gorm:"size:16;column:state;not null" json:"state"`
	FailureStage      string `gorm:"size:64;column:failure_stage;not null;default:''" json:"failureStage"`
	ResidualSessionID string `gorm:"size:191;column:residual_session_id;not null;default:''" json:"residualSessionId"`
	ResidualResource  string `gorm:"size:191;column:residual_resource;not null;default:''" json:"residualResource"`
	ResultJSON        string `gorm:"column:result_json;not null;default:'{}'" json:"resultJson"`
	Error             string `gorm:"column:error;not null;default:''" json:"error"`
	CreatedAt         int64  `gorm:"column:created_at;not null" json:"createdAt"`
	UpdatedAt         int64  `gorm:"column:updated_at;not null" json:"updatedAt"`
	FinishedAt        *int64 `gorm:"column:finished_at" json:"finishedAt"`
}

func (AgentOutcomeRow) TableName() string { return "ai_agent_outcome" }
